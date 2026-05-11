// Package api — chat_stream powers the dashboard's embedded streaming
// chat. A POST opens an SSE pipe; the server spawns `claude -p
// --output-format stream-json` and tees each stdout line to the
// browser, prefixed with the SSE `data:` frame. The first event
// carries the session id so the frontend can pass it back on the next
// turn to resume the same conversation.
package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/chat"
	"github.com/zhoushoujianwork/clawflow/internal/claude"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

type chatStreamRequest struct {
	Kind      string `json:"kind"`
	Project   string `json:"project"`
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

// HandleChatStream is the SSE endpoint backing the dashboard's
// per-project chat drawer. The protocol the frontend expects:
//
//   - First event: {"type":"clawflow_session","session_id":"<uuid>"}.
//     The frontend stores this and sends it back on every subsequent
//     turn so claude resumes the same JSONL transcript.
//   - Each subsequent event: a verbatim claude stream-json line wrapped
//     in `data:`. Parsing those is the frontend's job — we don't peek.
//   - On clean stdout close: `data: [DONE]`.
//   - On startup / runtime error: `data: {"type":"clawflow_error", ...}`
//     followed by `[DONE]`.
//
// The handler keeps a single goroutine: stdout-pipe reader doubles as
// the writer to ResponseWriter, so we don't need cross-goroutine
// flushing. The client-disconnect path comes from r.Context().Done()
// and reaps the claude subprocess.
func HandleChatStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req chatStreamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}

	kindName := strings.TrimSpace(req.Kind)
	projectName := strings.TrimSpace(req.Project)
	message := req.Message
	if kindName == "" || projectName == "" || strings.TrimSpace(message) == "" {
		writeJSON(w, 400, map[string]string{"error": "kind, project, message are required"})
		return
	}

	kind, ok := chat.GetKind(kindName)
	if !ok {
		writeJSON(w, 400, map[string]string{"error": fmt.Sprintf("unknown chat kind %q", kindName)})
		return
	}

	p, err := project.Get(projectName)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}

	// Build the member-repo table the same way the Pilot scheduler
	// does: project.Repos lists the slugs, cfg.Repos maps slug →
	// local clone path. Repos missing from cfg or without a local
	// clone still appear (the prompt notes the missing path) so the
	// model knows what's in play.
	cfg, _ := config.Load()
	repos := buildChatRepos(p.Repos, cfg)

	current, err := readCurrentForKind(kindName, p.Name)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}

	systemPrompt := kind.Builder(p.Name, repos, current)

	// New conversations get a fresh per-spawn UUID; resumes use
	// whatever the frontend handed us. We don't validate that an
	// inbound session-id actually corresponds to a session on disk
	// — claude itself errors out cleanly if it's bogus, and that
	// error already flows through to the frontend via SSE.
	sessionID := strings.TrimSpace(req.SessionID)
	resuming := sessionID != ""
	if !resuming {
		sessionID = chat.NewSessionID("project/"+p.Name+"/"+kindName, 0)
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // suppress nginx-style proxy buffering if any
	w.WriteHeader(http.StatusOK)

	sendEvent := func(payload string) {
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}
	sendJSON := func(v any) {
		buf, err := json.Marshal(v)
		if err != nil {
			return
		}
		sendEvent(string(buf))
	}

	// Hand the session id back FIRST so the frontend can persist it
	// before any model output races in.
	sendJSON(map[string]string{"type": "clawflow_session", "session_id": sessionID})

	creds, _ := config.LoadCredentials()
	apiKey, baseURL := "", ""
	if creds != nil {
		apiKey, baseURL = creds.ClaudeAPIKey, creds.ClaudeBaseURL
	}
	model := ""
	if creds != nil {
		model = creds.EffectiveOperatorModel()
	}

	args := []string{
		"-p",
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--system-prompt", systemPrompt,
	}
	if resuming {
		args = append(args, "--resume", sessionID)
	} else {
		args = append(args, "--session-id", sessionID)
	}
	if apiKey != "" {
		args = append(args, "--bare")
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, message)

	// Tie the subprocess lifetime to the request — when the browser
	// disconnects, r.Context().Done() fires and the os/exec package
	// kills the child for us.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	cmd := exec.CommandContext(ctx, claude.Resolve(), args...)
	cmd.Dir = project.ProjectDir(p.Name)
	cmd.Env = claude.EnvWithCredentials(os.Environ(), apiKey, baseURL)
	cmd.Stderr = os.Stderr // claude warnings/log lines go to the server console, not the browser

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		sendJSON(map[string]string{"type": "clawflow_error", "error": fmt.Sprintf("stdout pipe: %v", err)})
		sendEvent("[DONE]")
		return
	}
	if err := cmd.Start(); err != nil {
		sendJSON(map[string]string{"type": "clawflow_error", "error": fmt.Sprintf("claude start: %v", err)})
		sendEvent("[DONE]")
		return
	}

	sc := bufio.NewScanner(stdout)
	// stream-json lines can carry full assistant messages; bump the
	// default 64KB cap for the same reason operator/claude.go does.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		sendEvent(line)
	}
	scanErr := sc.Err()

	// Wait for the process to fully exit BEFORE [DONE] so the
	// frontend's "stream finished" signal is true (no late stderr
	// surfacing into the next request's transcript).
	waitErr := cmd.Wait()
	if scanErr != nil {
		sendJSON(map[string]string{"type": "clawflow_error", "error": fmt.Sprintf("read stream: %v", scanErr)})
	} else if waitErr != nil && ctx.Err() == nil {
		// ctx.Err() != nil = client disconnected; in that case
		// kill-by-context is the expected exit cause and we should
		// not surface it as an error.
		sendJSON(map[string]string{"type": "clawflow_error", "error": fmt.Sprintf("claude: %v", waitErr)})
	}
	sendEvent("[DONE]")
}

// readCurrentForKind reads the current file content the chat is
// editing — context.md is the only kind currently. Empty on missing
// files (treated by the prompt as "fresh draft").
func readCurrentForKind(kind, projectName string) (string, error) {
	switch kind {
	case "context":
		return project.ReadContext(projectName)
	default:
		// Future-proof: an unknown kind that somehow passed validation
		// just gets an empty starting context — better than failing
		// the whole stream over a missing case branch.
		return "", nil
	}
}

// buildChatRepos turns a project's repo slugs into the chat package's
// table-row form, looking up local_path from the active config. A
// repo missing from cfg.Repos still produces a row (no LocalPath), so
// the model knows the slug exists even if it's not locally cloned.
func buildChatRepos(repos []string, cfg *config.Config) []chat.ProjectChatRepo {
	out := make([]chat.ProjectChatRepo, 0, len(repos))
	for _, name := range repos {
		row := chat.ProjectChatRepo{Name: name}
		if cfg != nil {
			if rc, ok := cfg.Repos[name]; ok {
				row.LocalPath = rc.LocalPath
			}
		}
		out = append(out, row)
	}
	return out
}
