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
	cmd.AddCommand(newConfigSetCmd())
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func newConfigSetCmd() *cobra.Command {
	var saasURL, token string

	cmd := &cobra.Command{
		Use:   "set",
		Short: "Manually configure SaaS URL and worker token",
		Long: `Writes saas_url and worker_token to ~/.clawflow/config/worker.yaml.
Use this instead of 'clawflow login' when you already have a worker token.`,
		Example: "  clawflow config set --saas-url https://clawflow.daboluo.cc --token cfw_xxx",
		RunE: func(cmd *cobra.Command, args []string) error {
			if saasURL == "" && token == "" {
				return fmt.Errorf("at least one of --saas-url or --token is required")
			}
			wc, err := config.LoadWorkerConfig()
			if err != nil {
				return fmt.Errorf("load worker config: %w", err)
			}
			if saasURL != "" {
				wc.SaasURL = saasURL
			}
			if token != "" {
				wc.WorkerToken = token
			}
			if err := wc.Save(); err != nil {
				return fmt.Errorf("save worker config: %w", err)
			}
			fmt.Printf("Worker config saved to ~/.clawflow/config/worker.yaml\n")
			if wc.SaasURL != "" {
				fmt.Printf("  saas_url:     %s\n", wc.SaasURL)
			}
			if wc.WorkerToken != "" {
				n := len(wc.WorkerToken)
				fmt.Printf("  worker_token: %s***\n", wc.WorkerToken[:max(0, n-4)])
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&saasURL, "saas-url", "", "ClawFlow SaaS base URL")
	cmd.Flags().StringVar(&token, "token", "", "Worker token (cfw_xxx)")
	return cmd
}
