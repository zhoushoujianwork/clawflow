package commands

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime/debug"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func NewWorkerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Manage ClawFlow SaaS worker",
	}
	cmd.AddCommand(newWorkerStartCmd())
	cmd.AddCommand(newWorkerStatusCmd())
	return cmd
}

func newWorkerStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show worker configuration and verify SaaS connectivity",
		RunE: func(cmd *cobra.Command, args []string) error {
			wc, err := config.LoadWorkerConfig()
			if err != nil {
				return fmt.Errorf("load worker config: %w", err)
			}
			if wc.SaasURL == "" || wc.WorkerToken == "" {
				fmt.Println("Worker not configured — run: clawflow login")
				return nil
			}

			masked := wc.WorkerToken[:min(8, len(wc.WorkerToken))] + "***"
			fmt.Printf("saas_url:     %s\n", wc.SaasURL)
			fmt.Printf("worker_token: %s\n", masked)
			fmt.Printf("config:       ~/.clawflow/config/worker.yaml\n\n")

			// Verify token by fetching tasks.
			req, err := http.NewRequest("GET", wc.SaasURL+"/api/v1/worker/tasks", nil)
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+wc.WorkerToken)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				fmt.Printf("connectivity: FAILED (%v)\n", err)
				return nil
			}
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				fmt.Printf("connectivity: FAILED (token rejected, status %d)\n", resp.StatusCode)
				return nil
			}
			fmt.Printf("connectivity: OK (status %d)\n", resp.StatusCode)
			return nil
		},
	}
}

func newWorkerStartCmd() *cobra.Command {
	var pollInterval int

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start polling the SaaS task queue and executing pipelines locally",
		Long: `Polls {saas-url}/api/v1/worker/tasks, claims pending tasks, runs the local
ClawFlow pipeline via 'claude -p "ClawFlow run"', then reports success or failure.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			wc, err := config.LoadWorkerConfig()
			if err != nil {
				return fmt.Errorf("load worker config: %w", err)
			}
			if wc.SaasURL == "" || wc.WorkerToken == "" {
				return fmt.Errorf("worker not configured — run: clawflow login --saas-url <url>\n  or: clawflow config set --saas-url <url> --token <cfw_xxx>")
			}
			return runWorker(wc, pollInterval)
		},
	}

	cmd.Flags().IntVar(&pollInterval, "poll-interval", 30, "Seconds between task polls")
	return cmd
}

// workerTask is the shape returned by GET /api/v1/worker/tasks.
type workerTask struct {
	ID      string          `json:"id"`
	Payload json.RawMessage `json:"payload"`
}

func runWorker(wc *config.WorkerConfig, pollSecs int) error {
	agentID := agentIDHeader()
	fmt.Printf("Worker started\n")
	fmt.Printf("  saas:     %s\n", wc.SaasURL)
	fmt.Printf("  agent_id: %s\n", orDash(agentID))
	fmt.Printf("  poll:     every %ds\n", pollSecs)
	fmt.Println("Press Ctrl+C to stop.")

	// Send initial heartbeat, then every 60 s regardless of poll interval.
	sendHeartbeat(wc)
	heartbeatTicker := time.NewTicker(60 * time.Second)
	defer heartbeatTicker.Stop()

	pollCount := 0
	for {
		select {
		case <-heartbeatTicker.C:
			sendHeartbeat(wc)
		default:
		}

		pollCount++
		tasks, err := fetchTasks(wc)
		now := time.Now().Format("15:04:05")
		switch {
		case err != nil:
			fmt.Printf("[%s] poll #%d: fetch failed — %v\n", now, pollCount, err)
		case len(tasks) == 0:
			// Quiet heartbeat: one line every 10 polls so idle logs don't drown.
			if pollCount%10 == 1 {
				fmt.Printf("[%s] poll #%d: idle (no pending tasks)\n", now, pollCount)
			}
		default:
			fmt.Printf("[%s] poll #%d: got %d task(s)\n", now, pollCount, len(tasks))
			for _, t := range tasks {
				if err := processTask(wc, t); err != nil {
					fmt.Printf("[error] task %s: %v\n", t.ID, err)
				}
			}
		}
		time.Sleep(time.Duration(pollSecs) * time.Second)
	}
}

func orDash(s string) string {
	if s == "" {
		return "— (legacy client, no X-Agent-Id sent)"
	}
	return s
}

func sendHeartbeat(wc *config.WorkerConfig) {
	hostname, _ := os.Hostname()
	cliVersion := cliVersionString()
	body, _ := json.Marshal(map[string]string{
		"hostname":    hostname,
		"cli_version": cliVersion,
	})
	req, err := http.NewRequest("POST", wc.SaasURL+"/api/v1/worker/heartbeat", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+wc.WorkerToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("[warn] heartbeat: %v\n", err)
		return
	}
	resp.Body.Close()
}

func cliVersionString() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Version
	}
	return ""
}

// agentIDHeader returns the agent_id saved by `clawflow connect run`, if any.
// Empty string = legacy client (server returns all bound-repo tasks).
func agentIDHeader() string {
	saas, err := config.LoadSaasConfig()
	if err != nil || saas == nil {
		return ""
	}
	return saas.AgentID
}

func setWorkerHeaders(req *http.Request, wc *config.WorkerConfig) {
	req.Header.Set("Authorization", "Bearer "+wc.WorkerToken)
	if id := agentIDHeader(); id != "" {
		req.Header.Set("X-Agent-Id", id)
	}
}

func fetchTasks(wc *config.WorkerConfig) ([]workerTask, error) {
	req, err := http.NewRequest("GET", wc.SaasURL+"/api/v1/worker/tasks", nil)
	if err != nil {
		return nil, err
	}
	setWorkerHeaders(req, wc)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}

	var tasks []workerTask
	if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

func claimTask(wc *config.WorkerConfig, taskID string) error {
	req, err := http.NewRequest("POST", wc.SaasURL+"/api/v1/worker/tasks/"+taskID+"/claim", nil)
	if err != nil {
		return err
	}
	setWorkerHeaders(req, wc)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("claim status %d: %s", resp.StatusCode, b)
	}
	return nil
}

func reportComplete(wc *config.WorkerConfig, taskID, prURL string) error {
	body, _ := json.Marshal(map[string]string{"pr_url": prURL})
	req, err := http.NewRequest("POST", wc.SaasURL+"/api/v1/worker/tasks/"+taskID+"/complete", bytes.NewReader(body))
	if err != nil {
		return err
	}
	setWorkerHeaders(req, wc)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func reportFail(wc *config.WorkerConfig, taskID, reason string) error {
	body, _ := json.Marshal(map[string]string{"reason": reason})
	req, err := http.NewRequest("POST", wc.SaasURL+"/api/v1/worker/tasks/"+taskID+"/fail", bytes.NewReader(body))
	if err != nil {
		return err
	}
	setWorkerHeaders(req, wc)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func processTask(wc *config.WorkerConfig, t workerTask) error {
	start := time.Now()
	fmt.Printf("[task %s] claim ...\n", t.ID)
	if err := claimTask(wc, t.ID); err != nil {
		return fmt.Errorf("claim: %w", err)
	}

	fmt.Printf("[task %s] run 'claude -p \"ClawFlow run\"' ...\n", t.ID)
	result, err := runPipeline(t.Payload)
	dur := time.Since(start).Round(time.Second)

	// Push logs + usage to SaaS regardless of success/failure so the UI has a
	// record even when the pipeline crashes halfway.
	if len(result.Logs) > 0 {
		if lerr := reportLogs(wc, t.ID, result.Logs); lerr != nil {
			fmt.Printf("[warn] upload logs for %s: %v\n", t.ID, lerr)
		}
	}
	if result.Usage.HasCost() {
		if uerr := reportTaskUsage(wc, t.ID, result.Usage); uerr != nil {
			fmt.Printf("[warn] report usage for %s: %v\n", t.ID, uerr)
		}
	}

	if err != nil {
		reason := err.Error()
		if len(result.RawTail) > 0 {
			reason = result.RawTail
		}
		fmt.Printf("[task %s] FAILED in %s — %v\n", t.ID, dur, err)
		_ = reportFail(wc, t.ID, reason)
		return fmt.Errorf("pipeline: %w", err)
	}

	if result.PRURL == "" {
		fmt.Printf("[task %s] OK in %s (no PR URL parsed from output)\n", t.ID, dur)
	} else {
		fmt.Printf("[task %s] OK in %s — PR: %s\n", t.ID, dur, result.PRURL)
	}
	return reportComplete(wc, t.ID, result.PRURL)
}

// ── runPipeline: stream-json parse ────────────────────────────────────────────

// LogEntry matches the SaaS /worker/tasks/:id/logs payload.
type LogEntry struct {
	Level     string `json:"level"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp,omitempty"`
}

// UsageReport matches /worker/tasks/:id/usage.
type UsageReport struct {
	Model               string   `json:"model,omitempty"`
	InputTokens         int64    `json:"input_tokens"`
	OutputTokens        int64    `json:"output_tokens"`
	CacheCreationTokens int64    `json:"cache_creation_tokens"`
	CacheReadTokens     int64    `json:"cache_read_tokens"`
	TotalCostUSD        *float64 `json:"total_cost_usd,omitempty"`
}

func (u UsageReport) HasCost() bool {
	return u.TotalCostUSD != nil || u.InputTokens > 0 || u.OutputTokens > 0
}

// PipelineResult is the structured outcome of one `claude -p` invocation.
type PipelineResult struct {
	Logs    []LogEntry
	Usage   UsageReport
	PRURL   string
	RawTail string // last ~4 KB of raw output, for failure reasons
}

// runPipeline invokes `claude -p --output-format stream-json --verbose` with
// the task payload on stdin. Parses the NDJSON event stream so we can:
//   - surface human-readable progress live on worker stdout
//   - accumulate structured log entries to POST to SaaS
//   - extract total_cost_usd + token usage from the final `result` event
//   - scrape the PR URL out of assistant messages
//
// Falls back gracefully for non-JSON lines (treated as plain log messages).
func runPipeline(payload json.RawMessage) (PipelineResult, error) {
	cmd := exec.Command("claude", "-p",
		"--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions", "ClawFlow run")
	cmd.Stdin = bytes.NewReader(payload)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return PipelineResult{}, err
	}
	// Stderr we still tee to our own stderr so operators see crashes.
	var errBuf bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &errBuf)

	if err := cmd.Start(); err != nil {
		return PipelineResult{}, err
	}

	var (
		logs     []LogEntry
		usage    UsageReport
		prURL    string
		rawTail  bytes.Buffer
		rawLimit = 4096
	)

	// Use a 1 MB-capacity scanner so large JSON events (full diff attachments)
	// don't get truncated silently.
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)

	appendLog := func(level, message string) {
		if message == "" {
			return
		}
		logs = append(logs, LogEntry{Level: level, Message: message, Timestamp: time.Now().UTC().Format(time.RFC3339)})
		// Mirror to worker stdout in a compact form.
		fmt.Printf("  %s\n", oneLine(message))
	}

	for sc.Scan() {
		line := sc.Bytes()
		// Track raw tail for failure reason fallback.
		if rawTail.Len()+len(line)+1 > rawLimit {
			// Keep only the most recent chunk.
			excess := rawTail.Len() + len(line) + 1 - rawLimit
			rem := rawTail.Bytes()[excess:]
			rawTail.Reset()
			rawTail.Write(rem)
		}
		rawTail.Write(line)
		rawTail.WriteByte('\n')

		var evt map[string]interface{}
		if err := json.Unmarshal(line, &evt); err != nil {
			// Non-JSON line — treat as plain info log.
			appendLog("info", string(line))
			continue
		}
		handleStreamEvent(evt, &logs, &usage, &prURL, appendLog)
	}

	waitErr := cmd.Wait()

	// If we never recorded any logs but claude printed to stderr, surface that.
	if len(logs) == 0 && errBuf.Len() > 0 {
		appendLog("error", errBuf.String())
	}

	return PipelineResult{
		Logs:    logs,
		Usage:   usage,
		PRURL:   prURL,
		RawTail: rawTail.String(),
	}, waitErr
}

// handleStreamEvent interprets a single parsed JSON event from claude's
// stream-json output and updates our accumulators.
func handleStreamEvent(
	evt map[string]interface{},
	logs *[]LogEntry,
	usage *UsageReport,
	prURL *string,
	appendLog func(level, message string),
) {
	typ, _ := evt["type"].(string)
	switch typ {
	case "system":
		if sub, _ := evt["subtype"].(string); sub != "" {
			appendLog("info", fmt.Sprintf("system: %s", sub))
		}
	case "assistant":
		// message.content is an array of {type, text|name|input, ...}
		text := extractAssistantText(evt)
		if text != "" {
			appendLog("assistant", text)
			if *prURL == "" {
				if u := extractPRURL(text); u != "" {
					*prURL = u
				}
			}
		}
	case "user":
		// Typically tool-result replays. Summarise one line.
		if t := extractAssistantText(evt); t != "" {
			appendLog("tool", oneLine(t))
		}
	case "result":
		// Final event: cost + usage + optional result text.
		if cost, ok := evt["total_cost_usd"].(float64); ok {
			c := cost
			usage.TotalCostUSD = &c
		}
		if u, ok := evt["usage"].(map[string]interface{}); ok {
			usage.InputTokens = asInt(u["input_tokens"])
			usage.OutputTokens = asInt(u["output_tokens"])
			usage.CacheCreationTokens = asInt(u["cache_creation_input_tokens"])
			usage.CacheReadTokens = asInt(u["cache_read_input_tokens"])
		}
		if m, ok := evt["model"].(string); ok {
			usage.Model = m
		}
		if r, ok := evt["result"].(string); ok && r != "" {
			appendLog("result", r)
			if *prURL == "" {
				if u := extractPRURL(r); u != "" {
					*prURL = u
				}
			}
		}
	}
}

func extractAssistantText(evt map[string]interface{}) string {
	msg, ok := evt["message"].(map[string]interface{})
	if !ok {
		return ""
	}
	content, ok := msg["content"].([]interface{})
	if !ok {
		return ""
	}
	var out bytes.Buffer
	for _, c := range content {
		block, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		switch block["type"] {
		case "text":
			if t, ok := block["text"].(string); ok {
				out.WriteString(t)
				out.WriteByte('\n')
			}
		case "tool_use":
			if name, ok := block["name"].(string); ok {
				out.WriteString("→ tool: ")
				out.WriteString(name)
				out.WriteByte('\n')
			}
		case "tool_result":
			if t, ok := block["content"].(string); ok {
				out.WriteString(oneLine(t))
				out.WriteByte('\n')
			}
		}
	}
	return out.String()
}

func asInt(v interface{}) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	}
	return 0
}

func oneLine(s string) string {
	const max = 400
	// Collapse whitespace runs.
	var b bytes.Buffer
	var lastSpace bool
	for _, r := range s {
		if r == '\n' || r == '\t' || r == '\r' {
			r = ' '
		}
		if r == ' ' {
			if lastSpace {
				continue
			}
			lastSpace = true
		} else {
			lastSpace = false
		}
		b.WriteRune(r)
		if b.Len() >= max {
			b.WriteString("…")
			break
		}
	}
	return b.String()
}

// ── SaaS uploads (logs + usage) ──────────────────────────────────────────────

func reportLogs(wc *config.WorkerConfig, taskID string, logs []LogEntry) error {
	body, _ := json.Marshal(map[string]interface{}{"logs": logs})
	req, err := http.NewRequest("POST", wc.SaasURL+"/api/v1/worker/tasks/"+taskID+"/logs", bytes.NewReader(body))
	if err != nil {
		return err
	}
	setWorkerHeaders(req, wc)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	return nil
}

func reportTaskUsage(wc *config.WorkerConfig, taskID string, u UsageReport) error {
	body, _ := json.Marshal(u)
	req, err := http.NewRequest("POST", wc.SaasURL+"/api/v1/worker/tasks/"+taskID+"/usage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	setWorkerHeaders(req, wc)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// extractPRURL does a simple scan for a GitHub/GitLab PR URL in the output.
func extractPRURL(output string) string {
	// Look for https://github.com/.../pull/N or https://gitlab.*/merge_requests/N
	for _, prefix := range []string{"https://github.com/", "https://gitlab."} {
		idx := 0
		for {
			i := indexOf(output[idx:], prefix)
			if i < 0 {
				break
			}
			start := idx + i
			end := start
			for end < len(output) && output[end] != ' ' && output[end] != '\n' && output[end] != '"' {
				end++
			}
			candidate := output[start:end]
			if containsAny(candidate, "/pull/", "/merge_requests/") {
				return candidate
			}
			idx = start + 1
		}
	}
	return ""
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}
