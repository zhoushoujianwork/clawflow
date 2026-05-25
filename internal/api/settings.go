// Package api: settings endpoints back the dashboard's /settings
// page. They are the only writers for credentials.yaml and the
// settings: section of config.yaml outside the CLI. Keeping the
// authoritative source as YAML — the API just edits the same files
// the CLI does, then refreshes the dashboard's data/*.json snapshot.
package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// settingsView is what GET /api/settings returns. Sensitive fields
// (tokens) are NEVER returned in full — the response carries only
// `*_set` booleans and a 4-character hint. Editing requires the user
// to type the new value; this prevents accidental disclosure via
// screenshots, browser history, or extension snooping.
//
// Claude model configuration lives on each provider now — see
// /api/providers — so this view only covers VCS tokens and
// non-credential global settings.
type settingsView struct {
	Tokens struct {
		GHSet      bool   `json:"gh_set"`
		GHHint     string `json:"gh_hint,omitempty"`
		GitlabSet  bool   `json:"gitlab_set"`
		GitlabHint string `json:"gitlab_hint,omitempty"`
	} `json:"tokens"`
	Global struct {
		PollInterval        int    `json:"poll_interval"`
		ConfidenceThreshold int    `json:"confidence_threshold"`
		AgentTimeout        int    `json:"agent_timeout"`
		MaxConcurrentAgents int    `json:"max_concurrent_agents"`
		RunIntervalMinutes  int    `json:"run_interval_minutes"`
		GithubCloneDir      string `json:"github_clone_dir,omitempty"`
		GitlabCloneDir      string `json:"gitlab_clone_dir,omitempty"`
		GitLabURL           string `json:"gitlab_url,omitempty"`
		Terminal            string `json:"terminal,omitempty"`
		DefaultIDE          string `json:"default_ide,omitempty"`
		RequireBinding      bool   `json:"require_binding"`
		// Hostname is this machine's identifier — the same string the
		// /api/repo/bind endpoint writes into bound_machine. Exposed so
		// the dashboard can decide whether a given repo is "mine"
		// without round-tripping through the bind endpoint.
		Hostname string `json:"hostname,omitempty"`
		// Language is the preferred AI output language. Supported values:
		// "" (auto), "zh" (Simplified Chinese), "en" (English).
		Language string `json:"language,omitempty"`
	} `json:"global"`
}

// HandleGetSettings serves GET /api/settings with masked credentials.
func HandleGetSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cfg, err := config.Load()
	if err != nil {
		writeErr(w, err)
		return
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		writeErr(w, err)
		return
	}

	var v settingsView
	v.Tokens.GHSet = creds.GHToken != ""
	v.Tokens.GHHint = lastFour(creds.GHToken)
	v.Tokens.GitlabSet = creds.GitLabToken != ""
	v.Tokens.GitlabHint = lastFour(creds.GitLabToken)
	v.Global.PollInterval = cfg.Settings.PollInterval
	if v.Global.PollInterval <= 0 {
		v.Global.PollInterval = 30
	}
	v.Global.ConfidenceThreshold = cfg.Settings.ConfidenceThreshold
	v.Global.AgentTimeout = cfg.Settings.AgentTimeout
	if v.Global.AgentTimeout <= 0 {
		v.Global.AgentTimeout = 3600
	}
	v.Global.MaxConcurrentAgents = cfg.Settings.MaxConcurrentAgents
	if v.Global.MaxConcurrentAgents <= 0 {
		v.Global.MaxConcurrentAgents = 4
	}
	v.Global.RunIntervalMinutes = cfg.Settings.RunIntervalMinutes
	v.Global.GithubCloneDir = cfg.Settings.GithubCloneDir
	v.Global.GitlabCloneDir = cfg.Settings.GitlabCloneDir
	if len(cfg.Settings.GitLabHosts) > 0 {
		v.Global.GitLabURL = cfg.Settings.GitLabHosts[0]
	}
	v.Global.Terminal = cfg.Settings.Terminal
	v.Global.DefaultIDE = cfg.Settings.DefaultIDE
	v.Global.RequireBinding = cfg.Settings.RequireBinding
	v.Global.Language = cfg.Settings.Language
	if h, hErr := os.Hostname(); hErr == nil {
		v.Global.Hostname = h
	}

	writeJSON(w, 200, v)
}

// Claude model and API-key configuration is per-provider now — see
// /api/providers (and its POST/PUT handlers). The old
// /api/settings/claude global endpoint is gone; the dashboard edits
// each provider's chat_model / eval_model / operator_model slot
// directly.

// revealRequest names which secret the caller wants in plaintext.
// Single-secret resolution (rather than dumping all three at once)
// keeps the surface tight: the network tab only ever logs the value
// the user explicitly clicked the eye on. Always POST — never GET —
// so the secret never lands in URL bars or shell history.
type revealRequest struct {
	Which string `json:"which"` // "claude_api_key" | "gh_token" | "gitlab_token"
}

// HandleRevealSecret handles POST /api/settings/reveal. Returns the
// raw saved value for the requested credential. The dashboard is
// localhost-only and the user just clicked the eye icon, so the
// disclosure is intentional. Unknown `which` returns 400.
func HandleRevealSecret(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req revealRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		writeErr(w, err)
		return
	}
	var v string
	switch req.Which {
	case "claude_api_key":
		v = creds.ClaudeAPIKey
	case "gh_token":
		v = creds.GHToken
	case "gitlab_token":
		v = creds.GitLabToken
	default:
		writeJSON(w, 400, map[string]string{"error": "unknown secret: " + req.Which})
		return
	}
	writeJSON(w, 200, map[string]string{"value": v})
}

type tokensUpdate struct {
	GHToken     *string `json:"gh_token,omitempty"`
	GitlabToken *string `json:"gitlab_token,omitempty"`
}

// HandleUpdateTokens handles POST /api/settings/tokens.
func HandleUpdateTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req tokensUpdate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		writeErr(w, err)
		return
	}
	if req.GHToken != nil {
		creds.GHToken = *req.GHToken
	}
	if req.GitlabToken != nil {
		creds.GitLabToken = *req.GitlabToken
	}
	if err := config.SaveCredentials(creds); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

type globalUpdate struct {
	PollInterval        *int    `json:"poll_interval,omitempty"`
	ConfidenceThreshold *int    `json:"confidence_threshold,omitempty"`
	AgentTimeout        *int    `json:"agent_timeout,omitempty"`
	MaxConcurrentAgents *int    `json:"max_concurrent_agents,omitempty"`
	RunIntervalMinutes  *int    `json:"run_interval_minutes,omitempty"`
	GithubCloneDir      *string `json:"github_clone_dir,omitempty"`
	GitlabCloneDir      *string `json:"gitlab_clone_dir,omitempty"`
	GitLabURL           *string `json:"gitlab_url,omitempty"`
	Terminal            *string `json:"terminal,omitempty"`
	DefaultIDE          *string `json:"default_ide,omitempty"`
	RequireBinding      *bool   `json:"require_binding,omitempty"`
	Language            *string `json:"language,omitempty"`
}

// HandleUpdateGlobalSettings handles POST /api/settings/global.
// Updated values flow back to data/repos.json so any field the
// dashboard mirrors stays in sync without a `clawflow run` cycle.
func HandleUpdateGlobalSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req globalUpdate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	cfg, err := config.Load()
	if err != nil {
		writeErr(w, err)
		return
	}
	if req.PollInterval != nil {
		cfg.Settings.PollInterval = *req.PollInterval
	}
	if req.ConfidenceThreshold != nil {
		cfg.Settings.ConfidenceThreshold = *req.ConfidenceThreshold
	}
	if req.AgentTimeout != nil {
		cfg.Settings.AgentTimeout = *req.AgentTimeout
	}
	if req.MaxConcurrentAgents != nil {
		cfg.Settings.MaxConcurrentAgents = *req.MaxConcurrentAgents
	}
	if req.RunIntervalMinutes != nil {
		// Clamp negative to 0 (= disabled). Any positive int passes
		// through; the scheduler re-reads on its next 30s check.
		v := *req.RunIntervalMinutes
		if v < 0 {
			v = 0
		}
		cfg.Settings.RunIntervalMinutes = v
	}
	if req.GithubCloneDir != nil {
		cfg.Settings.GithubCloneDir = *req.GithubCloneDir
	}
	if req.GitlabCloneDir != nil {
		cfg.Settings.GitlabCloneDir = *req.GitlabCloneDir
	}
	if req.GitLabURL != nil {
		url := strings.TrimSpace(*req.GitLabURL)
		if url == "" {
			cfg.Settings.GitLabHosts = nil
		} else {
			cfg.Settings.GitLabHosts = []string{url}
		}
	}
	if req.Terminal != nil {
		cfg.Settings.Terminal = strings.TrimSpace(*req.Terminal)
	}
	if req.DefaultIDE != nil {
		cfg.Settings.DefaultIDE = strings.TrimSpace(*req.DefaultIDE)
	}
	if req.RequireBinding != nil {
		cfg.Settings.RequireBinding = *req.RequireBinding
	}
	if req.Language != nil {
		lang := strings.TrimSpace(*req.Language)
		// Validate: only "", "zh", "en" are accepted.
		switch lang {
		case "", "zh", "en":
			cfg.Settings.Language = lang
		default:
			writeJSON(w, 400, map[string]string{"error": "language must be '' (auto), 'zh', or 'en'"})
			return
		}
	}
	if err := cfg.Save(); err != nil {
		writeErr(w, err)
		return
	}
	_ = snapshot.WriteRepos(cfg)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// testClaudeTimeout caps how long a connectivity probe waits for
// claude to reply. Anthropic's direct API answers a haiku ping in
// 2-4 s, but corporate proxies (cc-proxy, internal LLM gateways)
// frequently add 10-20 s on first token — the previous 8 s budget
// flagged perfectly working setups as "timed out". 30 s leaves room
// for cold-start TLS + first-token latency on a slow relay while
// still failing fast on a misconfigured base URL. Used by the
// per-provider test endpoint in providers.go.
const testClaudeTimeout = 30 * time.Second

// truncateMsg trims trailing whitespace and caps len at n, appending
// an ellipsis when truncated. Empty input returns empty string.
func truncateMsg(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// lastFour returns "…XYZW" — the last 4 chars of s — for redacted
// display in the UI. Empty input returns empty string.
func lastFour(s string) string {
	if len(s) <= 4 {
		return s
	}
	return "…" + s[len(s)-4:]
}

