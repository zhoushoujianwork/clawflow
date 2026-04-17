package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
	var url, token, orgID string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Register this machine with ClawFlow SaaS",
		RunE: func(cmd *cobra.Command, args []string) error {
			hostname, _ := os.Hostname()

			// Register agent
			body, _ := json.Marshal(map[string]any{
				"hostname":    hostname,
				"cli_version": Version,
				"org_id":      orgID,
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
			return pushConfig(saas)
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "SaaS base URL (required)")
	cmd.Flags().StringVar(&token, "token", "", "API token (required)")
	cmd.Flags().StringVar(&orgID, "org-id", "", "Organization UUID (required)")
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
				return pushConfig(saas)
			case "pull":
				return pullConfig(saas)
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

func pushConfig(saas *config.SaasConfig) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load local config: %w", err)
	}

	type repoPayload struct {
		FullName   string `json:"full_name"`
		Platform   string `json:"platform"`
		BaseBranch string `json:"base_branch"`
	}
	var repos []repoPayload
	for name, r := range cfg.Repos {
		platform := r.Platform
		if platform == "" {
			platform = "github"
		}
		repos = append(repos, repoPayload{
			FullName:   name,
			Platform:   platform,
			BaseBranch: r.BaseBranch,
		})
	}

	body, _ := json.Marshal(map[string]any{"repos": repos})
	req, _ := http.NewRequest("POST", saas.URL+"/api/v1/sync/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-sync-token", saas.SyncToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("push failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("push failed (%d): %s", resp.StatusCode, b)
	}

	var result struct {
		Synced int `json:"synced"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&result)
	fmt.Printf("Pushed %d repos to SaaS.\n", result.Synced)
	return nil
}

func pullConfig(saas *config.SaasConfig) error {
	url := saas.URL + "/api/v1/sync/config"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("x-sync-token", saas.SyncToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("pull failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("pull failed (%d): %s", resp.StatusCode, b)
	}

	var result struct {
		Repos []struct {
			FullName   string    `json:"full_name"`
			Platform   string    `json:"platform"`
			BaseBranch string    `json:"base_branch"`
			Enabled    bool      `json:"enabled"`
			UpdatedAt  time.Time `json:"updated_at"`
		} `json:"repos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	if len(result.Repos) == 0 {
		fmt.Println("No changes from SaaS.")
		return nil
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
		cfg.Repos[r.FullName] = existing
	}

	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save local config: %w", err)
	}
	fmt.Printf("Pulled %d repo updates from SaaS.\n", len(result.Repos))
	return nil
}
