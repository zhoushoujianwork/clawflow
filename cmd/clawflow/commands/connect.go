package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func NewConnectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connect",
		Short: "Connect to ClawFlow SaaS and sync local config",
	}
	cmd.AddCommand(newConnectRunCmd())
	cmd.AddCommand(newSyncCmd())
	cmd.AddCommand(newDisconnectCmd())
	return cmd
}

// clawflow connect --url https://app.clawflow.io --token <api-key> --org-id <uuid>
func newConnectRunCmd() *cobra.Command {
	var url, token, orgID, tagsCSV string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Register this machine with ClawFlow SaaS",
		RunE: func(cmd *cobra.Command, args []string) error {
			hostname, _ := os.Hostname()

			tags := []string{}
			for _, t := range strings.Split(tagsCSV, ",") {
				if trimmed := strings.TrimSpace(t); trimmed != "" {
					tags = append(tags, trimmed)
				}
			}

			// Register agent
			body, _ := json.Marshal(map[string]any{
				"hostname":    hostname,
				"cli_version": Version,
				"org_id":      orgID,
				"tags":        tags,
			})
			req, _ := http.NewRequest("POST", url+"/api/v1/agents/register", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("register failed: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				b, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("register failed (%d): %s", resp.StatusCode, b)
			}

			var reg struct {
				AgentID   string `json:"agent_id"`
				SyncToken string `json:"sync_token"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&reg); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}

			saas := &config.SaasConfig{
				URL:       url,
				OrgID:     orgID,
				AgentID:   reg.AgentID,
				SyncToken: reg.SyncToken,
			}
			if err := saas.Save(); err != nil {
				return fmt.Errorf("save saas config: %w", err)
			}

			fmt.Printf("Connected! agent_id=%s\n", reg.AgentID)
			fmt.Println("Syncing local repos to SaaS...")
			n, err := pushConfig(saas)
			if err != nil {
				return err
			}
			fmt.Printf("Pushed %d repos to SaaS.\n", n)
			return nil
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "SaaS base URL (required)")
	cmd.Flags().StringVar(&token, "token", "", "API token (required)")
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization UUID (required)")
	cmd.Flags().StringVar(&tagsCSV, "tags", "", "Comma-separated capability tags (e.g. docker,gpu,vpn)")
	_ = cmd.MarkFlagRequired("url")
	_ = cmd.MarkFlagRequired("token")
	_ = cmd.MarkFlagRequired("org-id")
	return cmd
}

// clawflow connect sync [push|pull]
func newSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync [push|pull]",
		Short: "Sync config between local and SaaS",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			saas, err := config.LoadSaasConfig()
			if err != nil {
				return fmt.Errorf("not connected — run: clawflow connect run --url ... --token ... --org-id ...")
			}
			direction := "push"
			if len(args) > 0 {
				direction = args[0]
			}
			switch direction {
			case "push":
				n, err := pushConfig(saas)
				if err != nil {
					return err
				}
				fmt.Printf("Pushed %d repos to SaaS.\n", n)
				return nil
			case "pull":
				n, err := pullConfig(saas)
				if err != nil {
					return err
				}
				if n == 0 {
					fmt.Println("No changes from SaaS.")
				} else {
					fmt.Printf("Pulled %d repo updates from SaaS.\n", n)
				}
				return nil
			default:
				return fmt.Errorf("unknown direction %q, use push or pull", direction)
			}
		},
	}
	return cmd
}

func newDisconnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect",
		Short: "Remove SaaS connection from this machine",
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.SaasConfigPath()
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Println("Disconnected from SaaS.")
			return nil
		},
	}
}

// ── sync helpers ──────────────────────────────────────────────────────────────

// pushConfig uploads the local repo config to SaaS. Returns the number
// of repos the server acknowledged syncing plus any error. The caller
// owns stdout — interactive entry points (`connect run`, the manual
// `sync push` fallback) print a summary, while the worker's periodic
// sync pass and the mutation-time best-effort push stay silent on
// success so they don't flood logs.
func pushConfig(saas *config.SaasConfig) (int, error) {
	cfg, err := config.Load()
	if err != nil {
		return 0, fmt.Errorf("load local config: %w", err)
	}

	type repoPayload struct {
		FullName    string  `json:"full_name"`
		Platform    string  `json:"platform"`
		BaseBranch  string  `json:"base_branch"`
		TestCommand *string `json:"test_command,omitempty"`
		CIRequired  *bool   `json:"ci_required,omitempty"`
		Enabled     *bool   `json:"enabled,omitempty"`
		LocalPath   *string `json:"local_path,omitempty"`
	}
	var repos []repoPayload
	for name, r := range cfg.Repos {
		platform := r.Platform
		if platform == "" {
			platform = "github"
		}
		p := repoPayload{
			FullName:   name,
			Platform:   platform,
			BaseBranch: r.BaseBranch,
			Enabled:    &r.Enabled,
			CIRequired: &r.CIRequired,
		}
		if r.TestCommand != "" {
			p.TestCommand = &r.TestCommand
		}
		if r.LocalPath != "" {
			p.LocalPath = &r.LocalPath
		}
		repos = append(repos, p)
	}

	body, _ := json.Marshal(map[string]any{"repos": repos})
	req, _ := http.NewRequest("POST", saas.URL+"/api/v1/sync/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-sync-token", saas.SyncToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("push failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("push failed (%d): %s", resp.StatusCode, b)
	}

	var result struct {
		Synced int `json:"synced"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	return result.Synced, nil
}

// bestEffortPushConfig pushes local repo config to SaaS after a local
// mutation (`repo add/remove/enable/disable/set`). Fails open: if the
// user never ran `clawflow connect`, we silently skip — the legacy
// local-only mode is still a first-class path. If the user is connected
// but SaaS is unreachable, we print one warning and carry on: the
// worker's periodic sync loop will reconcile on its next pass.
func bestEffortPushConfig() {
	saas, err := config.LoadSaasConfig()
	if err != nil || saas == nil || saas.URL == "" || saas.SyncToken == "" {
		return
	}
	if _, err := pushConfig(saas); err != nil {
		fmt.Printf("[sync] warn: could not push to SaaS (%v) — worker loop will retry.\n", err)
	}
}

// pullConfig runs one incremental `/sync/config?since=` pull and merges the
// results into the local config file. Returns the number of repos that
// changed (0 = nothing new from SaaS). Logging is the caller's job —
// interactive `connect sync pull` prints a user-facing summary, while the
// worker's periodic sync loop (issue-31 follow-up) only wants a line when
// there's actually something to report.
func pullConfig(saas *config.SaasConfig) (int, error) {
	pullURL := saas.URL + "/api/v1/sync/config"
	if saas.LastSync != "" {
		pullURL += "?since=" + saas.LastSync
	}
	req, _ := http.NewRequest("GET", pullURL, nil)
	req.Header.Set("x-sync-token", saas.SyncToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("pull failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("pull failed (%d): %s", resp.StatusCode, b)
	}

	var result struct {
		Repos []struct {
			FullName              string    `json:"full_name"`
			Platform              string    `json:"platform"`
			BaseBranch            string    `json:"base_branch"`
			Enabled               bool      `json:"enabled"`
			TestCommand           *string   `json:"test_command"`
			CIRequired            bool      `json:"ci_required"`
			LocalPath             *string   `json:"local_path"`
			AutoEvaluateAllIssues bool      `json:"auto_evaluate_all_issues"`
			UpdatedAt             time.Time `json:"updated_at"`
		} `json:"repos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}

	if len(result.Repos) == 0 {
		return 0, nil
	}

	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{Repos: make(map[string]config.Repo)}
	}
	if cfg.Repos == nil {
		cfg.Repos = make(map[string]config.Repo)
	}

	for _, r := range result.Repos {
		existing := cfg.Repos[r.FullName]
		existing.Enabled = r.Enabled
		existing.BaseBranch = r.BaseBranch
		existing.Platform = r.Platform
		existing.CIRequired = r.CIRequired
		existing.AutoEvaluateAllIssues = r.AutoEvaluateAllIssues
		if r.TestCommand != nil {
			existing.TestCommand = *r.TestCommand
		}
		if r.LocalPath != nil {
			existing.LocalPath = *r.LocalPath
		}
		cfg.Repos[r.FullName] = existing
	}

	if err := cfg.Save(); err != nil {
		return 0, fmt.Errorf("save local config: %w", err)
	}

	last := result.Repos[len(result.Repos)-1].UpdatedAt
	saas.LastSync = last.Format(time.RFC3339Nano)
	_ = saas.Save()

	return len(result.Repos), nil
}
