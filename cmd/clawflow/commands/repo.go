package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func NewRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Manage monitored repositories",
	}
	cmd.AddCommand(newRepoListCmd())
	cmd.AddCommand(newRepoShowCmd())
	cmd.AddCommand(newRepoAddCmd())
	cmd.AddCommand(newRepoRemoveCmd())
	cmd.AddCommand(newRepoEnableCmd())
	cmd.AddCommand(newRepoDisableCmd())
	cmd.AddCommand(newRepoSetCmd())
	cmd.AddCommand(newRepoEnsureLocalCmd())
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
				autoApprove := "off"
				if r.AutoApprove {
					autoApprove = "on"
				}
				autoMerge := "off"
				if r.AutoMerge {
					autoMerge = "on"
				}
				fmt.Printf("%-40s %-10s %-12s %-10s %-10s %s\n", name, status, r.BaseBranch, autoApprove, autoMerge, r.Description)
			}
			return nil
		},
	}
}

func newRepoShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <owner/repo>",
		Short: "Show details for a repository, including the projects it belongs to",
		Args:  cobra.ExactArgs(1),
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
			status := "disabled"
			if r.Enabled {
				status = "enabled"
			}
			fmt.Printf("repo:         %s\n", ownerRepo)
			fmt.Printf("status:       %s\n", status)
			fmt.Printf("base_branch:  %s\n", r.BaseBranch)
			fmt.Printf("local_path:   %s\n", r.LocalPath)
			if r.Description != "" {
				fmt.Printf("description:  %s\n", r.Description)
			}

			// Multi-membership view (issue #267): list every project that
			// claims this repo, and mark which one is authoritative for the
			// project-context header injected into operators/chat.
			projects, err := project.FindProjectsByRepo(ownerRepo)
			if err != nil {
				return err
			}
			if len(projects) == 0 {
				fmt.Println("projects:     (none)")
				return nil
			}
			primary, _ := project.FindProjectByRepo(ownerRepo)
			primaryName := ""
			if primary != nil {
				primaryName = primary.Name
			}
			names := make([]string, 0, len(projects))
			for _, p := range projects {
				if p.Name == primaryName {
					names = append(names, p.Name+" (primary)")
				} else {
					names = append(names, p.Name)
				}
			}
			fmt.Printf("projects:     %s\n", strings.Join(names, ", "))
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

			// Stamp the new entry with the current time so LWW sync
			// knows this machine created it.
			config.TouchRepo(cfg, ownerRepo, func(r config.Repo) config.Repo { return r })

			if err := cfg.Save(); err != nil {
				return err
			}
			if err := snapshot.WriteRepos(cfg); err != nil {
				fmt.Printf("  [warn] failed to update dashboard: %v\n", err)
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
			if err := snapshot.WriteRepos(cfg); err != nil {
				fmt.Printf("  [warn] failed to update dashboard: %v\n", err)
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
	if _, exists := cfg.Repos[ownerRepo]; !exists {
		return fmt.Errorf("repo %q not found", ownerRepo)
	}
	config.TouchRepo(cfg, ownerRepo, func(r config.Repo) config.Repo {
		r.Enabled = enabled
		return r
	})
	if err := cfg.Save(); err != nil {
		return err
	}
	if err := snapshot.WriteRepos(cfg); err != nil {
		fmt.Printf("  [warn] failed to update dashboard: %v\n", err)
	}
	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	fmt.Printf("repo %q %s\n", ownerRepo, state)
	return nil
}

func newRepoSetCmd() *cobra.Command {
	var autoApprove string
	var autoMerge string
	var primaryProject string

	cmd := &cobra.Command{
		Use:     "set <owner/repo>",
		Short:   "Set configuration flags for a repository",
		Args:    cobra.ExactArgs(1),
		Example: "  clawflow repo set owner/repo --auto-approve on\n  clawflow repo set owner/repo --auto-merge on\n  clawflow repo set owner/repo --primary-project myproj",
		RunE: func(cmd *cobra.Command, args []string) error {
			ownerRepo := args[0]
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if _, exists := cfg.Repos[ownerRepo]; !exists {
				return fmt.Errorf("repo %q not found", ownerRepo)
			}
			// --primary-project chooses the authoritative project whose
			// context header is injected into operators/chat when the repo
			// belongs to several projects (issue #267). Pass "" to clear it
			// (falls back to the lexicographically first project). When set,
			// the named project must already contain this repo.
			primaryProjectSet := cmd.Flags().Changed("primary-project")
			if primaryProjectSet && primaryProject != "" {
				members, err := project.FindProjectsByRepo(ownerRepo)
				if err != nil {
					return err
				}
				ok := false
				for _, p := range members {
					if p.Name == primaryProject {
						ok = true
						break
					}
				}
				if !ok {
					return fmt.Errorf("project %q does not contain repo %q (add it first with: clawflow project add-repo %s %s)", primaryProject, ownerRepo, primaryProject, ownerRepo)
				}
			}
			// Validate flags before mutating.
			if autoApprove != "" {
				switch autoApprove {
				case "on", "true", "1", "off", "false", "0":
				default:
					return fmt.Errorf("--auto-approve must be on or off")
				}
			}
			if autoMerge != "" {
				switch autoMerge {
				case "on", "true", "1", "off", "false", "0":
				default:
					return fmt.Errorf("--auto-merge must be on or off")
				}
			}
			config.TouchRepo(cfg, ownerRepo, func(r config.Repo) config.Repo {
				if autoApprove != "" {
					switch autoApprove {
					case "on", "true", "1":
						r.AutoApprove = true
					case "off", "false", "0":
						r.AutoApprove = false
					}
				}
				if autoMerge != "" {
					switch autoMerge {
					case "on", "true", "1":
						r.AutoMerge = true
					case "off", "false", "0":
						r.AutoMerge = false
					}
				}
				if primaryProjectSet {
					r.PrimaryProject = primaryProject
				}
				return r
			})
			r := cfg.Repos[ownerRepo]
			if err := cfg.Save(); err != nil {
				return err
			}
			if err := snapshot.WriteRepos(cfg); err != nil {
				fmt.Printf("  [warn] failed to update dashboard: %v\n", err)
			}
			fmt.Printf("repo %q updated\n", ownerRepo)
			fmt.Printf("  auto_approve: %v\n", r.AutoApprove)
			fmt.Printf("  auto_merge:   %v\n", r.AutoMerge)
			if primaryProjectSet {
				if r.PrimaryProject == "" {
					fmt.Printf("  primary_project: (cleared)\n")
				} else {
					fmt.Printf("  primary_project: %s\n", r.PrimaryProject)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&autoApprove, "auto-approve", "", "enable/disable auto-approve: on or off")
	cmd.Flags().StringVar(&autoMerge, "auto-merge", "", "enable/disable auto-merge: on or off")
	cmd.Flags().StringVar(&primaryProject, "primary-project", "", "authoritative project for context injection when the repo belongs to multiple projects (empty clears it)")
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


func newRepoEnsureLocalCmd() *cobra.Command {
	var repo string

	cmd := &cobra.Command{
		Use:     "ensure-local",
		Short:   "Ensure a repo has a local clone, auto-cloning if needed",
		Example: "  clawflow repo ensure-local --repo owner/repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			repoCfg, ok := cfg.Repos[repo]
			if !ok {
				return fmt.Errorf("repo %q not found in config", repo)
			}
			localPath, err := resolveLocalPath(cfg, repo, repoCfg)
			if err != nil {
				return err
			}
			fmt.Println(localPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "owner/repo (required)")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}
