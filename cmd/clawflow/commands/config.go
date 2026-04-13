package commands

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func NewConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage ClawFlow configuration",
	}
	cmd.AddCommand(newConfigSetTokenCmd())
	cmd.AddCommand(newConfigShowCmd())
	return cmd
}

func newConfigSetTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-token <gh-token>",
		Short: "Store GitHub token in ~/.clawflow/config/credentials.yaml",
		Long: `Saves the GitHub personal access token to ~/.clawflow/config/credentials.yaml (mode 0600).
The token is used by clawflow harvest, status, and label commands.

Required scopes: repo (full), read:org`,
		Args:    cobra.ExactArgs(1),
		Example: "  clawflow config set-token ghp_xxxxxxxxxxxx",
		RunE: func(cmd *cobra.Command, args []string) error {
			token := args[0]
			if token == "" {
				return fmt.Errorf("token cannot be empty")
			}

			creds := &config.Credentials{GHToken: token}
			if err := config.SaveCredentials(creds); err != nil {
				return fmt.Errorf("failed to save token: %w", err)
			}

			// Also set in current process environment so immediate commands work
			os.Setenv("GH_TOKEN", token)

			fmt.Printf("GitHub token saved to %s\n", config.CredentialsPath())
			fmt.Println("Token is loaded automatically on every clawflow run.")
			return nil
		},
	}
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show current ClawFlow configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			creds, _ := config.LoadCredentials()

			fmt.Printf("Config file:  %s\n", config.ConfigPath())
			fmt.Printf("Credentials:  %s\n", config.CredentialsPath())

			tokenStatus := "not set"
			if creds != nil && creds.GHToken != "" {
				tokenStatus = "set (***" + creds.GHToken[max(0, len(creds.GHToken)-4):] + ")"
			}
			fmt.Printf("GH Token:     %s\n", tokenStatus)
			fmt.Println()

			fmt.Printf("Settings:\n")
			fmt.Printf("  poll_interval:        %d min\n", cfg.Settings.PollInterval)
			fmt.Printf("  confidence_threshold: %d/10\n", cfg.Settings.ConfidenceThreshold)
			fmt.Printf("  agent_timeout:        %d sec\n", cfg.Settings.AgentTimeout)
			fmt.Printf("  max_concurrent:       %d\n", cfg.Settings.MaxConcurrentAgents)
			fmt.Println()

			fmt.Printf("Repos (%d configured):\n", len(cfg.Repos))
			for name, r := range cfg.Repos {
				status := "disabled"
				if r.Enabled {
					status = "enabled"
				}
				fmt.Printf("  %-40s [%s]\n", name, status)
			}
			return nil
		},
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
