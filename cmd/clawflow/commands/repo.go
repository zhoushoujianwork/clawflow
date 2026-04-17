package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
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
	cmd.AddCommand(newRepoSetCmd())
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
			fmt.Printf("%-40s %-10s %-12s %-10s %-10s %s\n", "REPO", "STATUS", "BASE", "AUTO_FIX", "AUTO_MERGE", "DESCRIPTION")
			fmt.Println(strings.Repeat("─", 100))
			for name, r := range cfg.Repos {
				status := "disabled"
				if r.Enabled {
					status = "enabled"
				}
				autoFix := "off"
				if r.AutoFix {
					autoFix = "on"
				}
				autoMerge := "off"
				if r.AutoMerge {
					autoMerge = "on"
				}
				fmt.Printf("%-40s %-10s %-12s %-10s %-10s %s\n", name, status, r.BaseBranch, autoFix, autoMerge, r.Description)
			}
			return nil
		},
	}
}

func newRepoAddCmd() *cobra.Command {
	var baseBranch  string
	var localPath   string
	var description string
	var platform    string
	var baseURL     string

	cmd := &cobra.Command{
		Use:     "add <owner/repo|URL>",
		Short:   "Add a repository to monitor",
		Args:    cobra.ExactArgs(1),
		Example: "  clawflow repo add owner/repo\n  clawflow repo add https://github.com/owner/repo\n  clawflow repo add https://gitlab.company.com/ns/repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadOrNewConfig()
			if err != nil {
				return err
			}

			info, err := config.ParseRepoInput(args[0], cfg.Settings.GitLabHosts)
			if err != nil {
				return err
			}

			// manual flags override auto-detected values
			if platform != "" {
				info.Platform = platform
			}
			if baseURL != "" {
				info.BaseURL = baseURL
			}
			if info.Platform == "gitlab" && info.BaseURL == "" {
				return fmt.Errorf("cannot determine GitLab instance URL — pass --base-url or add the host to settings.gitlab_hosts")
			}

			// local path: from flag, or auto-detected from .git/config
			if localPath == "" {
				localPath = info.LocalPath
			}

			ownerRepo := info.OwnerRepo
			parts := strings.SplitN(ownerRepo, "/", 2)

			if _, exists := cfg.Repos[ownerRepo]; exists {
				return fmt.Errorf("repo %q already configured — use enable/disable to change status", ownerRepo)
			}

			cfg.Repos[ownerRepo] = config.Repo{
				Enabled:     true,
				Platform:    info.Platform,
				BaseURL:     info.BaseURL,
				BaseBranch:  baseBranch,
				LocalPath:   localPath,
				Owner:       parts[0],
				Description: description,
				AddedAt:     time.Now().Format("2006-01-02"),
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
			fmt.Printf("  platform:    %s\n", info.Platform)
			if info.BaseURL != "" {
				fmt.Printf("  base url:    %s\n", info.BaseURL)
			}
			fmt.Printf("  base branch: %s\n", baseBranch)
			if localPath != "" {
				fmt.Printf("  local path:  %s\n", localPath)
			}
			fmt.Printf("Initializing ClawFlow labels in %s ...\n", ownerRepo)
			repoCfg := cfg.Repos[ownerRepo]
			client, err := newVCSClient(repoCfg)
			if err != nil {
				fmt.Printf("  [warn] label init failed: %v\n", err)
			} else if err := client.InitLabels(ownerRepo, vcs.ClawFlowLabels); err != nil {
				fmt.Printf("  [warn] label init failed: %v\n", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&baseBranch,  "base",        "main", "base branch for PRs")
	cmd.Flags().StringVar(&localPath,   "local-path",  "",     "local clone path (for worktree)")
	cmd.Flags().StringVar(&description, "description", "",     "short description")
	cmd.Flags().StringVar(&platform,    "platform",    "",     "override platform: github or gitlab")
	cmd.Flags().StringVar(&baseURL,     "base-url",    "",     "override instance URL for self-hosted GitLab")
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

func newRepoSetCmd() *cobra.Command {
	var autoFix string
	var autoMerge string

	cmd := &cobra.Command{
		Use:     "set <owner/repo>",
		Short:   "Set configuration flags for a repository",
		Args:    cobra.ExactArgs(1),
		Example: "  clawflow repo set owner/repo --auto-fix on\n  clawflow repo set owner/repo --auto-merge on",
		RunE: func(cmd *cobra.Command, args []string) error {
			ownerRepo := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			r, exists := cfg.Repos[ownerRepo]
			if !exists {
				return fmt.Errorf("repo %q not found", ownerRepo)
			}
			if autoFix != "" {
				switch autoFix {
				case "on", "true", "1":
					r.AutoFix = true
				case "off", "false", "0":
					r.AutoFix = false
				default:
					return fmt.Errorf("--auto-fix must be on or off")
				}
			}
			if autoMerge != "" {
				switch autoMerge {
				case "on", "true", "1":
					r.AutoMerge = true
				case "off", "false", "0":
					r.AutoMerge = false
				default:
					return fmt.Errorf("--auto-merge must be on or off")
				}
			}
			cfg.Repos[ownerRepo] = r
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Printf("repo %q updated\n", ownerRepo)
			fmt.Printf("  auto_fix:   %v\n", r.AutoFix)
			fmt.Printf("  auto_merge: %v\n", r.AutoMerge)
			return nil
		},
	}
	cmd.Flags().StringVar(&autoFix, "auto-fix", "", "enable/disable auto-fix: on or off")
	cmd.Flags().StringVar(&autoMerge, "auto-merge", "", "enable/disable auto-merge: on or off")
	return cmd
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

// normalizeRepo converts GitHub URLs to owner/repo format.
// e.g. https://github.com/owner/repo.git → owner/repo
func normalizeRepo(input string) string {
	s := strings.TrimSuffix(strings.TrimSpace(input), ".git")
	for _, prefix := range []string{"https://github.com/", "http://github.com/", "git@github.com:"} {
		if strings.HasPrefix(s, prefix) {
			return strings.TrimPrefix(s, prefix)
		}
	}
	return s
}
