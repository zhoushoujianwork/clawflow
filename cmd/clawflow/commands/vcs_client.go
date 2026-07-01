package commands

import (
	"fmt"
	"os"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/pilot/budget"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

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
	platform := repo.Platform
	if platform == "" {
		platform = "github"
	}
	var base vcs.Client
	switch platform {
	case "github":
		base = github.New(creds.GHToken, repo.BaseURL)
	case "gitlab":
		if repo.BaseURL == "" {
			return nil, fmt.Errorf("repo platform is gitlab but base_url is not set")
		}
		base = gitlab.New(creds.GitLabToken, repo.BaseURL)
	default:
		return nil, fmt.Errorf("unsupported platform %q", platform)
	}
	if os.Getenv(budget.EnvPath) != "" {
		return budget.WrapClient(base), nil
	}
	return base, nil
}

// newVCSClientForRepo loads config and returns a client for the named repo,
// plus the canonical "owner/repo" (or GitLab "namespace/project") identifier
// that VCS API calls expect. Callers MUST use the returned canonical name when
// invoking client methods — repoName may be a full URL, which the API layer
// cannot split into owner/name.
//
// The repo need not be added via `clawflow repo add`: when it isn't in the
// config, a transient config is inferred from the argument and the API is
// tried directly. Access is enforced by the VCS (401/403/404), not membership.
func newVCSClientForRepo(repoName string) (vcs.Client, string, config.Repo, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "", config.Repo{}, err
	}
	if repo, ok := cfg.Repos[repoName]; ok {
		client, err := newVCSClient(repo)
		return client, repoName, repo, err
	}

	// Not a configured repo. Infer a transient config from the argument. The
	// arg may be "owner/repo", a github.com URL, or a self-hosted GitLab URL
	// (https://git.example.com/group/proj) — the URL form is the only way to
	// learn a self-hosted GitLab base_url, since it can't be inferred from
	// "owner/repo" alone.
	info, err := config.ParseRepoInput(repoName, cfg.Settings.GitLabHosts)
	if err != nil {
		return nil, "", config.Repo{}, fmt.Errorf("repo %q not in config and could not be parsed: %w", repoName, err)
	}
	// The URL might actually point at a repo that IS configured (added by its
	// owner/repo key). Prefer the stored config so its settings apply.
	if repo, ok := cfg.Repos[info.OwnerRepo]; ok {
		client, err := newVCSClient(repo)
		return client, info.OwnerRepo, repo, err
	}
	repo := config.Repo{
		Platform: info.Platform,
		BaseURL:  info.BaseURL,
		Owner:    info.OwnerRepo,
	}
	client, err := newVCSClient(repo)
	return client, info.OwnerRepo, repo, err
}

// newGitLabClientForRepo resolves repoName to a concrete *gitlab.Client for
// GitLab-only operations (CI/CD runners, pipelines, job logs) that have no
// platform-agnostic vcs.Client equivalent. It errors when the resolved repo is
// not a GitLab project. Resolution mirrors newVCSClientForRepo: configured repo
// first, then a transient config inferred from a URL / owner/repo argument.
//
// These calls are reads, so — unlike newVCSClient — the client is never wrapped
// in the Pilot budget decorator (which only gates writes).
func newGitLabClientForRepo(repoName string) (*gitlab.Client, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "", err
	}
	build := func(repo config.Repo, canonical string) (*gitlab.Client, string, error) {
		platform := repo.Platform
		if platform == "" {
			platform = "github"
		}
		if platform != "gitlab" {
			return nil, "", fmt.Errorf("repo %q is not a GitLab project (platform=%q); ci commands are GitLab-only", canonical, platform)
		}
		if repo.BaseURL == "" {
			return nil, "", fmt.Errorf("repo %q is gitlab but base_url is not set", canonical)
		}
		creds, err := config.LoadCredentials()
		if err != nil {
			return nil, "", fmt.Errorf("load credentials: %w", err)
		}
		return gitlab.New(creds.GitLabToken, repo.BaseURL), canonical, nil
	}
	if repo, ok := cfg.Repos[repoName]; ok {
		return build(repo, repoName)
	}
	info, err := config.ParseRepoInput(repoName, cfg.Settings.GitLabHosts)
	if err != nil {
		return nil, "", fmt.Errorf("repo %q not in config and could not be parsed: %w", repoName, err)
	}
	if repo, ok := cfg.Repos[info.OwnerRepo]; ok {
		return build(repo, info.OwnerRepo)
	}
	return build(config.Repo{
		Platform: info.Platform,
		BaseURL:  info.BaseURL,
		Owner:    info.OwnerRepo,
	}, info.OwnerRepo)
}
