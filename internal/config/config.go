// Package config loads and parses ~/.clawflow/config/repos.yaml.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Repo holds per-repository settings.
type Repo struct {
	Enabled          bool              `yaml:"enabled"`
	BaseBranch       string            `yaml:"base_branch"`
	LocalPath        string            `yaml:"local_path"`
	Owner            string            `yaml:"owner"`
	Description      string            `yaml:"description"`
	AddedAt          string            `yaml:"added_at"`
	WebhookConfigured bool             `yaml:"webhook_configured"`
	Labels           map[string]string `yaml:"labels"`
}

// Settings holds global ClawFlow settings.
type Settings struct {
	PollInterval        int    `yaml:"poll_interval"`
	ConfidenceThreshold int    `yaml:"confidence_threshold"`
	AgentTimeout        int    `yaml:"agent_timeout"`
	MaxConcurrentAgents int    `yaml:"max_concurrent_agents"`
	NotificationChannel string `yaml:"notification_channel"`
}

// Config is the top-level config file structure.
type Config struct {
	Repos    map[string]Repo `yaml:"repos"`
	Settings Settings        `yaml:"settings"`
}

// Load reads the config from ~/.clawflow/config/repos.yaml.
func Load() (*Config, error) {
	path := ConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("cannot parse config: %w", err)
	}
	return &cfg, nil
}

// ConfigPath returns the canonical config file path.
func ConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "config", "repos.yaml")
}

// MemoryDir returns the memory directory for a repo.
func MemoryDir(repoSlug string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "memory", "repos", repoSlug)
}

// EnabledRepos returns only the repos with enabled: true.
func (c *Config) EnabledRepos() map[string]Repo {
	out := make(map[string]Repo)
	for k, v := range c.Repos {
		if v.Enabled {
			out[k] = v
		}
	}
	return out
}

// RepoSlug converts "owner/repo" to "owner-repo" for use in file paths.
func RepoSlug(ownerRepo string) string {
	for i, c := range ownerRepo {
		if c == '/' {
			return ownerRepo[:i] + "-" + ownerRepo[i+1:]
		}
	}
	return ownerRepo
}

// WorktreePath returns the standard worktree path for an issue.
func WorktreePath(ownerRepo string, issueNumber int) string {
	return fmt.Sprintf("/tmp/clawflow-fix/%s-issue-%d", RepoSlug(ownerRepo), issueNumber)
}

// BranchName returns the standard branch name for an issue.
func BranchName(issueNumber int) string {
	return fmt.Sprintf("fix/issue-%d", issueNumber)
}
