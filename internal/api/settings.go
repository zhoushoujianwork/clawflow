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
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
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
		// Three model-override slots; empty means "fall back to the
		// built-in default exposed via the corresponding *_default
		// field". Models aren't sensitive so they're returned in
		// plaintext.
		ChatModel        string `json:"chat_model"`
		EvalModel        string `json:"eval_model"`
		OperatorModel    string `json:"operator_model"`
		ChatModelDefault string `json:"chat_model_default"`
		EvalModelDefault string `json:"eval_model_default"`
		OperatorModelDef string `json:"operator_model_default"`
	} `json:"claude"`
	Tokens struct {
		GHSet      bool   `json:"gh_set"`
		GHHint     string `json:"gh_hint,omitempty"`
		GitlabSet  bool   `json:"gitlab_set"`
		GitlabHint string `json:"gitlab_hint,omitempty"`
	} `json:"tokens"`
	Global struct {
		PollInterval        int      `json:"poll_interval"`
		ConfidenceThreshold int      `json:"confidence_threshold"`
		AgentTimeout        int      `json:"agent_timeout"`
		MaxConcurrentAgents int      `json:"max_concurrent_agents"`
		RunIntervalMinutes  int      `json:"run_interval_minutes"`
		GithubCloneDir      string   `json:"github_clone_dir,omitempty"`
		GitlabCloneDir      string   `json:"gitlab_clone_dir,omitempty"`
		GitLabURL           string   `json:"gitlab_url,omitempty"`
		Terminal            string   `json:"terminal,omitempty"`
		DefaultIDE          string   `json:"default_ide,omitempty"`
		RequireBinding      bool     `json:"require_binding"`
		// Hostname is this machine's identifier — the same string the
		// /api/repo/bind endpoint writes into bound_machine. Exposed so
		// the dashboard can decide whether a given repo is "mine"
		// without round-tripping through the bind endpoint.
		Hostname            string   `json:"hostname,omitempty"`
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
	v.Claude.ChatModel = creds.ClaudeChatModel
	v.Claude.EvalModel = creds.ClaudeEvalModel
	v.Claude.OperatorModel = creds.ClaudeOperatorModel
	v.Claude.ChatModelDefault = config.DefaultChatModel
	v.Claude.EvalModelDefault = config.DefaultEvalModel
	v.Claude.OperatorModelDef = config.DefaultOperatorModel
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
	if h, hErr := os.Hostname(); hErr == nil {
		v.Global.Hostname = h
	}

	writeJSON(w, 200, v)
}

// claudeUpdate uses *string for tristate semantics:
//   - field omitted from JSON → ptr is nil → don't change
//   - field set to ""        → ptr is &"" → clear
//   - field set to non-empty → update to that value
type claudeUpdate struct {
	APIKey        *string `json:"api_key,omitempty"`
	BaseURL       *string `json:"base_url,omitempty"`
	ChatModel     *string `json:"chat_model,omitempty"`
	EvalModel     *string `json:"eval_model,omitempty"`
	OperatorModel *string `json:"operator_model,omitempty"`
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
	if req.ChatModel != nil {
		creds.ClaudeChatModel = *req.ChatModel
	}
	if req.EvalModel != nil {
		creds.ClaudeEvalModel = *req.EvalModel
	}
	if req.OperatorModel != nil {
		creds.ClaudeOperatorModel = *req.OperatorModel
	}
	if err := config.SaveCredentials(creds); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

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

// testClaudeTimeout caps how long the connectivity probe waits for
// claude to reply. Anthropic's direct API answers a haiku ping in
// 2-4 s, but corporate proxies (cc-proxy, internal LLM gateways)
// frequently add 10-20 s on first token — the previous 8 s budget
// flagged perfectly working setups as "timed out". 30 s leaves room
// for cold-start TLS + first-token latency on a slow relay while
// still failing fast on a misconfigured base URL.
const testClaudeTimeout = 30 * time.Second

// HandleTestClaude handles POST /api/settings/claude/test. Spawns a
// short `claude -p "ping"` with the supplied credentials and reports
// whether it responded within testClaudeTimeout. Always tests against
// the chat model default (haiku) — a connectivity probe shouldn't
// depend on whichever heavier model the user pinned for evaluations.
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

	// Trace every probe to the server log so the operator can see what
	// got tried and why. Mask the key down to the same hint format the
	// UI uses (last 4 chars) — anything more risks logging a token
	// into a tail-of-stderr dump.
	keyHint := "(empty)"
	if n := len(apiKey); n >= 4 {
		keyHint = "…" + apiKey[n-4:]
	}
	urlForLog := baseURL
	if urlForLog == "" {
		urlForLog = "(default — api.anthropic.com)"
	}
	log.Printf("test-claude: start model=%s base_url=%s key_hint=%s timeout=%s",
		config.DefaultChatModel, urlForLog, keyHint, testClaudeTimeout)
	startedAt := time.Now()

	ctx, cancel := context.WithTimeout(r.Context(), testClaudeTimeout)
	defer cancel()

	// --bare so the probe actually exercises the API key path. Without
	// it, claude defers to keychain/OAuth when both auth sources are
	// present, the request goes to api.anthropic.com under the user's
	// claude.ai login, and "Test connection" returns OK while the
	// configured proxy URL was never touched. Mirrors what `clawflow
	// chat` and operator runs do when an API key is configured.
	probeArgs := []string{
		"-p",
		"--bare",
		"--model", config.DefaultChatModel,
		"--output-format", "text",
		"say PONG",
	}
	cmd := exec.CommandContext(ctx, claude.Resolve(), probeArgs...)
	cmd.Env = claude.EnvWithCredentials(os.Environ(), apiKey, baseURL)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Claude routes auth/model errors to stdout in text-output
		// mode while reserving stderr for hard crashes — surface
		// whichever is non-empty so the UI sees the real cause
		// instead of just "exit status 1". Both are returned so a
		// caller wanting full detail still has it.
		stderrMsg := truncateMsg(stderr.String(), 800)
		stdoutMsg := truncateMsg(stdout.String(), 800)
		detail := stderrMsg
		if detail == "" {
			detail = stdoutMsg
		}
		// Build the human-facing error: "<detail> (exit status 1)"
		// so the UI badge actually carries information. Falls back
		// to the raw exit message when both pipes were silent.
		exitMsg := err.Error()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			exitMsg = fmt.Sprintf("timed out after %s — proxy/relay slow or unreachable", testClaudeTimeout)
			detail = ""
		}
		humanErr := exitMsg
		if detail != "" {
			humanErr = detail + " (" + exitMsg + ")"
		}
		log.Printf("test-claude: FAIL after %s — %s | stderr=%q stdout=%q",
			time.Since(startedAt).Round(time.Millisecond), humanErr,
			truncateMsg(stderr.String(), 200), truncateMsg(stdout.String(), 200))
		writeJSON(w, 200, map[string]any{
			"status": "error",
			"error":  humanErr,
			"stderr": stderrMsg,
			"stdout": stdoutMsg,
		})
		return
	}

	out := truncateMsg(stdout.String(), 400)
	log.Printf("test-claude: OK after %s — reply=%q",
		time.Since(startedAt).Round(time.Millisecond), out)
	writeJSON(w, 200, map[string]any{
		"status": "ok",
		"reply":  out,
	})
}

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

