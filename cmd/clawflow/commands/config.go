package commands

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func NewConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage ClawFlow configuration",
	}
	cmd.AddCommand(newConfigSetTokenCmd())
	cmd.AddCommand(newConfigSetGitLabTokenCmd())
	cmd.AddCommand(newConfigShowCmd())
	return cmd
}

func newConfigSetTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-token <gh-token>",
		Short: "Store GitHub token in ~/.clawflow/config/credentials.yaml",
		Long: `Saves the GitHub personal access token to ~/.clawflow/config/credentials.yaml (mode 0600).

Required scopes: repo (full), read:org`,
		Args:    cobra.ExactArgs(1),
		Example: "  clawflow config set-token ghp_xxxxxxxxxxxx",
		RunE: func(cmd *cobra.Command, args []string) error {
			return setToken(func(c *config.Credentials, v string) { c.GHToken = v }, args[0], "GitHub")
		},
	}
}

func newConfigSetGitLabTokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set-gitlab-token <token>",
		Short: "Store GitLab token in ~/.clawflow/config/credentials.yaml",
		Long: `Saves the GitLab personal access token to ~/.clawflow/config/credentials.yaml (mode 0600).

Required scopes: api`,
		Args:    cobra.ExactArgs(1),
		Example: "  clawflow config set-gitlab-token glpat-xxxxxxxxxxxx",
		RunE: func(cmd *cobra.Command, args []string) error {
			return setToken(func(c *config.Credentials, v string) { c.GitLabToken = v }, args[0], "GitLab")
		},
	}
}

func setToken(apply func(*config.Credentials, string), token, platform string) error {
	if token == "" {
		return fmt.Errorf("token cannot be empty")
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		return err
	}
	if creds == nil {
		creds = &config.Credentials{}
	}
	apply(creds, token)
	if err := config.SaveCredentials(creds); err != nil {
		return fmt.Errorf("failed to save token: %w", err)
	}
	fmt.Printf("%s token saved to %s\n", platform, config.CredentialsPath())
	return nil
}

func newConfigShowCmd() *cobra.Command {
	var repoFlag, fieldFlag string

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show current ClawFlow configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			// --repo + --field: print a single repo field value (for scripting)
			if repoFlag != "" && fieldFlag != "" {
				r, ok := cfg.Repos[repoFlag]
				if !ok {
					return fmt.Errorf("repo %q not found", repoFlag)
				}
				switch fieldFlag {
				case "auto_fix":
					fmt.Println(r.AutoFix)
				case "auto_merge":
					fmt.Println(r.AutoMerge)
				case "enabled":
					fmt.Println(r.Enabled)
				case "base_branch":
					fmt.Println(r.BaseBranch)
				case "platform":
					fmt.Println(r.Platform)
				case "local_path":
					fmt.Println(r.LocalPath)
				case "ci_required":
					fmt.Println(r.CIRequired)
				default:
					return fmt.Errorf("unknown field %q", fieldFlag)
				}
				return nil
			}

			creds, _ := config.LoadCredentials()

			fmt.Printf("Config file:  %s\n", config.ConfigPath())
			fmt.Printf("Credentials:  %s\n", config.CredentialsPath())

			tokenStatus := "not set"
			if creds != nil && creds.GHToken != "" {
				tokenStatus = "set (***" + creds.GHToken[max(0, len(creds.GHToken)-4):] + ")"
			}
			fmt.Printf("GH Token:     %s\n", tokenStatus)

			glTokenStatus := "not set"
			if creds != nil && creds.GitLabToken != "" {
				glTokenStatus = "set (***" + creds.GitLabToken[max(0, len(creds.GitLabToken)-4):] + ")"
			}
			fmt.Printf("GitLab Token: %s\n", glTokenStatus)
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

	cmd.Flags().StringVar(&repoFlag, "repo", "", "Repo to query (owner/repo)")
	cmd.Flags().StringVar(&fieldFlag, "field", "", "Single field to print (auto_fix, auto_merge, enabled, ...)")
	return cmd
}

