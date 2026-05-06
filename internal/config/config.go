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
	Platform          string            `yaml:"platform,omitempty"` // "github" (default) or "gitlab"
	BaseURL           string            `yaml:"base_url,omitempty"` // GitLab self-hosted instance URL
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
	AutoApprove       bool              `yaml:"auto_approve,omitempty"`
	// AutoEvaluateAllIssues, when true, makes the discover loop fetch every
	// open issue on the repo (not just those carrying the trigger label) and
	// hand each one to local Claude scoring. Low-score runs get filtered by
	// SaaS threshold rather than by the user's labelling discipline. Default
	// false preserves the original "label is gatekeeper" behaviour. Pushed
	// from SaaS via /sync/config (issue #28).
	AutoEvaluateAllIssues bool `yaml:"auto_evaluate_all_issues,omitempty"`
}

// Settings holds global ClawFlow settings.
type Settings struct {
	PollInterval        int      `yaml:"poll_interval"`
	ConfidenceThreshold int      `yaml:"confidence_threshold"`
	AgentTimeout        int      `yaml:"agent_timeout"`
	MaxConcurrentAgents int      `yaml:"max_concurrent_agents"`
	NotificationChannel string   `yaml:"notification_channel"`
	GitLabHosts         []string `yaml:"gitlab_hosts"`               // e.g. ["gitlab.company.com"] or ["http://git.internal.com:8080"]
	GithubCloneDir      string   `yaml:"github_clone_dir,omitempty"` // default: ~/github
	GitlabCloneDir      string   `yaml:"gitlab_clone_dir,omitempty"` // default: ~/gitlab

	// RunIntervalMinutes drives the optional periodic auto-runner that
	// `clawflow web` embeds. 0 disables it (manual Run button only).
	// Any positive integer N = fire `clawflow run` every N minutes,
	// gated by the same in-process mutex the manual button uses so
	// they can never overlap.
	RunIntervalMinutes int `yaml:"run_interval_minutes,omitempty"`

	// RunPaused, when true, suppresses periodic ticks while leaving
	// `RunIntervalMinutes` unchanged. The dashboard's Pause/Resume
	// button toggles this. Persisted so a paused state survives a web
	// restart — losing it on restart would silently re-enable runs the
	// user explicitly stopped.
	RunPaused bool `yaml:"run_paused,omitempty"`

	// Terminal selects which terminal emulator `clawflow web` opens for
	// chat sessions. Supported values:
	//   ""          / "system" — platform default (Terminal.app on macOS)
	//   "vscode"    — VS Code integrated terminal (supports image paste)
	//   "iterm"     — iTerm2 on macOS
	//   custom path — any executable; invoked as `<path> -e <cmdLine>`
	Terminal string `yaml:"terminal,omitempty"`

	// BillingCycleDay is the day of month (1-28) when a billing period
	// starts. Usage is aggregated into monthly periods aligned to this
	// day. 0 or unset defaults to 1 (calendar month).
	BillingCycleDay int `yaml:"billing_cycle_day,omitempty"`

	// MaxConsecutiveFailures is the number of consecutive failed runs on
	// the same (repo, issue) before the runner auto-adds `agent-failed`
	// to stop retrying. 0 or unset defaults to 3.
	MaxConsecutiveFailures int `yaml:"max_consecutive_failures,omitempty"`
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

	// ClaudeAPIKey, when set, overrides whatever auth the user's
	// system claude would use (OAuth keychain, ~/.claude.json) by
	// passing ANTHROPIC_API_KEY to every claude subprocess clawflow
	// spawns. Useful for routing through a proxy / relay or pinning
	// to a specific account separate from the user's interactive
	// Claude Code login.
	ClaudeAPIKey string `yaml:"claude_api_key,omitempty"`

	// ClaudeBaseURL, when set, is forwarded as ANTHROPIC_BASE_URL.
	// Typically paired with ClaudeAPIKey when targeting a relay.
	ClaudeBaseURL string `yaml:"claude_base_url,omitempty"`

	// ClaudeChatModel / ClaudeEvalModel / ClaudeOperatorModel are
	// the three claude `--model` overrides clawflow uses, scoped by
	// what the subprocess does. Stored in credentials.yaml because
	// they ship next to api_key/base_url; values themselves aren't
	// secret. Empty = fall back to the built-in default returned by
	// EffectiveChat/Eval/OperatorModel — the user's
	// ~/.claude/settings.json model is never inherited, because we
	// always pass `--model` so a broken global default can't break
	// clawflow.
	//
	//	ChatModel     — `clawflow chat` REPL (analysis, fast turns).
	//	                Default: "haiku".
	//	EvalModel     — operators whose name starts with "evaluate-"
	//	                (currently evaluate-bug, evaluate-feat).
	//	                Default: "claude-opus-4-7".
	//	OperatorModel — every other operator (implement, reply-comment,
	//	                user-supplied skills).
	//	                Default: "sonnet".
	ClaudeChatModel     string `yaml:"claude_chat_model,omitempty"`
	ClaudeEvalModel     string `yaml:"claude_eval_model,omitempty"`
	ClaudeOperatorModel string `yaml:"claude_operator_model,omitempty"`
}

// Default model identifiers used when the corresponding Credentials
// field is empty. Centralized here so the API, CLI, and operator
// runner all return the same answer.
//
// We use the family aliases (haiku / sonnet / opus) rather than
// pinned IDs because they're the only form that works across every
// provider clawflow targets:
//
//   - Anthropic native API resolves them to the current latest.
//   - cc-proxy and most third-party Anthropic-compatible proxies
//     accept the same aliases.
//   - Kiro's local proxy doesn't list them in /v1/models but
//     accepts them anyway via fuzzy fallback.
//
// The downside is opacity about the exact pinned version, but a
// user who needs that pins via the settings dropdown — these
// constants are just the safe out-of-the-box default.
const (
	DefaultChatModel     = "haiku"
	DefaultEvalModel     = "opus"
	DefaultOperatorModel = "sonnet"
)

// EffectiveChatModel returns the configured chat model, or the
// built-in default if unset.
func (c *Credentials) EffectiveChatModel() string {
	if c == nil || c.ClaudeChatModel == "" {
		return DefaultChatModel
	}
	return c.ClaudeChatModel
}

// EffectiveEvalModel returns the configured evaluation-operator
// model, or the built-in default if unset.
func (c *Credentials) EffectiveEvalModel() string {
	if c == nil || c.ClaudeEvalModel == "" {
		return DefaultEvalModel
	}
	return c.ClaudeEvalModel
}

// EffectiveOperatorModel returns the configured generic-operator
// model, or the built-in default if unset.
func (c *Credentials) EffectiveOperatorModel() string {
	if c == nil || c.ClaudeOperatorModel == "" {
		return DefaultOperatorModel
	}
	return c.ClaudeOperatorModel
}

// CredentialsPath returns the path to the credentials file.
func CredentialsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "config", "credentials.yaml")
}

// LoadCredentials reads ~/.clawflow/config/credentials.yaml and merges env vars.
// Priority: env > credentials.yaml
// Supported env vars: GH_TOKEN, GITLAB_TOKEN, CLAWFLOW_CLAUDE_API_KEY,
// CLAWFLOW_CLAUDE_BASE_URL (the CLAWFLOW_ prefix avoids conflict with
// a user-set ANTHROPIC_API_KEY meant for their interactive shell).
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
	c.ClaudeAPIKey = envOrFile("CLAWFLOW_CLAUDE_API_KEY", c.ClaudeAPIKey)
	c.ClaudeBaseURL = envOrFile("CLAWFLOW_CLAUDE_BASE_URL", c.ClaudeBaseURL)
	c.ClaudeChatModel = envOrFile("CLAWFLOW_CLAUDE_CHAT_MODEL", c.ClaudeChatModel)
	c.ClaudeEvalModel = envOrFile("CLAWFLOW_CLAUDE_EVAL_MODEL", c.ClaudeEvalModel)
	c.ClaudeOperatorModel = envOrFile("CLAWFLOW_CLAUDE_OPERATOR_MODEL", c.ClaudeOperatorModel)
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

// RepoSlug converts "owner/repo" to "owner-repo" for use in branch names and
// other contexts where a dash separator is conventional.
func RepoSlug(ownerRepo string) string {
	for i, c := range ownerRepo {
		if c == '/' {
			return ownerRepo[:i] + "-" + ownerRepo[i+1:]
		}
	}
	return ownerRepo
}

// RepoSlugFS converts "owner/repo" to "owner__repo" for use in filesystem
// paths. The double-underscore separator is unambiguous: a repo named
// "foo-bar" under owner "foo" won't collide with owner "foo-bar" + repo "baz".
func RepoSlugFS(ownerRepo string) string {
	return strings.ReplaceAll(ownerRepo, "/", "__")
}

// WorktreePath returns the standard worktree path for an issue.
// All worktrees live under ~/.clawflow/worktrees/<owner__repo>/issue-<N>
// so they are user-scoped, survive reboots, and match the path the
// automated runner (setupWorktree in run.go) uses — enabling
// clawflow worktree remove to clean up what the runner created.
func WorktreePath(ownerRepo string, issueNumber int) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "worktrees", RepoSlugFS(ownerRepo), fmt.Sprintf("issue-%d", issueNumber))
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
