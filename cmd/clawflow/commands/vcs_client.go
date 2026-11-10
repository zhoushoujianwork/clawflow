package commands

import (
	"context"
	"fmt"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"os"
	"path/filepath"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/devmode"
	"github.com/zhoushoujianwork/clawflow/internal/pilot/budget"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

// gitlabSiteClient builds the GitLab client of the site a repository is bound
// to. A registered site uses only its own credential: never the legacy global
// token (unless that site opted in) and never another site's token. A legacy
// repository, which names no registered site, keeps using base_url and the
// legacy token it has always used.
func gitlabSiteClient(repo config.Repo, creds *config.Credentials) (*gitlab.Client, error) {
	if repo.BaseURL == "" {
		return nil, fmt.Errorf("repo platform is gitlab but base_url is not set")
	}
	if repo.Site == "" {
		return gitlab.New(creds.GitLabToken, repo.BaseURL), nil
	}
	if repo.SiteCfg == nil {
		return nil, fmt.Errorf("GitLab site %q is not registered (clawflow gitlab site list)", repo.Site)
	}
	token, _ := creds.SiteToken(*repo.SiteCfg)
	if token == "" {
		return nil, &vcs.AuthError{Site: repo.Site, URL: repo.SiteCfg.URL, NoCred: true}
	}
	return gitlab.NewForSite(gitlab.Site{Name: repo.Site, URL: repo.SiteCfg.URL, Token: token, Unsupported: repo.SiteCfg.Unsupported}), nil
}

// newVCSClient returns a VCS client for the given repo config.
// When the process is running as part of a Pilot wake (env var
// CLAWFLOW_PILOT_BUDGET_PATH is set), the returned client is wrapped in a
// budget decorator that enforces a per-wake hard cap on VCS write
// operations. Reads pass through unchanged.
func newVCSClient(repo config.Repo) (vcs.Client, error) {
	creds, err := config.LoadCredentials()
	if err != nil {
		return nil, fmt.Errorf("load credentials: %w", err)
	}
	if devmode.Active() {
		if err := devmode.CheckBaseURL(repo.BaseURL); err != nil {
			return nil, err
		}
	}
	platform := repo.Platform
	if platform == "" {
		platform = "github"
	}
	var base vcs.Client
	switch platform {
	case "github":
		c := github.New(creds.GHToken, repo.BaseURL)
		c.UseCacheDir(filepath.Join(snapshot.DataDir(), "github-http"))
		base = c
	case "gitlab":
		c, err := gitlabSiteClient(repo, creds)
		if err != nil {
			return nil, err
		}
		base = c
	default:
		return nil, fmt.Errorf("unsupported platform %q", platform)
	}
	if os.Getenv(budget.EnvPath) != "" {
		return budget.WrapClient(base), nil
	}
	return base, nil
}

// newVCSClientForRepo loads config and returns a client for the named repo,
// plus the canonical config key that VCS API calls expect. Callers MUST use the
// returned key when invoking client methods: repoName may be a full URL, an
// SSH address or a shorthand, none of which the API layer can split.
//
// Resolution is config.Resolve, shared by every command and the API: a full URL
// or alias:path selects its own site, a shorthand with several candidates fails
// as ambiguous, and nothing falls back to another platform, site or credential.
// The repo need not be added via `clawflow repo add`; access is then enforced by
// the VCS (401/403/404) on the one site the argument names.
func newVCSClientForRepo(repoName string) (vcs.Client, string, config.Repo, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "", config.Repo{}, err
	}
	t, err := cfg.Resolve(repoName)
	if err != nil {
		return nil, "", config.Repo{}, err
	}
	client, err := newVCSClient(t.Repo)
	return client, t.Key, t.Repo, err
}

// newGitLabClientForRepo resolves repoName to a concrete *gitlab.Client for
// GitLab-only operations (CI/CD runners, pipelines, job logs) that have no
// platform-agnostic vcs.Client equivalent. It errors when the resolved repo is
// not a GitLab project. Resolution is the shared config.Resolve.
//
// The concrete client cannot use the platform-agnostic Pilot budget decorator.
// Commands that write through this client must call budget.Reserve themselves.
func newGitLabClientForRepo(repoName string) (*gitlab.Client, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "", err
	}
	t, err := cfg.Resolve(repoName)
	if err != nil {
		return nil, "", err
	}
	if t.Platform != "gitlab" {
		return nil, "", fmt.Errorf("repo %q is not a GitLab project (%s); ci commands are GitLab-only", t.Key, t.Describe())
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		return nil, "", fmt.Errorf("load credentials: %w", err)
	}
	if devmode.Active() {
		if err := devmode.CheckBaseURL(t.Repo.BaseURL); err != nil {
			return nil, "", err
		}
	}
	client, err := gitlabSiteClient(t.Repo, creds)
	return client, t.Key, err
}

// configuredRepo resolves a user-supplied repository argument to an existing
// config entry through the shared config.Resolve rules (full URL, alias:path,
// SSH address or an unambiguous shorthand). It returns the canonical key.
func configuredRepo(cfg *config.Config, input string) (string, config.Repo, error) {
	t, err := cfg.ResolveConfigured(input)
	if err != nil {
		return "", config.Repo{}, err
	}
	return t.Key, t.Repo, nil
}

// repoSiteOf names the platform site a repository ID is read from.
func repoSiteOf(r config.Repo) snapshot.RepoSite {
	if r.Platform != "gitlab" {
		return snapshot.RepoSite{}
	}
	return snapshot.RepoSite{Name: r.Site, URL: r.BaseURL}
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// pollingAllowed only gates discovery; delivery and writes retain the reserve.
func pollingAllowed(client vcs.Client) bool {
	if c, ok := client.(interface{ PollingAllowed() bool }); ok {
		return c.PollingAllowed()
	}
	return true
}
func setVCSContext(client vcs.Client, ctx context.Context) {
	if c, ok := client.(interface{ SetRequestContext(context.Context) }); ok {
		c.SetRequestContext(ctx)
	}
}
