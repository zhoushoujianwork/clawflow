package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolatedHome points the config root at a temp dir so Load/Save and
// credentials never touch the real ~/.clawflow.
func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAWFLOW_STATE_DIR", "")
	t.Setenv("GITLAB_TOKEN", "")
	t.Setenv("GITLAB_TOKEN_A", "")
	t.Setenv("GITLAB_TOKEN_B", "")
	return home
}

func writeConfig(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".clawflow", "config")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A config written before sites existed loads unchanged, keeps its repos on
// their legacy site, and registering a second site neither rebinds them nor
// hands them (or the new site) a credential they did not have.
func TestLegacySingleSiteConfigMigratesWithoutCopyingTokens(t *testing.T) {
	home := isolatedHome(t)
	writeConfig(t, home, `repos:
  team/tools/service:
    platform: gitlab
    base_url: http://gitlab-a.example.com
    enabled: true
settings:
  gitlab_hosts: ["http://gitlab-a.example.com"]
`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Repos["team/tools/service"]
	if r.Site != "" || r.SiteCfg != nil || r.BaseURL != "http://gitlab-a.example.com" {
		t.Fatalf("legacy repo must stay legacy until a site is registered: %+v", r)
	}
	creds := &Credentials{GitLabToken: "legacy-token"}
	if got := r.GitLabCredential(creds); got != "legacy-token" {
		t.Fatalf("legacy repo lost its credential: %q", got)
	}

	// Register site B (new host). The legacy repo is untouched and B has no token.
	if err := cfg.UpsertGitLabSite(GitLabSite{Name: "b", URL: "https://gitlab-b.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	b, ok := cfg.Settings.GitLabSite("b")
	if !ok {
		t.Fatal("site b not persisted")
	}
	if tok, _ := creds.SiteToken(b); tok != "" {
		t.Fatalf("legacy token was copied to the new site: %q", tok)
	}
	r = cfg.Repos["team/tools/service"]
	if r.Site != "" || r.GitLabCredential(creds) != "legacy-token" {
		t.Fatalf("legacy repo changed after registering b: %+v", r)
	}
	raw, _ := os.ReadFile(ConfigPath())
	if strings.Contains(string(raw), "legacy-token") || strings.Contains(string(raw), "site_cfg") || strings.Contains(string(raw), "SiteCfg") {
		t.Fatalf("config file leaked a token or the runtime-only site field:\n%s", raw)
	}
}

// Registering the legacy host as a site binds the existing repo explicitly,
// and only that site (opted in) keeps using the old global token.
func TestRegisteringExistingHostBindsRepoAndKeepsOptInToken(t *testing.T) {
	home := isolatedHome(t)
	writeConfig(t, home, `repos:
  team/tools/service:
    platform: gitlab
    base_url: http://gitlab-a.example.com
    enabled: true
`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpsertGitLabSite(GitLabSite{Name: "a", URL: "http://gitlab-a.example.com", UseLegacyToken: true}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.UpsertGitLabSite(GitLabSite{Name: "b", URL: "https://gitlab-b.example.com"}); err != nil {
		t.Fatal(err)
	}
	cfg.BindGitLabRepos()
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Repos["team/tools/service"]
	if r.Site != "a" || r.ProjectPath != "team/tools/service" || r.SiteCfg == nil {
		t.Fatalf("repo not bound to site a after reload: %+v", r)
	}
	creds := &Credentials{GitLabToken: "legacy-token"}
	if got := r.GitLabCredential(creds); got != "legacy-token" {
		t.Fatalf("opted-in site a credential = %q", got)
	}
	b, _ := cfg.Settings.GitLabSite("b")
	if tok, _ := creds.SiteToken(b); tok != "" {
		t.Fatalf("site b borrowed the legacy token: %q", tok)
	}
}

func TestSiteTokensPersistPerSiteInCredentialsFile(t *testing.T) {
	isolatedHome(t)
	creds := &Credentials{}
	creds.SetSiteToken("a", "token-a")
	creds.SetSiteToken("b", "token-b")
	if err := SaveCredentials(creds); err != nil {
		t.Fatal(err)
	}
	creds, err := LoadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	creds.SetSiteToken("b", "token-b2")
	if err := SaveCredentials(creds); err != nil {
		t.Fatal(err)
	}
	creds, _ = LoadCredentials()
	if creds.GitLabTokens["a"] != "token-a" || creds.GitLabTokens["b"] != "token-b2" {
		t.Fatalf("per-site tokens not independent: %+v", creds.GitLabTokens)
	}
	info, err := os.Stat(CredentialsPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file mode = %v, %v", info.Mode(), err)
	}
}
