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

// newVCSClientForRepo loads config and returns a client for the named repo.
func newVCSClientForRepo(repoName string) (vcs.Client, config.Repo, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, config.Repo{}, err
	}
	repo, ok := cfg.Repos[repoName]
	if !ok {
		return nil, config.Repo{}, fmt.Errorf("repo %q not found in config", repoName)
	}
	client, err := newVCSClient(repo)
	return client, repo, err
}
