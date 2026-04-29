// Package api: settings endpoints back the dashboard's /settings
// page. They are the only writers for credentials.yaml and the
// settings: section of config.yaml outside the CLI. Keeping the
// authoritative source as YAML — the API just edits the same files
// the CLI does, then refreshes the dashboard's data/*.json snapshot.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/claude"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// settingsView is what GET /api/settings returns. Sensitive fields
// (API key, tokens) are NEVER returned in full — the response carries
// only `*_set` booleans and a 4-character hint. Editing requires the
// user to type the new value; this prevents accidental disclosure
// via screenshots, browser history, or extension snooping.
type settingsView struct {
	Claude struct {
		APIKeySet  bool   `json:"api_key_set"`
		APIKeyHint string `json:"api_key_hint,omitempty"`
		BaseURL    string `json:"base_url,omitempty"`
	} `json:"claude"`
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
		GithubCloneDir      string `json:"github_clone_dir,omitempty"`
		GitlabCloneDir      string `json:"gitlab_clone_dir,omitempty"`
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
	v.Claude.APIKeySet = creds.ClaudeAPIKey != ""
	v.Claude.APIKeyHint = lastFour(creds.ClaudeAPIKey)
	v.Claude.BaseURL = creds.ClaudeBaseURL
	v.Tokens.GHSet = creds.GHToken != ""
	v.Tokens.GHHint = lastFour(creds.GHToken)
	v.Tokens.GitlabSet = creds.GitLabToken != ""
	v.Tokens.GitlabHint = lastFour(creds.GitLabToken)
	v.Global.PollInterval = cfg.Settings.PollInterval
	v.Global.ConfidenceThreshold = cfg.Settings.ConfidenceThreshold
	v.Global.AgentTimeout = cfg.Settings.AgentTimeout
	v.Global.MaxConcurrentAgents = cfg.Settings.MaxConcurrentAgents
	v.Global.GithubCloneDir = cfg.Settings.GithubCloneDir
	v.Global.GitlabCloneDir = cfg.Settings.GitlabCloneDir

	writeJSON(w, 200, v)
}

// claudeUpdate uses *string for tristate semantics:
//   - field omitted from JSON → ptr is nil → don't change
//   - field set to ""        → ptr is &"" → clear
//   - field set to non-empty → update to that value
type claudeUpdate struct {
	APIKey  *string `json:"api_key,omitempty"`
	BaseURL *string `json:"base_url,omitempty"`
}

// HandleUpdateClaudeSettings handles POST /api/settings/claude.
func HandleUpdateClaudeSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req claudeUpdate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		writeErr(w, err)
		return
	}
	if req.APIKey != nil {
		creds.ClaudeAPIKey = *req.APIKey
	}
	if req.BaseURL != nil {
		creds.ClaudeBaseURL = *req.BaseURL
	}
	if err := config.SaveCredentials(creds); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
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
	GithubCloneDir      *string `json:"github_clone_dir,omitempty"`
	GitlabCloneDir      *string `json:"gitlab_clone_dir,omitempty"`
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
	if req.GithubCloneDir != nil {
		cfg.Settings.GithubCloneDir = *req.GithubCloneDir
	}
	if req.GitlabCloneDir != nil {
		cfg.Settings.GitlabCloneDir = *req.GitlabCloneDir
	}
	if err := cfg.Save(); err != nil {
		writeErr(w, err)
		return
	}
	_ = snapshot.WriteRepos(cfg)
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// claudeTestRequest carries the credentials to validate. Empty
// fields fall back to the saved values, so a user can test "what's
// already configured" by POSTing {} — useful as a status probe.
type claudeTestRequest struct {
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
}

// HandleTestClaude handles POST /api/settings/claude/test. Spawns a
// short `claude -p "ping"` with the supplied credentials and reports
// whether it responded within 8 s.
func HandleTestClaude(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req claudeTestRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // empty body is OK

	apiKey, baseURL := req.APIKey, req.BaseURL
	if apiKey == "" || baseURL == "" {
		creds, _ := config.LoadCredentials()
		if creds != nil {
			if apiKey == "" {
				apiKey = creds.ClaudeAPIKey
			}
			if baseURL == "" {
				baseURL = creds.ClaudeBaseURL
			}
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx,
		claude.Resolve(),
		"-p",
		"--model", "haiku",
		"--output-format", "text",
		"say PONG",
	)
	cmd.Env = claude.EnvWithCredentials(os.Environ(), apiKey, baseURL)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Trim stderr to keep the response readable.
		msg := stderr.String()
		const cap = 800
		if len(msg) > cap {
			msg = msg[:cap] + "…"
		}
		// Differentiate timeout from other failures.
		exitMsg := err.Error()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			exitMsg = "timed out after 8s"
		}
		writeJSON(w, 200, map[string]any{
			"status": "error",
			"error":  exitMsg,
			"stderr": msg,
		})
		return
	}

	out := stdout.String()
	if len(out) > 400 {
		out = out[:400] + "…"
	}
	writeJSON(w, 200, map[string]any{
		"status": "ok",
		"reply":  out,
	})
}

// lastFour returns "…XYZW" — the last 4 chars of s — for redacted
// display in the UI. Empty input returns empty string.
func lastFour(s string) string {
	if len(s) <= 4 {
		return s
	}
	return "…" + s[len(s)-4:]
}

