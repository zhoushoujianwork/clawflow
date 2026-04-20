package commands

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// runContext is persisted between `pipeline start` and `pipeline finish` so
// the finish call can resend the same run_id and repo coordinates.
type runContext struct {
	ID          string `json:"id"`
	Platform    string `json:"platform"`
	FullName    string `json:"full_name"`
	IssueNumber int    `json:"issue_number"`
	IssueTitle  string `json:"issue_title,omitempty"`
}

func runContextPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "current_run.json")
}

func currentRunIDPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "current_run_id")
}

func NewPipelineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pipeline",
		Short: "Report local pipeline status to SaaS (used by the ClawFlow skill)",
	}
	cmd.AddCommand(newPipelineStartCmd())
	cmd.AddCommand(newPipelineFinishCmd())
	return cmd
}

func newPipelineStartCmd() *cobra.Command {
	var platform, repo, title string
	var issue int

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Mark a pipeline run as started; prints the generated run_id",
		Long: `Generates a new run_id, records it locally (~/.clawflow/current_run.json),
and POSTs status=running to SaaS. Subsequent 'clawflow pipeline finish' or
'clawflow billing-hook' calls read the same run_id.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if platform == "" || repo == "" || issue == 0 {
				return fmt.Errorf("--platform, --repo, and --issue are required")
			}
			id, err := newUUIDv4()
			if err != nil {
				return err
			}
			ctx := runContext{
				ID:          id,
				Platform:    platform,
				FullName:    repo,
				IssueNumber: issue,
				IssueTitle:  title,
			}
			if err := saveRunContext(ctx); err != nil {
				return err
			}
			_ = os.WriteFile(currentRunIDPath(), []byte(id), 0o600)

			body := map[string]any{
				"id":           id,
				"platform":     platform,
				"full_name":    repo,
				"issue_number": issue,
				"status":       "running",
				"started_at":   time.Now().UTC().Format(time.RFC3339),
			}
			if title != "" {
				body["issue_title"] = title
			}
			if err := pushRun(body); err != nil {
				fmt.Fprintf(os.Stderr, "warn: pipeline start push failed: %v\n", err)
			}
			fmt.Println(id)
			return nil
		},
	}
	cmd.Flags().StringVar(&platform, "platform", "", "github | gitlab")
	cmd.Flags().StringVar(&repo, "repo", "", "owner/name")
	cmd.Flags().IntVar(&issue, "issue", 0, "issue number")
	cmd.Flags().StringVar(&title, "title", "", "issue title (optional)")
	return cmd
}

func newPipelineFinishCmd() *cobra.Command {
	var status, prURL string
	var prNumber, confidence int

	cmd := &cobra.Command{
		Use:   "finish",
		Short: "Mark the active pipeline run as success/failed/skipped",
		RunE: func(cmd *cobra.Command, args []string) error {
			if status == "" {
				return fmt.Errorf("--status is required (success|failed|skipped)")
			}
			ctx, err := loadRunContext()
			if err != nil {
				return fmt.Errorf("no active pipeline run — call 'clawflow pipeline start' first: %w", err)
			}

			body := map[string]any{
				"id":           ctx.ID,
				"platform":     ctx.Platform,
				"full_name":    ctx.FullName,
				"issue_number": ctx.IssueNumber,
				"status":       status,
				"finished_at":  time.Now().UTC().Format(time.RFC3339),
			}
			if ctx.IssueTitle != "" {
				body["issue_title"] = ctx.IssueTitle
			}
			if prURL != "" {
				body["pr_url"] = prURL
			}
			if prNumber != 0 {
				body["pr_number"] = prNumber
			}
			if confidence != 0 {
				body["confidence"] = confidence
			}

			if err := pushRun(body); err != nil {
				fmt.Fprintf(os.Stderr, "warn: pipeline finish push failed: %v\n", err)
			}
			_ = os.Remove(runContextPath())
			_ = os.Remove(currentRunIDPath())
			return nil
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "success | failed | skipped")
	cmd.Flags().StringVar(&prURL, "pr-url", "", "URL of the created PR")
	cmd.Flags().IntVar(&prNumber, "pr-number", 0, "PR number")
	cmd.Flags().IntVar(&confidence, "confidence", 0, "feasibility confidence score 0-100")
	return cmd
}

func pushRun(body map[string]any) error {
	wc, err := config.LoadWorkerConfig()
	if err != nil {
		return err
	}
	if wc.SaasURL == "" || wc.WorkerToken == "" {
		return fmt.Errorf("worker not configured — run: clawflow login")
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", wc.SaasURL+"/api/v1/worker/pipelines", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+wc.WorkerToken)
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

func saveRunContext(ctx runContext) error {
	path := runContextPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(ctx, "", "  ")
	return os.WriteFile(path, data, 0o600)
}

func loadRunContext() (runContext, error) {
	var ctx runContext
	data, err := os.ReadFile(runContextPath())
	if err != nil {
		return ctx, err
	}
	err = json.Unmarshal(data, &ctx)
	return ctx, err
}

// newUUIDv4 generates a random RFC4122 v4 UUID without any external dependency.
func newUUIDv4() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	), nil
}
