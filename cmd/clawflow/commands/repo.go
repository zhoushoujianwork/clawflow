package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func NewRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Manage monitored repositories",
	}
	cmd.AddCommand(newRepoListCmd())
	cmd.AddCommand(newRepoAddCmd())
	cmd.AddCommand(newRepoRemoveCmd())
	cmd.AddCommand(newRepoEnableCmd())
	cmd.AddCommand(newRepoDisableCmd())
	return cmd
}

func newRepoListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List all configured repositories",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if len(cfg.Repos) == 0 {
				fmt.Println("No repositories configured.")
				fmt.Println("Add one with: clawflow repo add <owner/repo>")
				return nil
			}
			fmt.Printf("%-40s %-10s %-12s %s\n", "REPO", "STATUS", "BASE", "DESCRIPTION")
			fmt.Println(strings.Repeat("─", 80))
			for name, r := range cfg.Repos {
				status := "disabled"
				if r.Enabled {
					status = "enabled"
				}
				fmt.Printf("%-40s %-10s %-12s %s\n", name, status, r.BaseBranch, r.Description)
			}
			return nil
		},
	}
}

func newRepoAddCmd() *cobra.Command {
	var baseBranch  string
	var localPath   string
	var description string

	cmd := &cobra.Command{
		Use:     "add <owner/repo>",
		Short:   "Add a repository to monitor",
		Args:    cobra.ExactArgs(1),
		Example: "  clawflow repo add zhoushoujianwork/llm-wiki --local-path ~/github/llm-wiki",
		RunE: func(cmd *cobra.Command, args []string) error {
			ownerRepo := args[0]
			if !strings.Contains(ownerRepo, "/") {
				return fmt.Errorf("repo must be in owner/repo format")
			}
			parts := strings.SplitN(ownerRepo, "/", 2)

			cfg, err := loadOrNewConfig()
			if err != nil {
				return err
			}
			if _, exists := cfg.Repos[ownerRepo]; exists {
				return fmt.Errorf("repo %q already configured — use enable/disable to change status", ownerRepo)
			}

			cfg.Repos[ownerRepo] = config.Repo{
				Enabled:    true,
				BaseBranch: baseBranch,
				LocalPath:  localPath,
				Owner:      parts[0],
				Description: description,
				AddedAt:    time.Now().Format("2006-01-02"),
				Labels: map[string]string{
					"trigger":     "ready-for-agent",
					"in_progress": "in-progress",
					"bug":         "bug",
					"enhancement": "enhancement",
					"help_wanted": "help-wanted",
				},
			}

			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Printf("repo %q added and enabled\n", ownerRepo)
			fmt.Printf("  base branch: %s\n", baseBranch)
			if localPath != "" {
				fmt.Printf("  local path:  %s\n", localPath)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&baseBranch,  "base",        "main", "base branch for PRs")
	cmd.Flags().StringVar(&localPath,   "local-path",  "",     "local clone path (for worktree)")
	cmd.Flags().StringVar(&description, "description", "",     "short description")
	return cmd
}

func newRepoRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <owner/repo>",
		Short:   "Remove a repository from config",
		Aliases: []string{"rm"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ownerRepo := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if _, exists := cfg.Repos[ownerRepo]; !exists {
				return fmt.Errorf("repo %q not found", ownerRepo)
			}
			delete(cfg.Repos, ownerRepo)
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Printf("repo %q removed\n", ownerRepo)
			return nil
		},
	}
}

func newRepoEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <owner/repo>",
		Short: "Enable a repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setRepoEnabled(args[0], true)
		},
	}
}

func newRepoDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <owner/repo>",
		Short: "Disable a repository (stop monitoring without removing)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setRepoEnabled(args[0], false)
		},
	}
}

func setRepoEnabled(ownerRepo string, enabled bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	r, exists := cfg.Repos[ownerRepo]
	if !exists {
		return fmt.Errorf("repo %q not found", ownerRepo)
	}
	r.Enabled = enabled
	cfg.Repos[ownerRepo] = r
	if err := cfg.Save(); err != nil {
		return err
	}
	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	fmt.Printf("repo %q %s\n", ownerRepo, state)
	return nil
}

// loadOrNewConfig loads existing config or creates an empty one.
func loadOrNewConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		// If file doesn't exist yet, start fresh
		cfg = &config.Config{
			Repos: make(map[string]config.Repo),
		}
	}
	if cfg.Repos == nil {
		cfg.Repos = make(map[string]config.Repo)
	}
	return cfg, nil
}
