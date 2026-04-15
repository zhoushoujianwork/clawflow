package commands

import (
	"fmt"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

// newVCSClient returns a VCS client for the given repo config.
func newVCSClient(repo config.Repo) (vcs.Client, error) {
	creds, err := config.LoadCredentials()
	if err != nil {
		return nil, fmt.Errorf("load credentials: %w", err)
	}
	platform := repo.Platform
	if platform == "" {
		platform = "github"
	}
	switch platform {
	case "github":
		return github.New(creds.GHToken, repo.BaseURL), nil
	case "gitlab":
		if repo.BaseURL == "" {
			return nil, fmt.Errorf("repo platform is gitlab but base_url is not set")
		}
		return gitlab.New(creds.GitLabToken, repo.BaseURL), nil
	default:
		return nil, fmt.Errorf("unsupported platform %q", platform)
	}
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
