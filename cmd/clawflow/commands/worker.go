package commands

import (
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
	out, err := runPipeline(t.Payload)
	dur := time.Since(start).Round(time.Second)
	if err != nil {
		reason := err.Error()
		if len(out) > 0 {
			reason = string(out)
		}
		fmt.Printf("[task %s] FAILED in %s — %v\n", t.ID, dur, err)
		_ = reportFail(wc, t.ID, reason)
		return fmt.Errorf("pipeline: %w", err)
	}

	prURL := extractPRURL(string(out))
	if prURL == "" {
		fmt.Printf("[task %s] OK in %s (no PR URL parsed from output)\n", t.ID, dur)
	} else {
		fmt.Printf("[task %s] OK in %s — PR: %s\n", t.ID, dur, prURL)
	}
	return reportComplete(wc, t.ID, prURL)
}

// runPipeline invokes `claude -p "ClawFlow run"` with the task payload as
// stdin. Streams child stdout/stderr to the worker's own stdout so the user
// can watch Claude's progress live.
func runPipeline(payload json.RawMessage) ([]byte, error) {
	cmd := exec.Command("claude", "-p", "ClawFlow run")
	cmd.Stdin = bytes.NewReader(payload)

	var buf bytes.Buffer
	cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
	cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
	err := cmd.Run()
	return buf.Bytes(), err
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
