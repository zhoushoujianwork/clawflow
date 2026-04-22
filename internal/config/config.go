// Package config loads and parses ~/.clawflow/config/config.yaml.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// envOrFile returns the env value if set, otherwise the file value.
func envOrFile(envKey, fileVal string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	return fileVal
}

// Repo holds per-repository settings.
type Repo struct {
	Enabled           bool              `yaml:"enabled"`
	Platform          string            `yaml:"platform,omitempty"`   // "github" (default) or "gitlab"
	BaseURL           string            `yaml:"base_url,omitempty"`   // GitLab self-hosted instance URL
	BaseBranch        string            `yaml:"base_branch"`
	LocalPath         string            `yaml:"local_path"`
	Owner             string            `yaml:"owner"`
	Description       string            `yaml:"description"`
	AddedAt           string            `yaml:"added_at"`
	WebhookConfigured bool              `yaml:"webhook_configured"`
	Labels            map[string]string `yaml:"labels"`
	TestCommand       string            `yaml:"test_command,omitempty"`
	CIRequired        bool              `yaml:"ci_required,omitempty"`
	CITimeout         int               `yaml:"ci_timeout,omitempty"`
	AutoMerge         bool              `yaml:"auto_merge,omitempty"`
	AutoFix           bool              `yaml:"auto_fix,omitempty"`
}

// Settings holds global ClawFlow settings.
type Settings struct {
	PollInterval        int      `yaml:"poll_interval"`
	ConfidenceThreshold int      `yaml:"confidence_threshold"`
	AgentTimeout        int      `yaml:"agent_timeout"`
	MaxConcurrentAgents int      `yaml:"max_concurrent_agents"`
	NotificationChannel string   `yaml:"notification_channel"`
	GitLabHosts         []string `yaml:"gitlab_hosts"`                       // e.g. ["gitlab.company.com"]
	GithubCloneDir      string   `yaml:"github_clone_dir,omitempty"`         // default: ~/github
	GitlabCloneDir      string   `yaml:"gitlab_clone_dir,omitempty"`         // default: ~/gitlab
}

// ResolveGithubCloneDir returns the configured GitHub clone directory, defaulting to ~/github.
func (s *Settings) ResolveGithubCloneDir() string {
	if s.GithubCloneDir != "" {
		return expandHome(s.GithubCloneDir)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "github")
}

// ResolveGitlabCloneDir returns the configured GitLab clone directory, defaulting to ~/gitlab.
func (s *Settings) ResolveGitlabCloneDir() string {
	if s.GitlabCloneDir != "" {
		return expandHome(s.GitlabCloneDir)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "gitlab")
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

// Config is the top-level config file structure.
type Config struct {
	Repos    map[string]Repo `yaml:"repos"`
	Settings Settings        `yaml:"settings"`
}

// Credentials holds sensitive config.
type Credentials struct {
	GHToken     string `yaml:"gh_token,omitempty"`
	GitLabToken string `yaml:"gitlab_token,omitempty"`
}

// CredentialsPath returns the path to the credentials file.
func CredentialsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "config", "credentials.yaml")
}

// LoadCredentials reads ~/.clawflow/config/credentials.yaml and merges env vars.
// Priority: env > credentials.yaml
// Supported env vars: GH_TOKEN, GITLAB_TOKEN
func LoadCredentials() (*Credentials, error) {
	c := &Credentials{}
	data, err := os.ReadFile(CredentialsPath())
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := yaml.Unmarshal(data, c); err != nil {
			return nil, err
		}
	}
	c.GHToken = envOrFile("GH_TOKEN", c.GHToken)
	c.GitLabToken = envOrFile("GITLAB_TOKEN", c.GitLabToken)
	return c, nil
}

// Save writes the config back to ~/.clawflow/config/config.yaml.
func (c *Config) Save() error {
	if c.Repos == nil {
		c.Repos = make(map[string]Repo)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("cannot marshal config: %w", err)
	}
	path := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// SaveCredentials writes credentials with restricted permissions (0600).
func SaveCredentials(c *Credentials) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	path := CredentialsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// Load reads the config from ~/.clawflow/config/config.yaml.
// If config.yaml does not exist but the legacy repos.yaml does, it migrates automatically.
func Load() (*Config, error) {
	path := ConfigPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// migrate from legacy repos.yaml
		legacyPath := filepath.Join(filepath.Dir(path), "repos.yaml")
		data, err = os.ReadFile(legacyPath)
		if err != nil {
			return nil, fmt.Errorf("cannot read config %s: %w", path, err)
		}
		var cfg Config
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("cannot parse config: %w", err)
		}
		if err := cfg.Save(); err != nil {
			return nil, fmt.Errorf("cannot migrate config to %s: %w", path, err)
		}
		_ = os.Rename(legacyPath, legacyPath+".bak")
		return &cfg, nil
	}
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
	return filepath.Join(home, ".clawflow", "config", "config.yaml")
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

// RepoInfo is the result of ParseRepoInput.
type RepoInfo struct {
	OwnerRepo string // "owner/repo" or "namespace/repo"
	Platform  string // "github" or "gitlab"
	BaseURL   string // instance root URL (empty for github.com)
	LocalPath string // set when input was a local directory
}

// ReadGitRemoteURL reads the origin remote URL from a local git repo's .git/config.
func ReadGitRemoteURL(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		return "", err
	}
	inOrigin := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == `[remote "origin"]` {
			inOrigin = true
			continue
		}
		if inOrigin {
			if strings.HasPrefix(line, "[") {
				break // moved to next section
			}
			if strings.HasPrefix(line, "url =") {
				return strings.TrimSpace(strings.TrimPrefix(line, "url =")), nil
			}
		}
	}
	return "", fmt.Errorf("no remote origin found in %s/.git/config", dir)
}

// ParseRepoInput parses a repo argument which may be:
//   - "owner/repo"                          → github (default)
//   - "https://github.com/owner/repo"       → github
//   - "https://gitlab.company.com/ns/repo"  → gitlab
//   - "git@github.com:owner/repo.git"       → github (SSH)
//   - "git@gitlab.company.com:ns/repo.git"  → gitlab (SSH)
//   - "/local/path" or "."                  → reads .git/config origin URL
//
// gitlabHosts comes from Settings.GitLabHosts.
func ParseRepoInput(input string, gitlabHosts []string) (RepoInfo, error) {
	input = strings.TrimSpace(input)
	input = strings.TrimSuffix(input, ".git")

	// Local path: starts with / . or ~ or is a directory
	if input == "." || strings.HasPrefix(input, "/") || strings.HasPrefix(input, "~/") || strings.HasPrefix(input, "./") {
		dir := input
		if strings.HasPrefix(dir, "~/") {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, dir[2:])
		}
		remoteURL, err := ReadGitRemoteURL(dir)
		if err != nil {
			return RepoInfo{}, fmt.Errorf("cannot read git remote from %q: %w", input, err)
		}
		info, err := ParseRepoInput(remoteURL, gitlabHosts)
		if err != nil {
			return RepoInfo{}, err
		}
		info.LocalPath = dir
		return info, nil
	}

	// SSH URL: git@host:owner/repo or git@host:ns/group/repo
	if strings.HasPrefix(input, "git@") {
		rest := strings.TrimPrefix(input, "git@")
		colonIdx := strings.Index(rest, ":")
		if colonIdx < 0 {
			return RepoInfo{}, fmt.Errorf("invalid SSH URL %q", input)
		}
		host := strings.ToLower(rest[:colonIdx])
		fullPath := rest[colonIdx+1:]
		if host == "github.com" {
			// GitHub: always owner/repo
			parts := strings.SplitN(fullPath, "/", 3)
			return RepoInfo{OwnerRepo: parts[0] + "/" + parts[1], Platform: "github"}, nil
		}
		// GitLab: keep full path
		baseURL := "https://" + host
		for _, h := range gitlabHosts {
			if strings.ToLower(strings.TrimSpace(h)) == host {
				return RepoInfo{OwnerRepo: fullPath, Platform: "gitlab", BaseURL: baseURL}, nil
			}
		}
		return RepoInfo{OwnerRepo: fullPath, Platform: "gitlab", BaseURL: baseURL}, nil
	}

	// Not a URL — plain "owner/repo"
	if !strings.HasPrefix(input, "http://") && !strings.HasPrefix(input, "https://") {
		if !strings.Contains(input, "/") {
			return RepoInfo{}, fmt.Errorf("repo must be owner/repo, a full URL, or a local path, got %q", input)
		}
		return RepoInfo{OwnerRepo: input, Platform: "github"}, nil
	}

	u, err := url.Parse(input)
	if err != nil {
		return RepoInfo{}, fmt.Errorf("invalid URL %q: %w", input, err)
	}

	host := strings.ToLower(u.Hostname())
	fullPath := strings.TrimPrefix(u.Path, "/")

	if host == "github.com" {
		// GitHub: always owner/repo (2 segments)
		parts := strings.SplitN(fullPath, "/", 3)
		if len(parts) < 2 {
			return RepoInfo{}, fmt.Errorf("URL %q does not contain owner/repo path", input)
		}
		return RepoInfo{OwnerRepo: parts[0] + "/" + parts[1], Platform: "github"}, nil
	}

	// GitLab (known host or self-hosted): keep full path as project identifier
	if fullPath == "" || !strings.Contains(fullPath, "/") {
		return RepoInfo{}, fmt.Errorf("URL %q does not contain a valid project path", input)
	}
	baseURL := u.Scheme + "://" + u.Host
	for _, h := range gitlabHosts {
		if strings.ToLower(strings.TrimSpace(h)) == host {
			return RepoInfo{OwnerRepo: fullPath, Platform: "gitlab", BaseURL: baseURL}, nil
		}
	}
	// unknown host — assume gitlab self-hosted
	return RepoInfo{OwnerRepo: fullPath, Platform: "gitlab", BaseURL: baseURL}, nil
}
