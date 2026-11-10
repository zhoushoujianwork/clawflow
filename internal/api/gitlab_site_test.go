package api

import (
	"errors"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func sitesCfg(t *testing.T, sites ...config.GitLabSite) *config.Config {
	t.Helper()
	cfg := &config.Config{Repos: map[string]config.Repo{}}
	for _, s := range sites {
		if err := cfg.UpsertGitLabSite(s); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

// With several sites registered, the remote project list needs an explicit
// choice: nothing is defaulted to the first site or to the legacy token.
func TestListRemoteRequiresSiteWhenSeveralAreRegistered(t *testing.T) {
	t.Setenv("GITLAB_TOKEN_A", "")
	t.Setenv("GITLAB_TOKEN_B", "")
	cfg := sitesCfg(t,
		config.GitLabSite{Name: "a", URL: "http://a.example.com", UseLegacyToken: true},
		config.GitLabSite{Name: "b", URL: "https://b.example.com:8443/gitlab"})
	creds := &config.Credentials{GitLabToken: "legacy", GitLabTokens: map[string]string{"b": "tok-b"}}

	_, _, _, status, msg := gitlabListSource(cfg, creds, "")
	if status != 400 || !strings.Contains(msg, "choose one") || !strings.Contains(msg, "a, b") {
		t.Fatalf("no site given: status=%d msg=%q", status, msg)
	}
	tok, base, site, status, msg := gitlabListSource(cfg, creds, "b")
	if msg != "" || tok != "tok-b" || base != "https://b.example.com:8443/gitlab" || site != "b" {
		t.Fatalf("site b: %q %q %q %d %q", tok, base, site, status, msg)
	}
	tok, base, _, _, msg = gitlabListSource(cfg, creds, "a")
	if msg != "" || tok != "legacy" || base != "http://a.example.com" {
		t.Fatalf("site a (opted in to the legacy token): %q %q %q", tok, base, msg)
	}
	if _, _, _, status, msg := gitlabListSource(cfg, creds, "zz"); status != 400 || !strings.Contains(msg, "unknown GitLab site") {
		t.Fatalf("unknown site: %d %q", status, msg)
	}
}

func TestListRemoteSiteWithoutCredentialDoesNotUseLegacyToken(t *testing.T) {
	t.Setenv("GITLAB_TOKEN_B", "")
	cfg := sitesCfg(t,
		config.GitLabSite{Name: "a", URL: "http://a.example.com", UseLegacyToken: true},
		config.GitLabSite{Name: "b", URL: "https://b.example.com"})
	tok, _, _, _, msg := gitlabListSource(cfg, &config.Credentials{GitLabToken: "legacy"}, "b")
	if tok != "" || !strings.Contains(msg, "No credential is configured for GitLab site b") {
		t.Fatalf("token=%q msg=%q", tok, msg)
	}
}

// Without registered sites the pre-existing single-site behavior is unchanged.
func TestListRemoteLegacySingleSiteUnchanged(t *testing.T) {
	cfg := &config.Config{Repos: map[string]config.Repo{}}
	cfg.Settings.GitLabHosts = []string{"gitlab.example.com"}
	tok, base, site, _, msg := gitlabListSource(cfg, &config.Credentials{GitLabToken: "legacy"}, "")
	if msg != "" || tok != "legacy" || site != "" || !strings.HasPrefix(base, "http") || !strings.Contains(base, "gitlab.example.com") {
		t.Fatalf("legacy: %q %q %q %q", tok, base, site, msg)
	}
	if _, _, _, _, msg := gitlabListSource(cfg, &config.Credentials{}, ""); !strings.Contains(msg, "token not configured") {
		t.Fatalf("legacy without token: %q", msg)
	}
}

func TestGitLabClientForRepoUsesOnlyItsSiteCredential(t *testing.T) {
	t.Setenv("GITLAB_TOKEN_B", "")
	cfg := sitesCfg(t,
		config.GitLabSite{Name: "a", URL: "http://a.example.com", UseLegacyToken: true},
		config.GitLabSite{Name: "b", URL: "https://b.example.com"})
	cfg.Repos["b:g/p"] = config.Repo{Platform: "gitlab", Site: "b", ProjectPath: "g/p"}
	cfg.BindGitLabRepos()
	repo := cfg.Repos["b:g/p"]

	_, err := gitlabClientForRepo(repo, &config.Credentials{GitLabToken: "legacy"})
	var he *httpError
	if !errors.As(err, &he) || he.Code != 401 || !strings.Contains(he.Msg, "no credential is configured for GitLab site b") {
		t.Fatalf("site b must not use the legacy token: %v", err)
	}
	c, err := gitlabClientForRepo(repo, &config.Credentials{GitLabToken: "legacy", GitLabTokens: map[string]string{"b": "tok-b"}})
	if err != nil || c.SiteName() != "b" || c.SiteURL() != "https://b.example.com" {
		t.Fatalf("client: %v %v", c, err)
	}
	var _ vcs.Client = c
}
