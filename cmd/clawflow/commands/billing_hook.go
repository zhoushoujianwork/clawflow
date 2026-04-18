package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"bytes"
	"net/http"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// StopHookPayload is the stdin JSON from Claude Code Stop hook.
type StopHookPayload struct {
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	Model          *struct {
		ID string `json:"id"`
	} `json:"model"`
	ContextWindow *struct {
		CurrentUsage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"current_usage"`
	} `json:"context_window"`
	Cost *struct {
		TotalCostUSD *float64 `json:"total_cost_usd"`
	} `json:"cost"`
}

func NewBillingHookCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "billing-hook",
		Short:  "Called by Claude Code Stop hook to report token usage to SaaS",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			saas, err := config.LoadSaasConfig()
			if err != nil {
				// Not connected to SaaS — silently exit, don't block Claude Code
				return nil
			}

			raw, err := io.ReadAll(os.Stdin)
			if err != nil || len(raw) == 0 {
				return nil
			}

			var payload StopHookPayload
			if err := json.Unmarshal(raw, &payload); err != nil {
				return nil
			}

			if payload.ContextWindow == nil || payload.ContextWindow.CurrentUsage == nil {
				return nil
			}
			u := payload.ContextWindow.CurrentUsage

			// Read run_id written by SKILL.md before execution
			runID := readCurrentRunID()

			var costUSD *float64
			if payload.Cost != nil {
				costUSD = payload.Cost.TotalCostUSD
			}

			modelID := ""
			if payload.Model != nil {
				modelID = payload.Model.ID
			}

			return reportUsage(saas, runID, modelID, u.InputTokens, u.OutputTokens,
				u.CacheCreationInputTokens, u.CacheReadInputTokens, costUSD)
		},
	}
}

func readCurrentRunID() string {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".clawflow", "current_run_id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func reportUsage(saas *config.SaasConfig, runID, model string,
	input, output, cacheCreate, cacheRead int64, costUSD *float64) error {

	body := map[string]any{
		"input_tokens":           input,
		"output_tokens":          output,
		"cache_creation_tokens":  cacheCreate,
		"cache_read_tokens":      cacheRead,
	}
	if runID != "" {
		body["run_id"] = runID
	}
	if model != "" {
		body["model"] = model
	}
	if costUSD != nil {
		body["total_cost_usd"] = *costUSD
	}

	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", saas.URL+"/api/v1/billing/usage", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-sync-token", saas.SyncToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "clawflow billing-hook: report failed: %v\n", err)
		return nil // don't block Claude Code
	}
	defer resp.Body.Close()
	return nil
}
