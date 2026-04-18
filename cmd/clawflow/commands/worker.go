package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
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
	return cmd
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
	fmt.Printf("Worker started — polling %s every %ds\n", wc.SaasURL, pollSecs)
	fmt.Println("Press Ctrl+C to stop.")

	for {
		tasks, err := fetchTasks(wc)
		if err != nil {
			fmt.Printf("[warn] fetch tasks: %v\n", err)
		} else {
			for _, t := range tasks {
				if err := processTask(wc, t); err != nil {
					fmt.Printf("[error] task %s: %v\n", t.ID, err)
				}
			}
		}
		time.Sleep(time.Duration(pollSecs) * time.Second)
	}
}

func fetchTasks(wc *config.WorkerConfig) ([]workerTask, error) {
	req, err := http.NewRequest("GET", wc.SaasURL+"/api/v1/worker/tasks", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+wc.WorkerToken)

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
	req.Header.Set("Authorization", "Bearer "+wc.WorkerToken)

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
	req.Header.Set("Authorization", "Bearer "+wc.WorkerToken)
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
	req.Header.Set("Authorization", "Bearer "+wc.WorkerToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

func processTask(wc *config.WorkerConfig, t workerTask) error {
	fmt.Printf("[task %s] claiming...\n", t.ID)
	if err := claimTask(wc, t.ID); err != nil {
		return fmt.Errorf("claim: %w", err)
	}

	fmt.Printf("[task %s] running pipeline...\n", t.ID)
	out, err := runPipeline(t.Payload)
	if err != nil {
		reason := err.Error()
		if len(out) > 0 {
			reason = string(out)
		}
		_ = reportFail(wc, t.ID, reason)
		return fmt.Errorf("pipeline: %w", err)
	}

	// Extract PR URL from output if present (best-effort).
	prURL := extractPRURL(string(out))
	fmt.Printf("[task %s] complete, pr=%s\n", t.ID, prURL)
	return reportComplete(wc, t.ID, prURL)
}

// runPipeline invokes `claude -p "ClawFlow run"` with the task payload as stdin.
func runPipeline(payload json.RawMessage) ([]byte, error) {
	cmd := exec.Command("claude", "-p", "ClawFlow run")
	cmd.Stdin = bytes.NewReader(payload)
	return cmd.CombinedOutput()
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
