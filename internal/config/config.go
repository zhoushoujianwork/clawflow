// Package config loads and parses ~/.clawflow/config/config.yaml.
package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

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

	// BoundMachine, when non-empty, restricts processing of this repo to the
	// machine whose hostname matches this value. Repos with no BoundMachine
	// (or whose BoundMachine matches the current hostname) are processed
	// normally. This allows multiple machines sharing a synced config to each
	// own a subset of repos without stepping on each other.
	//
	// Like local_path, this field is machine-specific and is intentionally
	// excluded from cloud sync (see syncableRepo in sync.go).
	BoundMachine string `yaml:"bound_machine,omitempty"`

	// UpdatedAt is the RFC3339 UTC timestamp of the last write to this repo
	// entry. Used by the LWW (Last-Write-Wins) merge strategy during Gist
	// sync: the side with the newer timestamp wins the whole entry. A zero
	// value means the entry predates LWW and is treated as "oldest possible"
	// during migration.
	UpdatedAt time.Time `yaml:"updated_at,omitempty"`

	// UpdatedBy is the hostname of the machine that last wrote this entry.
	// Informational only — the authoritative tiebreaker is UpdatedAt.
	UpdatedBy string `yaml:"updated_by,omitempty"`
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

	// DefaultIDE selects which IDE the dashboard's "Open in IDE" button
	// uses. Supported values:
	//   ""         / "vscode"          — VS Code (vscode://file/<path>)
	//   "cursor"                       — Cursor (cursor://file/<path>)
	//   "qoder"                        — Qoder (qoder://file/<path>)
	//   "vscode-insiders"              — VS Code Insiders
	// Empty/unset defaults to "vscode" for backward compatibility.
	DefaultIDE string `yaml:"default_ide,omitempty"`

	// BillingCycleDay is the day of month (1-28) when a billing period
	// starts. Usage is aggregated into monthly periods aligned to this
	// day. 0 or unset defaults to 1 (calendar month).
	BillingCycleDay int `yaml:"billing_cycle_day,omitempty"`

	// MaxConsecutiveFailures is the number of consecutive failed runs on
	// the same (repo, issue) before the runner auto-adds `agent-failed`
	// to stop retrying. 0 or unset defaults to 3.
	MaxConsecutiveFailures int `yaml:"max_consecutive_failures,omitempty"`

	// RequireBinding, when true, causes `clawflow run` to skip repos
	// whose BoundMachine is empty. This prevents newly synced repos from
	// being processed by all machines simultaneously — they must be
	// explicitly bound first. Synced via Gist as a shared fleet policy.
	RequireBinding bool `yaml:"require_binding,omitempty"`
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

// ResolveIDEScheme returns the URI scheme prefix for the configured IDE.
// The returned string is the full prefix up to and including "://file/",
// ready to be concatenated with an absolute local path.
// Unknown values fall back to the vscode scheme.
func (s *Settings) ResolveIDEScheme() string {
	switch s.DefaultIDE {
	case "cursor":
		return "cursor://file/"
	case "qoder":
		return "qoder://file/"
	case "vscode-insiders":
		return "vscode-insiders://file/"
	default: // "", "vscode", or any unrecognised value
		return "vscode://file/"
	}
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

// ClaudeProvider represents a single Claude API configuration entry.
// The order in the list defines priority (index 0 = highest).
type ClaudeProvider struct {
	Name     string `yaml:"name"`               // display name, e.g. "Anthropic Official"
	BaseURL  string `yaml:"base_url,omitempty"` // e.g. "https://api.anthropic.com"
	APIKey   string `yaml:"api_key,omitempty"`  // stored plaintext (like current behavior)
	Model    string `yaml:"model,omitempty"`    // optional override, empty = use global default
	Enabled  bool   `yaml:"enabled"`             // toggle for this provider
	Position int    `yaml:"-"`                   // runtime order (not persisted)
}

// Credentials holds sensitive config.
type Credentials struct {
	GHToken     string `yaml:"gh_token,omitempty"`
	GitLabToken string `yaml:"gitlab_token,omitempty"`

	// GistID is the ID of the user's private "clawflow-config" GitHub Gist,
	// discovered or created by `clawflow login`. Persisted here so subsequent
	// sync operations skip the search step. Never synced to the Gist itself.
	GistID string `yaml:"gist_id,omitempty"`

	// ClaudeAPIKey, when set, overrides whatever auth the user's
	// system claude would use (OAuth keychain, ~/.claude.json) by
	// passing ANTHROPIC_API_KEY to every claude subprocess clawflow
	// spawns. Useful for routing through a proxy / relay or pinning
	// to a specific account separate from the user's interactive
	// Claude Code login.
	// Deprecated: Use ClaudeProviders instead for multi-provider support.
	ClaudeAPIKey string `yaml:"claude_api_key,omitempty"`

	// ClaudeBaseURL, when set, is forwarded as ANTHROPIC_BASE_URL.
	// Typically paired with ClaudeAPIKey when targeting a relay.
	// Deprecated: Use ClaudeProviders instead for multi-provider support.
	ClaudeBaseURL string `yaml:"claude_base_url,omitempty"`

	// ClaudeProviders is the ordered list of Claude API configurations.
	// The first enabled provider is used; on failure, the runner failover
	// to the next enabled one. Empty list + legacy fields triggers migration.
	ClaudeProviders []ClaudeProvider `yaml:"claude_providers,omitempty"`

	// FailoverPatterns are regex patterns (as strings) that trigger
	// automatic failover to the next provider. User patterns are merged
	// with (not replace) the defaults.
	FailoverPatterns []string `yaml:"failover_patterns,omitempty"`

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

	// LastSyncedAt is an RFC3339 timestamp recording when the config was
	// last successfully pushed or pulled via the sync API. It is stored in
	// credentials.yaml (alongside GistID) because it is machine-specific
	// metadata, not part of the synced config payload itself.
	LastSyncedAt string `yaml:"last_synced_at,omitempty"`

	// LastPulledAt is the RFC3339 UTC timestamp of the last successful
	// auto-pull or manual pull. Used by the manual-edit detection logic:
	// if config.yaml's mtime is newer than LastPulledAt, the user edited
	// the file directly and we should push instead of pull.
	LastPulledAt string `yaml:"last_pulled_at,omitempty"`

	// ChatDefaultMode controls the default mode for issue-level chat sessions.
	// Valid values: "issue" (default) — disallows Edit/Write/NotebookEdit and
	// prompts the AI to land conclusions in the issue tracker; "edit" — allows
	// full file editing (the historical behaviour). The --mode flag on
	// `clawflow chat` overrides this per-session.
	ChatDefaultMode string `yaml:"chat_default_mode,omitempty"`
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

// EffectiveChatDefaultMode returns the configured default chat mode for
// issue-level sessions. Valid values are "issue" and "edit"; anything
// else (including empty) falls back to "issue" — the safer default that
// prevents accidental file edits.
func (c *Credentials) EffectiveChatDefaultMode() string {
	if c != nil && c.ChatDefaultMode == "edit" {
		return "edit"
	}
	return "issue"
}

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

// MigrateLegacyProvider migrates the legacy single-provider fields
// (ClaudeAPIKey + ClaudeBaseURL) into ClaudeProviders[0] when the list
// is empty. This is idempotent: if ClaudeProviders is already populated
// the function is a no-op. Returns true when a migration was performed
// so callers can persist the updated credentials.
func MigrateLegacyProvider(c *Credentials) bool {
	if len(c.ClaudeProviders) > 0 {
		return false // already migrated
	}
	if c.ClaudeAPIKey == "" && c.ClaudeBaseURL == "" {
		return false // nothing to migrate
	}
	name := "Default"
	if c.ClaudeBaseURL != "" {
		name = "Legacy provider"
	}
	c.ClaudeProviders = []ClaudeProvider{
		{
			Name:    name,
			BaseURL: c.ClaudeBaseURL,
			APIKey:  c.ClaudeAPIKey,
			Enabled: true,
		},
	}
	return true
}

// EnabledProviders returns the subset of ClaudeProviders where Enabled == true,
// in list order (index 0 = highest priority). The returned slice is a copy.
func (c *Credentials) EnabledProviders() []ClaudeProvider {
	if c == nil {
		return nil
	}
	var out []ClaudeProvider
	for _, p := range c.ClaudeProviders {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out
}

// DefaultFailoverPatterns are the built-in substrings (case-insensitive)
// that identify a provider as temporarily unavailable and trigger failover
// to the next enabled provider. User-supplied FailoverPatterns are merged
// with (not replace) this list.
var DefaultFailoverPatterns = []string{
	"hit your limit",
	"you've hit your limit",
	"usage limit reached",
	"rate_limit_error",
	"rate limit",
	"429",
	"quota exceeded",
	"credit balance is too low",
	"overloaded_error",
	"401",
	"invalid api key",
	"invalid_api_key",
	"connection refused",
	"dial tcp",
	"i/o timeout",
	"tls handshake",
	"http 5",
	"status 5",
}

// EffectiveFailoverPatterns returns the merged set of default + user-supplied
// failover patterns. Duplicates are not deduplicated (harmless for substring
// matching).
func (c *Credentials) EffectiveFailoverPatterns() []string {
	patterns := make([]string, len(DefaultFailoverPatterns))
	copy(patterns, DefaultFailoverPatterns)
	if c != nil {
		patterns = append(patterns, c.FailoverPatterns...)
	}
	return patterns
}

// LoadCredentials reads ~/.clawflow/config/credentials.yaml and merges env vars.
// Priority: env > credentials.yaml
// Supported env vars: GH_TOKEN, GITLAB_TOKEN, CLAWFLOW_CLAUDE_API_KEY,
// CLAWFLOW_CLAUDE_BASE_URL (the CLAWFLOW_ prefix avoids conflict with
// a user-set ANTHROPIC_API_KEY meant for their interactive shell).
//
// On load, if ClaudeProviders is empty but legacy single-provider fields are
// set, they are automatically migrated into ClaudeProviders[0] and persisted.
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

	// Auto-migrate legacy single-provider fields to the providers list.
	// Best-effort: a save failure is non-fatal — the migration will be
	// retried on the next load.
	if MigrateLegacyProvider(c) {
		_ = SaveCredentials(c)
	}

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

// TouchRepo runs mutFn against the named repo entry, stamps UpdatedAt = now
// and UpdatedBy = hostname, then stores the result back into cfg.Repos.
// It does NOT save the config to disk — call cfg.Save() after.
//
// All code paths that mutate a repo entry should go through TouchRepo so
// the LWW timestamp is always current. If hostname resolution fails the
// field is left empty (non-fatal).
func TouchRepo(cfg *Config, name string, mutFn func(r Repo) Repo) {
	r := cfg.Repos[name]
	r = mutFn(r)
	r.UpdatedAt = time.Now().UTC()
	if h, err := os.Hostname(); err == nil {
		r.UpdatedBy = h
	}
	cfg.Repos[name] = r
}

// ConflictPath returns the path to the conflict artifact file.
func ConflictPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "config", "config.conflict.yaml")
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
