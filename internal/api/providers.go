package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/claude"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// providerView is the safe representation of a ClaudeProvider for the API.
// The api_key is never returned in full — only a masked hint.
//
// Each role (chat / eval / operator) has its own stored value plus the
// built-in default the runner will fall back to when the stored value
// is empty. Exposing the default lets the dashboard render it as a
// placeholder without having to duplicate the constants.
type providerView struct {
	Name                 string `json:"name"`
	BaseURL              string `json:"base_url,omitempty"`
	APIKeySet            bool   `json:"api_key_set"`
	APIKeyHint           string `json:"api_key_hint,omitempty"`
	ChatModel            string `json:"chat_model"`
	EvalModel            string `json:"eval_model"`
	OperatorModel        string `json:"operator_model"`
	ChatModelDefault     string `json:"chat_model_default"`
	EvalModelDefault     string `json:"eval_model_default"`
	OperatorModelDefault string `json:"operator_model_default"`
	Enabled              bool   `json:"enabled"`
	Index                int    `json:"index"`
}

func toProviderView(p config.ClaudeProvider, idx int) providerView {
	return providerView{
		Name:                 p.Name,
		BaseURL:              p.BaseURL,
		APIKeySet:            p.APIKey != "",
		APIKeyHint:           lastFour(p.APIKey),
		ChatModel:            p.ChatModel,
		EvalModel:            p.EvalModel,
		OperatorModel:        p.OperatorModel,
		ChatModelDefault:     config.DefaultChatModel,
		EvalModelDefault:     config.DefaultEvalModel,
		OperatorModelDefault: config.DefaultOperatorModel,
		Enabled:              p.Enabled,
		Index:                idx,
	}
}

// HandleListProviders handles GET /api/providers.
func HandleListProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		writeErr(w, err)
		return
	}
	views := make([]providerView, len(creds.ClaudeProviders))
	for i, p := range creds.ClaudeProviders {
		views[i] = toProviderView(p, i)
	}
	writeJSON(w, 200, views)
}

// providerAddRequest is the body for POST /api/providers.
type providerAddRequest struct {
	Name          string `json:"name"`
	BaseURL       string `json:"base_url,omitempty"`
	APIKey        string `json:"api_key,omitempty"`
	ChatModel     string `json:"chat_model,omitempty"`
	EvalModel     string `json:"eval_model,omitempty"`
	OperatorModel string `json:"operator_model,omitempty"`
	Enabled       *bool  `json:"enabled,omitempty"`
}

// HandleAddProvider handles POST /api/providers.
func HandleAddProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req providerAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeJSON(w, 400, map[string]string{"error": "name is required"})
		return
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		writeErr(w, err)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	p := config.ClaudeProvider{
		Name:          strings.TrimSpace(req.Name),
		BaseURL:       strings.TrimSpace(req.BaseURL),
		APIKey:        req.APIKey,
		ChatModel:     strings.TrimSpace(req.ChatModel),
		EvalModel:     strings.TrimSpace(req.EvalModel),
		OperatorModel: strings.TrimSpace(req.OperatorModel),
		Enabled:       enabled,
	}
	creds.ClaudeProviders = append(creds.ClaudeProviders, p)
	if err := config.SaveCredentials(creds); err != nil {
		writeErr(w, err)
		return
	}
	idx := len(creds.ClaudeProviders) - 1
	writeJSON(w, 200, toProviderView(p, idx))
}

// providerUpdateRequest is the body for PUT /api/providers/{index}.
// All fields are optional pointers — omitted = keep existing.
type providerUpdateRequest struct {
	Name          *string `json:"name,omitempty"`
	BaseURL       *string `json:"base_url,omitempty"`
	APIKey        *string `json:"api_key,omitempty"`
	ChatModel     *string `json:"chat_model,omitempty"`
	EvalModel     *string `json:"eval_model,omitempty"`
	OperatorModel *string `json:"operator_model,omitempty"`
	Enabled       *bool   `json:"enabled,omitempty"`
}

// HandleUpdateProvider handles PUT /api/providers/{index}.
func HandleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	idx, ok := parseProviderIndex(w, r)
	if !ok {
		return
	}
	var req providerUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		writeErr(w, err)
		return
	}
	if idx >= len(creds.ClaudeProviders) {
		writeJSON(w, 404, map[string]string{"error": "provider not found"})
		return
	}
	p := creds.ClaudeProviders[idx]
	if req.Name != nil {
		p.Name = strings.TrimSpace(*req.Name)
	}
	if req.BaseURL != nil {
		p.BaseURL = strings.TrimSpace(*req.BaseURL)
	}
	if req.APIKey != nil {
		p.APIKey = *req.APIKey
	}
	if req.ChatModel != nil {
		p.ChatModel = strings.TrimSpace(*req.ChatModel)
	}
	if req.EvalModel != nil {
		p.EvalModel = strings.TrimSpace(*req.EvalModel)
	}
	if req.OperatorModel != nil {
		p.OperatorModel = strings.TrimSpace(*req.OperatorModel)
	}
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	creds.ClaudeProviders[idx] = p
	if err := config.SaveCredentials(creds); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, toProviderView(p, idx))
}

// HandleDeleteProvider handles DELETE /api/providers/{index}.
func HandleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	idx, ok := parseProviderIndex(w, r)
	if !ok {
		return
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		writeErr(w, err)
		return
	}
	if idx >= len(creds.ClaudeProviders) {
		writeJSON(w, 404, map[string]string{"error": "provider not found"})
		return
	}
	creds.ClaudeProviders = append(creds.ClaudeProviders[:idx], creds.ClaudeProviders[idx+1:]...)
	if err := config.SaveCredentials(creds); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// providerReorderRequest is the body for PUT /api/providers/reorder.
// Names is the desired order of provider names; providers not in the list
// are appended at the end in their original relative order.
type providerReorderRequest struct {
	Order []string `json:"order"` // provider names in desired priority order
}

// HandleReorderProviders handles PUT /api/providers/reorder.
// Accepts the full ordered list of provider names and reorders the stored
// list to match. This is the single-write endpoint for drag-drop reordering.
func HandleReorderProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req providerReorderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		writeErr(w, err)
		return
	}

	// Build a name→provider map for O(1) lookup.
	byName := make(map[string]config.ClaudeProvider, len(creds.ClaudeProviders))
	for _, p := range creds.ClaudeProviders {
		byName[p.Name] = p
	}

	// Reorder: first the names in req.Order, then any remaining providers
	// not mentioned (preserving their relative order).
	seen := make(map[string]bool, len(req.Order))
	reordered := make([]config.ClaudeProvider, 0, len(creds.ClaudeProviders))
	for _, name := range req.Order {
		if p, ok := byName[name]; ok {
			reordered = append(reordered, p)
			seen[name] = true
		}
	}
	for _, p := range creds.ClaudeProviders {
		if !seen[p.Name] {
			reordered = append(reordered, p)
		}
	}
	creds.ClaudeProviders = reordered
	if err := config.SaveCredentials(creds); err != nil {
		writeErr(w, err)
		return
	}
	views := make([]providerView, len(creds.ClaudeProviders))
	for i, p := range creds.ClaudeProviders {
		views[i] = toProviderView(p, i)
	}
	writeJSON(w, 200, views)
}

// HandleRevealProviderKey handles POST /api/providers/{index}/reveal.
// Returns the raw api_key for the given provider index.
func HandleRevealProviderKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	idx, ok := parseProviderIndex(w, r)
	if !ok {
		return
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		writeErr(w, err)
		return
	}
	if idx >= len(creds.ClaudeProviders) {
		writeJSON(w, 404, map[string]string{"error": "provider not found"})
		return
	}
	writeJSON(w, 200, map[string]string{"value": creds.ClaudeProviders[idx].APIKey})
}

// HandleTestProvider handles POST /api/providers/{index}/test.
// Runs a lightweight connectivity probe against the provider using the
// stored credentials. Never invokes a full operator run.
func HandleTestProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	idx, ok := parseProviderIndex(w, r)
	if !ok {
		return
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		writeErr(w, err)
		return
	}
	if idx >= len(creds.ClaudeProviders) {
		writeJSON(w, 404, map[string]string{"error": "provider not found"})
		return
	}
	p := creds.ClaudeProviders[idx]

	keyHint := "(empty)"
	if n := len(p.APIKey); n >= 4 {
		keyHint = "…" + p.APIKey[n-4:]
	}
	urlForLog := p.BaseURL
	if urlForLog == "" {
		urlForLog = "(default — api.anthropic.com)"
	}
	log.Printf("test-provider[%d]: start name=%q base_url=%s key_hint=%s", idx, p.Name, urlForLog, keyHint)
	startedAt := time.Now()

	ctx, cancel := context.WithTimeout(r.Context(), testClaudeTimeout)
	defer cancel()

	probeArgs := []string{"-p"}
	// Only force --bare when an explicit API key is configured. Empty key
	// means the entry is the built-in OAuth fallback (seeded default) or a
	// user-defined keychain-based entry — in both cases --bare would skip
	// the claude CLI's keychain lookup and falsely fail the probe.
	if p.APIKey != "" {
		probeArgs = append(probeArgs, "--bare")
	}
	probeArgs = append(probeArgs,
		"--model", p.EffectiveChatModel(),
		"--output-format", "text",
		"say PONG",
	)
	cmd := exec.CommandContext(ctx, claude.Resolve(), probeArgs...)
	cmd.Env = claude.EnvWithCredentials(os.Environ(), p.APIKey, p.BaseURL)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		stderrMsg := truncateMsg(stderr.String(), 800)
		stdoutMsg := truncateMsg(stdout.String(), 800)
		detail := stderrMsg
		if detail == "" {
			detail = stdoutMsg
		}
		exitMsg := err.Error()
		if fmt.Sprintf("%v", ctx.Err()) == "context deadline exceeded" {
			exitMsg = fmt.Sprintf("timed out after %s — proxy/relay slow or unreachable", testClaudeTimeout)
			detail = ""
		}
		humanErr := exitMsg
		if detail != "" {
			humanErr = detail + " (" + exitMsg + ")"
		}
		log.Printf("test-provider[%d]: FAIL after %s — %s", idx, time.Since(startedAt).Round(time.Millisecond), humanErr)
		writeJSON(w, 200, map[string]any{
			"status": "error",
			"error":  humanErr,
			"stderr": stderrMsg,
			"stdout": stdoutMsg,
		})
		return
	}

	out := truncateMsg(stdout.String(), 400)
	log.Printf("test-provider[%d]: OK after %s — reply=%q", idx, time.Since(startedAt).Round(time.Millisecond), out)
	writeJSON(w, 200, map[string]any{
		"status": "ok",
		"reply":  out,
	})
}

// parseProviderIndex extracts the provider index from the URL path.
// The URL pattern is /api/providers/{index}/... or /api/providers/{index}.
// Returns (index, true) on success, writes an error response and returns
// (0, false) on failure.
func parseProviderIndex(w http.ResponseWriter, r *http.Request) (int, bool) {
	// Path: /api/providers/0 or /api/providers/0/test etc.
	path := strings.TrimPrefix(r.URL.Path, "/api/providers/")
	// Strip any trailing segment (e.g. "/test", "/reveal")
	if i := strings.Index(path, "/"); i >= 0 {
		path = path[:i]
	}
	var idx int
	if _, err := fmt.Sscanf(path, "%d", &idx); err != nil || idx < 0 {
		writeJSON(w, 400, map[string]string{"error": "invalid provider index"})
		return 0, false
	}
	return idx, true
}
