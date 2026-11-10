package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// twoSiteConfig models the issue's scenario: an old GitLab A and a newer
// GitLab B, both holding team/tools/service, B on another port with a
// deployment prefix and an SSH host that differs from its API host.
func twoSiteConfig(t *testing.T) *Config {
	t.Helper()
	cfg := &Config{}
	cfg.Repos = map[string]Repo{
		// Pre-existing single-site entry: bare key, base_url only.
		"team/tools/service": {Platform: "gitlab", BaseURL: "http://gitlab-a.example.com", Enabled: true},
		// GitHub repo with a path that collides with nothing but a GitHub name.
		"acme/service": {Platform: "github", Enabled: true},
	}
	must := func(s GitLabSite) {
		if err := cfg.UpsertGitLabSite(s); err != nil {
			t.Fatal(err)
		}
	}
	must(GitLabSite{Name: "a", URL: "http://gitlab-a.example.com", Version: "11.11", UseLegacyToken: true, Unsupported: []string{vcs.CapIssueEvents}})
	must(GitLabSite{Name: "b", URL: "https://gitlab-b.example.com:8443/gitlab", Version: "17.0", SSHHosts: []string{"ssh.gitlab-b.example.com:2222"}})
	cfg.BindGitLabRepos()
	cfg.Repos["b:team/tools/service"] = Repo{Platform: "gitlab", Site: "b", ProjectPath: "team/tools/service", BaseURL: "https://gitlab-b.example.com:8443/gitlab", Enabled: true}
	cfg.BindGitLabRepos()
	return cfg
}

func TestNormalizeSiteURL(t *testing.T) {
	cases := map[string]string{
		"GitLab.Example.com":                       "https://gitlab.example.com",
		"https://gitlab.example.com:443/":          "https://gitlab.example.com",
		"http://gitlab.example.com:80":             "http://gitlab.example.com",
		"https://x.example.com:8443/gitlab/api/v4": "https://x.example.com:8443/gitlab",
		"https://x.example.com/gitlab/":            "https://x.example.com/gitlab",
	}
	for in, want := range cases {
		got, err := NormalizeSiteURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeSiteURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://x", "https://u:p@x.example.com", "https://x.example.com?a=b", "https://x.example.com/#f"} {
		if _, err := NormalizeSiteURL(bad); err == nil {
			t.Errorf("NormalizeSiteURL(%q) accepted", bad)
		}
	}
}

func TestSiteChangesDoNotAffectOtherSites(t *testing.T) {
	cfg := twoSiteConfig(t)
	a, _ := cfg.Settings.GitLabSite("a")
	if err := cfg.UpsertGitLabSite(GitLabSite{Name: "b", URL: "https://gitlab-b.example.com:9443", Version: "17.1"}); err != nil {
		t.Fatal(err)
	}
	after, _ := cfg.Settings.GitLabSite("a")
	if a.URL != after.URL || a.Version != after.Version || !after.UseLegacyToken || !after.Lacks(vcs.CapIssueEvents) {
		t.Fatalf("site a changed when b was edited: %+v -> %+v", a, after)
	}
	b, _ := cfg.Settings.GitLabSite("b")
	if b.URL != "https://gitlab-b.example.com:9443" || b.Version != "17.1" {
		t.Fatalf("site b not updated: %+v", b)
	}
}

func TestSiteValidationRejectsConflicts(t *testing.T) {
	cfg := twoSiteConfig(t)
	if err := cfg.UpsertGitLabSite(GitLabSite{Name: "c", URL: "http://gitlab-a.example.com"}); err == nil {
		t.Error("duplicate site URL accepted")
	}
	if err := cfg.UpsertGitLabSite(GitLabSite{Name: "c", URL: "https://c.example.com", SSHHosts: []string{"ssh.gitlab-b.example.com:2222"}}); err == nil {
		t.Error("SSH host bound to two sites accepted")
	}
	if err := cfg.UpsertGitLabSite(GitLabSite{Name: "c", URL: "https://c.example.com", UseLegacyToken: true}); err == nil {
		t.Error("second legacy-token site accepted")
	}
	if err := cfg.UpsertGitLabSite(GitLabSite{Name: "c", URL: "https://c.example.com", Unsupported: []string{"nope"}}); err == nil {
		t.Error("unknown capability accepted")
	}
	if err := cfg.UpsertGitLabSite(GitLabSite{Name: "Bad Name", URL: "https://c.example.com"}); err == nil {
		t.Error("invalid site name accepted")
	}
	if err := cfg.RemoveGitLabSite("b"); err == nil || !strings.Contains(err.Error(), "b:team/tools/service") {
		t.Errorf("removing a site with repos must fail naming them, got %v", err)
	}
}

func TestLegacyRepoBindsOnlyWhenURLMatchesSite(t *testing.T) {
	cfg := twoSiteConfig(t)
	legacy := cfg.Repos["team/tools/service"]
	if legacy.Site != "a" || legacy.BaseURL != "http://gitlab-a.example.com" || legacy.ProjectPath != "team/tools/service" {
		t.Fatalf("legacy repo not bound to site a: %+v", legacy)
	}
	cfg2 := &Config{Repos: map[string]Repo{"x/y": {Platform: "gitlab", BaseURL: "https://other.example.com"}}}
	_ = cfg2.UpsertGitLabSite(GitLabSite{Name: "a", URL: "http://gitlab-a.example.com"})
	cfg2.BindGitLabRepos()
	if cfg2.Repos["x/y"].Site != "" || cfg2.Repos["x/y"].SiteCfg != nil {
		t.Fatalf("repo of an unregistered host was bound to a site: %+v", cfg2.Repos["x/y"])
	}
}

func TestResolveFullURLSelectsOwningSite(t *testing.T) {
	cfg := twoSiteConfig(t)
	cases := []struct {
		in, key, site, base, path string
	}{
		{"http://gitlab-a.example.com/team/tools/service", "team/tools/service", "a", "http://gitlab-a.example.com", "team/tools/service"},
		{"https://gitlab-b.example.com:8443/gitlab/team/tools/service", "b:team/tools/service", "b", "https://gitlab-b.example.com:8443/gitlab", "team/tools/service"},
		{"https://gitlab-b.example.com:8443/gitlab/team/tools/service/-/merge_requests/3", "b:team/tools/service", "b", "https://gitlab-b.example.com:8443/gitlab", "team/tools/service"},
		{"b:team/tools/service", "b:team/tools/service", "b", "https://gitlab-b.example.com:8443/gitlab", "team/tools/service"},
		{"https://gitlab-b.example.com:8443/gitlab/team/tools/service.git", "b:team/tools/service", "b", "https://gitlab-b.example.com:8443/gitlab", "team/tools/service"},
	}
	for _, c := range cases {
		got, err := cfg.Resolve(c.in)
		if err != nil {
			t.Errorf("Resolve(%q): %v", c.in, err)
			continue
		}
		if got.Key != c.key || got.Site != c.site || got.BaseURL != c.base || got.Path != c.path || got.Platform != "gitlab" || !got.Configured {
			t.Errorf("Resolve(%q) = %+v", c.in, got)
		}
	}
	// A URL on a host that no site owns is rejected, not routed to a default site.
	if _, err := cfg.Resolve("https://gitlab-c.example.com/team/tools/service"); err == nil || !strings.Contains(err.Error(), "does not belong to any registered GitLab site") {
		t.Errorf("unregistered host: %v", err)
	}
	// Same host, other port: not site b.
	if _, err := cfg.Resolve("https://gitlab-b.example.com:9443/gitlab/team/tools/service"); err == nil {
		t.Error("URL on a different port selected a site")
	}
	// Same host and port without the deployment prefix: not site b either.
	if _, err := cfg.Resolve("https://gitlab-b.example.com:8443/team/tools/service"); err == nil {
		t.Error("URL missing the deployment prefix selected a site")
	}
}

func TestResolveShorthandAmbiguityAndUniqueness(t *testing.T) {
	cfg := twoSiteConfig(t)
	_, err := cfg.Resolve("team/tools/service")
	var amb *AmbiguousError
	if !errors.As(err, &amb) || len(amb.Candidates) != 2 {
		t.Fatalf("shorthand shared by two sites must be ambiguous, got %v", err)
	}
	for _, want := range []string{"site=a", "site=b", "team/tools/service", "https://gitlab-b.example.com:8443/gitlab"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ambiguity error lacks %q: %v", want, err)
		}
	}
	// Removing B's repo makes the shorthand unique again.
	delete(cfg.Repos, "b:team/tools/service")
	got, err := cfg.Resolve("team/tools/service")
	if err != nil || got.Site != "a" || got.Platform != "gitlab" {
		t.Fatalf("unique shorthand: %+v, %v", got, err)
	}
	// A GitHub owner/repo never resolves to GitLab and vice versa.
	gh, err := cfg.Resolve("acme/service")
	if err != nil || gh.Platform != "github" || gh.Site != "" {
		t.Fatalf("github shorthand: %+v, %v", gh, err)
	}
}

func TestResolveNeverFallsBackToGitHub(t *testing.T) {
	cfg := twoSiteConfig(t)
	if _, err := cfg.Resolve("team/tools/unknown"); err == nil || !strings.Contains(err.Error(), "nested path is a GitLab project") {
		t.Fatalf("unconfigured nested path must not become GitHub: %v", err)
	}
	// Two segments that match no config are the one GitHub shape.
	if got, err := cfg.Resolve("someone/else"); err != nil || got.Platform != "github" || got.Configured {
		t.Fatalf("plain owner/repo: %+v, %v", got, err)
	}
	if _, err := cfg.Resolve("zz:team/tools/service"); err == nil || !strings.Contains(err.Error(), "unknown GitLab site") {
		t.Fatalf("unknown alias: %v", err)
	}
}

func TestResolveSameNameGitHubAndGitLab(t *testing.T) {
	cfg := &Config{Repos: map[string]Repo{
		"acme/service":   {Platform: "github"},
		"b:acme/service": {Platform: "gitlab", Site: "b", ProjectPath: "acme/service", BaseURL: "https://b.example.com"},
	}}
	_ = cfg.UpsertGitLabSite(GitLabSite{Name: "b", URL: "https://b.example.com"})
	cfg.BindGitLabRepos()
	if _, err := cfg.Resolve("acme/service"); err == nil {
		t.Fatal("a shorthand matching a GitHub repo and a GitLab project must be ambiguous")
	}
	if got, _ := cfg.Resolve("https://github.com/acme/service"); got.Platform != "github" || got.Key != "acme/service" {
		t.Fatalf("github URL: %+v", got)
	}
	if got, _ := cfg.Resolve("https://b.example.com/acme/service"); got.Platform != "gitlab" || got.Key != "b:acme/service" {
		t.Fatalf("gitlab URL: %+v", got)
	}
}

func TestResolveSSHUsesExplicitBindingsOnly(t *testing.T) {
	cfg := twoSiteConfig(t)
	got, err := cfg.Resolve("ssh://git@ssh.gitlab-b.example.com:2222/team/tools/service.git")
	if err != nil || got.Site != "b" || got.Key != "b:team/tools/service" {
		t.Fatalf("bound ssh host: %+v, %v", got, err)
	}
	// SSH host equal to site A's API host.
	got, err = cfg.Resolve("git@gitlab-a.example.com:team/tools/service.git")
	if err != nil || got.Site != "a" {
		t.Fatalf("same-host ssh: %+v, %v", got, err)
	}
	// Right host, wrong port: not bound, so it is not guessed.
	if _, err := cfg.Resolve("ssh://git@ssh.gitlab-b.example.com:2223/team/tools/service.git"); err == nil || !strings.Contains(err.Error(), "not bound to any GitLab site") {
		t.Errorf("unbound ssh port: %v", err)
	}
	// An SSH host unrelated to any site is not mapped to one.
	if _, err := cfg.Resolve("git@elsewhere.example.com:team/tools/service.git"); err == nil {
		t.Error("unknown ssh host was mapped to a site")
	}
	// Even though gitlab-b's API host matches, it has an SSH host binding so the
	// API host alone still selects B only when no other site shares that host.
	got, err = cfg.Resolve("git@gitlab-b.example.com:team/tools/service.git")
	if err != nil || got.Site != "b" {
		t.Fatalf("api-host ssh: %+v, %v", got, err)
	}
	// Two sites on one host demand an explicit binding.
	_ = cfg.UpsertGitLabSite(GitLabSite{Name: "b2", URL: "https://gitlab-b.example.com:7443"})
	if _, err := cfg.Resolve("git@gitlab-b.example.com:team/tools/service.git"); err == nil || !strings.Contains(err.Error(), "matches several GitLab sites") {
		t.Errorf("shared ssh host must be ambiguous: %v", err)
	}
}

func TestSiteTokenNeverBorrowed(t *testing.T) {
	t.Setenv("GITLAB_TOKEN_B", "")
	t.Setenv("GITLAB_TOKEN_A", "")
	cfg := twoSiteConfig(t)
	creds := &Credentials{GitLabToken: "legacy-token"}
	a, _ := cfg.Settings.GitLabSite("a")
	b, _ := cfg.Settings.GitLabSite("b")

	if tok, src := creds.SiteToken(a); tok != "legacy-token" || !strings.Contains(src, "opt-in") {
		t.Errorf("site a (opted in) = %q %q", tok, src)
	}
	// B never inherits the legacy token and never sees A's.
	if tok, _ := creds.SiteToken(b); tok != "" {
		t.Errorf("site b received a token it was not given: %q", tok)
	}
	creds.SetSiteToken("b", "token-b")
	if tok, _ := creds.SiteToken(b); tok != "token-b" {
		t.Errorf("site b token = %q", tok)
	}
	if tok, _ := creds.SiteToken(a); tok != "legacy-token" {
		t.Errorf("setting b's token changed a's: %q", tok)
	}
	creds.SetSiteToken("b", "")
	if tok, _ := creds.SiteToken(b); tok != "" {
		t.Errorf("cleared token still returned: %q", tok)
	}
	t.Setenv("GITLAB_TOKEN_B", "env-b")
	if tok, src := creds.SiteToken(b); tok != "env-b" || !strings.Contains(src, "GITLAB_TOKEN_B") {
		t.Errorf("env token = %q %q", tok, src)
	}
	// Without opt-in, the legacy token is not used by a registered site.
	a.UseLegacyToken = false
	if tok, _ := creds.SiteToken(a); tok != "" {
		t.Errorf("site without opt-in got the legacy token: %q", tok)
	}
	// A legacy (unbound) repository keeps using the legacy token, and a bound
	// repository whose site is missing gets nothing.
	if got := (Repo{Platform: "gitlab"}).GitLabCredential(creds); got != "legacy-token" {
		t.Errorf("legacy repo credential = %q", got)
	}
	if got := (Repo{Platform: "gitlab", Site: "zz"}).GitLabCredential(creds); got != "" {
		t.Errorf("unregistered-site repo credential = %q", got)
	}
}

func TestRepoSlugFSKeepsSitesApart(t *testing.T) {
	if RepoSlugFS("team/tools/service") == RepoSlugFS("b:team/tools/service") {
		t.Fatal("same path on two sites shares a directory slug")
	}
	if RepoSlugFS("a:team/service") == RepoSlugFS("b:team/service") {
		t.Fatal("two sites share a slug")
	}
	for _, key := range []string{"owner/repo", "team/tools/service", "b:team/tools/service", "site-1:g/p"} {
		if got := RepoFromSlugFS(RepoSlugFS(key)); got != key {
			t.Errorf("slug round trip %q -> %q", key, got)
		}
	}
	if RepoSlugFS("owner/repo") != "owner__repo" {
		t.Errorf("existing slugs must not change: %s", RepoSlugFS("owner/repo"))
	}
}

func TestDescribeRepoNamesPlatformSiteAndPath(t *testing.T) {
	cfg := twoSiteConfig(t)
	got := DescribeRepo("b:team/tools/service", cfg.Repos["b:team/tools/service"])
	for _, want := range []string{"gitlab", "site=b", "https://gitlab-b.example.com:8443/gitlab", "team/tools/service"} {
		if !strings.Contains(got, want) {
			t.Errorf("DescribeRepo lacks %q: %s", want, got)
		}
	}
	if got := DescribeRepo("acme/service", Repo{Platform: "github"}); got != "github acme/service" {
		t.Errorf("github describe = %q", got)
	}
}
