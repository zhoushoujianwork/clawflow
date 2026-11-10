package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

func listOpts() gitlab.PipelineListOpts { return gitlab.PipelineListOpts{Limit: 5} }

// siteServer is a stand-in GitLab: it accepts one token and records requests.
type siteServer struct {
	srv   *httptest.Server
	token string
	mu    sync.Mutex
	got   []string // "METHOD path token"
}

func newSiteServer(t *testing.T, token string) *siteServer {
	t.Helper()
	s := &siteServer{token: token}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.got = append(s.got, r.Method+" "+r.URL.EscapedPath()+" "+r.Header.Get("PRIVATE-TOKEN"))
		s.mu.Unlock()
		if r.Header.Get("PRIVATE-TOKEN") != s.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/runners"):
			_, _ = w.Write([]byte(`[{"id":1,"description":"r","active":true,"status":"online"}]`))
		case strings.Contains(r.URL.Path, "/pipelines"):
			_, _ = w.Write([]byte(`[{"id":11,"status":"success","ref":"main","sha":"` + strings.Repeat("a", 40) + `"}]`))
		default:
			_, _ = w.Write([]byte(`{"id":5}`))
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *siteServer) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

// twoSiteHome writes a config with sites a (old, opted into the legacy token)
// and b, a repo with the same path on each, and per-site credentials.
func twoSiteHome(t *testing.T, a, b *siteServer) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAWFLOW_STATE_DIR", "")
	for _, k := range []string{"GITLAB_TOKEN", "GITLAB_TOKEN_A", "GITLAB_TOKEN_B", "CLAWFLOW_DEV"} {
		t.Setenv(k, "")
	}
	cfg := &config.Config{Repos: map[string]config.Repo{
		"team/tools/service":   {Platform: "gitlab", BaseURL: a.srv.URL, Enabled: true},
		"b:team/tools/service": {Platform: "gitlab", Site: "b", BaseURL: b.srv.URL, ProjectPath: "team/tools/service", Enabled: true},
		"acme/service":         {Platform: "github", Enabled: true},
	}}
	if err := cfg.UpsertGitLabSite(config.GitLabSite{Name: "a", URL: a.srv.URL, Version: "11.11", UseLegacyToken: true}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpsertGitLabSite(config.GitLabSite{Name: "b", URL: b.srv.URL, Version: "17.0"}); err != nil {
		t.Fatal(err)
	}
	cfg.BindGitLabRepos()
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveCredentials(&config.Credentials{GitLabToken: a.token, GitLabTokens: map[string]string{"b": b.token}}); err != nil {
		t.Fatal(err)
	}
}

// Alternating CLI-level lookups reach each site with its own credential only,
// and changing site b afterwards leaves site a working.
func TestAlternatingQueriesUseEachSitesOwnCredential(t *testing.T) {
	a, b := newSiteServer(t, "token-a"), newSiteServer(t, "token-b")
	twoSiteHome(t, a, b)

	for i := 0; i < 2; i++ {
		for _, repo := range []string{"a:team/tools/service", "b:team/tools/service"} {
			c, key, err := newGitLabClientForRepo(repo)
			if err != nil {
				t.Fatalf("%s: %v", repo, err)
			}
			if _, err := c.ListPipelines(key, listOpts()); err != nil {
				t.Fatalf("%s pipelines: %v", repo, err)
			}
		}
	}
	for name, s := range map[string]*siteServer{"a": a, "b": b} {
		if len(s.requests()) != 2 {
			t.Errorf("site %s got %d requests: %v", name, len(s.requests()), s.requests())
		}
		for _, r := range s.requests() {
			if !strings.HasSuffix(r, " "+s.token) {
				t.Errorf("site %s got a foreign credential: %s", name, r)
			}
		}
	}

	// Edit site b (new version, new capability limits); a keeps working untouched.
	cfg, _ := config.Load()
	b2, _ := cfg.Settings.GitLabSite("b")
	b2.Version, b2.Unsupported = "17.1", []string{vcs.CapRunners}
	if err := cfg.UpsertGitLabSite(b2); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	ca, key, err := newGitLabClientForRepo("a:team/tools/service")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ca.ListProjectRunners(key); err != nil {
		t.Fatalf("site a runners after editing b: %v", err)
	}
	cb, key, _ := newGitLabClientForRepo("b:team/tools/service")
	if _, err := cb.ListProjectRunners(key); !errors.Is(err, vcs.ErrNotSupported) {
		t.Fatalf("site b runners must be unsupported: %v", err)
	}
	// The next CLI call already sees the saved change: no restart needed.
	if n := len(b.requests()); n != 2 {
		t.Errorf("unsupported call reached site b: %v", b.requests())
	}
}

func TestShorthandSharedByTwoSitesIsAmbiguousAtEveryEntryPoint(t *testing.T) {
	a, b := newSiteServer(t, "token-a"), newSiteServer(t, "token-b")
	twoSiteHome(t, a, b)

	for name, fn := range map[string]func() error{
		"vcs": func() error { _, _, _, err := newVCSClientForRepo("team/tools/service"); return err },
		"ci":  func() error { _, _, err := newGitLabClientForRepo("team/tools/service"); return err },
		"repo": func() error {
			cfg, _ := config.Load()
			_, _, err := configuredRepo(cfg, "team/tools/service")
			return err
		},
	} {
		err := fn()
		var amb *config.AmbiguousError
		if !errors.As(err, &amb) {
			t.Errorf("%s entry point: want AmbiguousError, got %v", name, err)
		}
	}
	if len(a.requests())+len(b.requests()) != 0 {
		t.Errorf("an ambiguous argument sent requests: %v %v", a.requests(), b.requests())
	}
	// The queue takes the same rule.
	cfg, _ := config.Load()
	if _, err := parseQueueKey(cfg, "team/tools/service#4"); err == nil {
		t.Error("queue key with an ambiguous repo was accepted")
	}
	if k, err := parseQueueKey(cfg, "b:team/tools/service#4"); err != nil || k.Repo != "b:team/tools/service" || k.Number != 4 {
		t.Errorf("explicit queue key: %+v, %v", k, err)
	}
}

// A 401 from one site is final: it is reported for that site and nothing is
// retried against the other site, whose credential is never sent.
func TestUnauthorizedDoesNotFailOverToAnotherSite(t *testing.T) {
	a, b := newSiteServer(t, "token-a"), newSiteServer(t, "token-b")
	twoSiteHome(t, a, b)
	if err := config.SaveCredentials(&config.Credentials{GitLabToken: a.token, GitLabTokens: map[string]string{"b": "stale"}}); err != nil {
		t.Fatal(err)
	}
	c, key, err := newGitLabClientForRepo("b:team/tools/service")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListPipelines(key, listOpts())
	var ae *vcs.AuthError
	if !errors.As(err, &ae) || ae.Site != "b" {
		t.Fatalf("want AuthError for b, got %v", err)
	}
	if strings.Contains(err.Error(), "stale") || strings.Contains(err.Error(), "token-a") {
		t.Errorf("error leaks a credential: %v", err)
	}
	if len(a.requests()) != 0 {
		t.Errorf("site a was contacted after b rejected its credential: %v", a.requests())
	}
}

func TestSiteWithoutCredentialDoesNotBorrowTheLegacyToken(t *testing.T) {
	a, b := newSiteServer(t, "token-a"), newSiteServer(t, "token-b")
	twoSiteHome(t, a, b)
	if err := config.SaveCredentials(&config.Credentials{GitLabToken: a.token}); err != nil {
		t.Fatal(err)
	}
	_, _, err := newGitLabClientForRepo("b:team/tools/service")
	var ae *vcs.AuthError
	if !errors.As(err, &ae) || !ae.NoCred || ae.Site != "b" {
		t.Fatalf("want NoCred AuthError for b, got %v", err)
	}
	if len(a.requests())+len(b.requests()) != 0 {
		t.Errorf("requests were sent: %v %v", a.requests(), b.requests())
	}
}

func TestGitHubAndGitLabSameNameNeverCross(t *testing.T) {
	a, b := newSiteServer(t, "token-a"), newSiteServer(t, "token-b")
	twoSiteHome(t, a, b)
	if _, _, err := newGitLabClientForRepo("acme/service"); err == nil || !strings.Contains(err.Error(), "not a GitLab project") {
		t.Fatalf("a GitHub repo reached the GitLab client: %v", err)
	}
	cfg, _ := config.Load()
	cfg.Repos["b:acme/service"] = config.Repo{Platform: "gitlab", Site: "b", BaseURL: b.srv.URL, ProjectPath: "acme/service", Enabled: true}
	cfg.BindGitLabRepos()
	if _, _, err := configuredRepo(cfg, "acme/service"); err == nil {
		t.Fatal("shorthand shared by a GitHub repo and a GitLab project must be ambiguous")
	}
}

func TestRepoListAndShowNamePlatformSiteAndPath(t *testing.T) {
	a, b := newSiteServer(t, "token-a"), newSiteServer(t, "token-b")
	twoSiteHome(t, a, b)

	var out bytes.Buffer
	cmd := newRepoListCmd()
	cmd.SetOut(&out)
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"site=a", "site=b", b.srv.URL, "team/tools/service", "github acme/service"} {
		if !strings.Contains(text, want) {
			t.Errorf("repo list lacks %q:\n%s", want, text)
		}
	}
	for _, secret := range []string{"token-a", "token-b"} {
		if strings.Contains(text, secret) {
			t.Errorf("repo list leaks %s", secret)
		}
	}
}

func TestSiteCommandsAddSetListKeepOtherSitesAndHideTokens(t *testing.T) {
	a, b := newSiteServer(t, "token-a"), newSiteServer(t, "token-b")
	twoSiteHome(t, a, b)

	run := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		cmd := NewGitLabCmd()
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("gitlab %v: %v\n%s", args, err, out.String())
		}
		return out.String()
	}
	run("site", "add", "c", "--url", "https://gitlab-c.example.com:8443/gitlab", "--version", "16.4", "--ssh-host", "ssh.c.example.com:2222", "--unsupported", vcs.CapRunners)
	if got := run("site", "set-token", "c", "super-secret-token-9999"); strings.Contains(got, "super-secret") {
		t.Errorf("set-token echoed the token: %s", got)
	}
	run("site", "set", "c", "--version", "16.5")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Settings.GitLabSites) != 3 {
		t.Fatalf("sites: %+v", cfg.Settings.GitLabSites)
	}
	c, _ := cfg.Settings.GitLabSite("c")
	if c.Version != "16.5" || c.URL != "https://gitlab-c.example.com:8443/gitlab" || len(c.SSHHosts) != 1 || !c.Lacks(vcs.CapRunners) {
		t.Fatalf("site c = %+v", c)
	}
	if a2, _ := cfg.Settings.GitLabSite("a"); a2.URL != a.srv.URL || !a2.UseLegacyToken {
		t.Fatalf("site a changed: %+v", a2)
	}
	creds, _ := config.LoadCredentials()
	if creds.GitLabTokens["b"] != "token-b" || creds.GitLabToken != "token-a" {
		t.Fatalf("existing credentials changed: %+v", creds)
	}
	listing := run("site", "list")
	for _, want := range []string{"a", "b", "c", "16.5", "set (credentials.yaml gitlab_tokens.c)"} {
		if !strings.Contains(listing, want) {
			t.Errorf("site list lacks %q:\n%s", want, listing)
		}
	}
	if strings.Contains(listing, "super-secret") || strings.Contains(listing, "token-b") || strings.Contains(listing, "token-a") {
		t.Errorf("site list leaks a token:\n%s", listing)
	}
	raw, _ := os.ReadFile(config.ConfigPath())
	if strings.Contains(string(raw), "super-secret") {
		t.Error("token written to config.yaml")
	}
	var rows []SiteRow
	if err := json.Unmarshal([]byte(run("site", "list", "--json")), &rows); err != nil || len(rows) != 3 {
		t.Fatalf("json list: %v %v", rows, err)
	}

	// Resolving shows the selected target without any network access.
	got := run("site", "resolve", "ssh://git@ssh.c.example.com:2222/team/tools/service.git")
	if !strings.Contains(got, "site=c") || !strings.Contains(got, "configured") && !strings.Contains(got, "transient") {
		t.Errorf("resolve output: %s", got)
	}
	// A site that still has repositories cannot be removed.
	cmd := NewGitLabCmd()
	cmd.SetArgs([]string{"site", "remove", "b"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err == nil {
		t.Error("removed a site that still has repositories")
	}
}
