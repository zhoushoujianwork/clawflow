package commands

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/githubauth"
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
	for _, sub := range cmd.Commands() {
		switch sub.Name() {
		case "list", "add", "enable", "disable", "set":
			routePlatformRepo(sub)
		}
	}
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
				fmt.Fprintln(cmd.OutOrStdout(), "No repositories configured.")
				fmt.Fprintln(cmd.OutOrStdout(), "Add one with: clawflow repo add <owner/repo>")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%-40s %-10s %-12s %-10s %s\n", "REPO", "STATUS", "BASE", "AUTO_MERGE", "DESCRIPTION")
			fmt.Fprintln(cmd.OutOrStdout(), strings.Repeat("─", 100))
			for name, r := range cfg.Repos {
				status := "disabled"
				if r.Enabled {
					status = "enabled"
				}
				autoMerge := "off"
				if r.AutoMerge {
					autoMerge = "on"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-40s %-10s %-12s %-10s %s\n", name, status, r.BaseBranch, autoMerge, r.Description)
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
			if r.Platform == "" || r.Platform == "github" {
				creds, err := config.LoadCredentials()
				if err != nil {
					return err
				}
				identity, err := githubauth.Resolve(cmd.Context(), ownerRepo, creds.GHToken, r.BaseURL)
				if err != nil {
					return err
				}
				fmt.Printf("github_identity: %s\npermissions: %v\nrepository_selection: %s\n", identity.Name, identity.Permissions, identity.Selection)
			}
			status := "disabled"
			if r.Enabled {
				status = "enabled"
			}
			fmt.Printf("repo:         %s\n", ownerRepo)
			fmt.Printf("status:       %s\n", status)
			fmt.Printf("ci_required:  %v\n", r.CIRequired)
			fmt.Printf("acceptance_required: %v\n", r.AcceptanceRequired)
			fmt.Printf("acceptance_max_parallel: %d\n", r.AcceptanceParallelLimit())
			fmt.Printf("self_upgrade: %v\n", r.SelfUpgrade)
			fmt.Printf("worker_tags:  %v\n", r.WorkerTags)
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
	var baseBranch string
	var localPath string
	var description string
	var platform string
	var baseURL string
	var disabled, skipLabelInit bool

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

			if localPath != "" {
				localPath, err = validateRepoCheckout(localPath, info)
				if err != nil {
					return err
				}
			}
			ownerRepo := info.OwnerRepo
			parts := strings.SplitN(ownerRepo, "/", 2)

			if _, exists := cfg.Repos[ownerRepo]; exists {
				return fmt.Errorf("repo %q already configured — use enable/disable to change status", ownerRepo)
			}

			cfg.Repos[ownerRepo] = config.Repo{
				Enabled:     !disabled,
				Platform:    info.Platform,
				BaseURL:     info.BaseURL,
				BaseBranch:  baseBranch,
				LocalPath:   localPath,
				Owner:       parts[0],
				Description: description,
				AddedAt:     time.Now().Format("2006-01-02"),
			}

			// Stamp the new entry with the current time so LWW sync
			// knows this machine created it.
			config.TouchRepo(cfg, ownerRepo, func(r config.Repo) config.Repo { return r })

			if err := cfg.Save(); err != nil {
				return err
			}
			if err := snapshot.WriteRepos(cfg); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "  [warn] failed to update dashboard: %v\n", err)
			}
			if disabled {
				fmt.Fprintf(cmd.OutOrStdout(), "repo %q added and disabled\n", ownerRepo)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "repo %q added and enabled\n", ownerRepo)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  platform:    %s\n", info.Platform)
			if info.BaseURL != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  base url:    %s\n", info.BaseURL)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  base branch: %s\n", baseBranch)
			if localPath != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  local path:  %s\n", localPath)
			}
			if disabled || skipLabelInit {
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Initializing ClawFlow labels in %s ...\n", ownerRepo)
			repoCfg := cfg.Repos[ownerRepo]
			client, err := newVCSClient(repoCfg)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "  [warn] label init failed: %v\n", err)
			} else if err := client.InitLabels(ownerRepo, vcs.ClawFlowLabels); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "  [warn] label init failed: %v\n", err)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&disabled, "disabled", false, "Register disabled; do not initialize remote labels")
	cmd.Flags().BoolVar(&skipLabelInit, "skip-label-init", false, "Skip remote label initialization")
	cmd.Flags().StringVar(&baseBranch, "base", "main", "base branch for PRs")
	cmd.Flags().StringVar(&localPath, "local-path", "", "local clone path (for worktree)")
	cmd.Flags().StringVar(&description, "description", "", "short description")
	cmd.Flags().StringVar(&platform, "platform", "", "override platform: github or gitlab")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "override instance URL for self-hosted GitLab")
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
			return setRepoEnabled(cmd, args[0], true)
		},
	}
}

func newRepoDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <owner/repo>",
		Short: "Disable a repository (stop monitoring without removing)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setRepoEnabled(cmd, args[0], false)
		},
	}
}

func setRepoEnabled(cmd *cobra.Command, ownerRepo string, enabled bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if enabled {
		if err := cfg.Settings.SchedulingError(); err != nil {
			return err
		}
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
		fmt.Fprintf(cmd.OutOrStdout(), "  [warn] failed to update dashboard: %v\n", err)
	}
	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "repo %q %s\n", ownerRepo, state)
	return nil
}

func newRepoSetCmd() *cobra.Command {
	var acceptanceRequired string
	var acceptanceOverlapCI string
	var ciRequired string
	var autoMerge string
	var selfUpgrade string
	var primaryProject string
	var boundMachine string
	var maxInFlight int
	var acceptanceMaxParallel int
	var testCommand string

	cmd := &cobra.Command{
		Use:     "set <owner/repo>",
		Short:   "Set configuration flags for a repository",
		Args:    cobra.ExactArgs(1),
		Example: "  clawflow repo set owner/repo --auto-merge on\n  clawflow repo set owner/repo --primary-project myproj",
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
			if cmd.Flags().Changed("acceptance-overlap-ci") && acceptanceOverlapCI != "on" && acceptanceOverlapCI != "off" {
				return fmt.Errorf("--acceptance-overlap-ci must be on or off")
			}
			if cmd.Flags().Changed("acceptance-required") && acceptanceRequired != "on" && acceptanceRequired != "off" {
				return fmt.Errorf("--acceptance-required must be on or off")
			}
			if cmd.Flags().Changed("ci-required") && ciRequired != "on" && ciRequired != "off" {
				return fmt.Errorf("--ci-required must be on or off")
			}
			if cmd.Flags().Changed("self-upgrade") && selfUpgrade != "on" && selfUpgrade != "off" {
				return fmt.Errorf("--self-upgrade must be on or off")
			}
			if cmd.Flags().Changed("acceptance-max-parallel") && acceptanceMaxParallel < 1 {
				return fmt.Errorf("--acceptance-max-parallel must be at least 1")
			}
			maxInFlightSet := cmd.Flags().Changed("max-in-flight")
			if maxInFlightSet && maxInFlight < 1 {
				return fmt.Errorf("--max-in-flight must be >= 1")
			}
			boundMachineSet := cmd.Flags().Changed("bound-machine")
			if boundMachineSet && strings.ContainsFunc(boundMachine, func(r rune) bool {
				return unicode.IsSpace(r) || unicode.IsControl(r) || r == '/' || r == '\\'
			}) {
				return fmt.Errorf("--bound-machine must not contain whitespace, control characters, or slashes")
			}
			if autoMerge != "" {
				switch autoMerge {
				case "on", "true", "1", "off", "false", "0":
				default:
					return fmt.Errorf("--auto-merge must be on or off")
				}
			}
			config.TouchRepo(cfg, ownerRepo, func(r config.Repo) config.Repo {
				if cmd.Flags().Changed("acceptance-overlap-ci") {
					v := acceptanceOverlapCI == "on"
					r.AcceptanceOverlapCI = &v
				}
				if cmd.Flags().Changed("acceptance-required") {
					r.AcceptanceRequired = acceptanceRequired == "on"
				}
				if ciRequired != "" {
					r.CIRequired = ciRequired == "on"
				}
				if autoMerge != "" {
					r.AutoMerge = autoMerge == "on" || autoMerge == "true" || autoMerge == "1"
				}
				if cmd.Flags().Changed("self-upgrade") {
					r.SelfUpgrade = selfUpgrade == "on"
				}
				if primaryProjectSet {
					r.PrimaryProject = primaryProject
				}
				if boundMachineSet {
					r = r.WithHostTag(boundMachine)
				}
				if cmd.Flags().Changed("acceptance-max-parallel") {
					r.AcceptanceMaxParallel = acceptanceMaxParallel
				}
				if cmd.Flags().Changed("test-command") {
					r.TestCommand = testCommand
				}
				if maxInFlightSet {
					r.MaxInFlight = maxInFlight
				}
				return r
			})
			r := cfg.Repos[ownerRepo]
			if err := cfg.Save(); err != nil {
				return err
			}
			if err := snapshot.WriteRepos(cfg); err != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "  [warn] failed to update dashboard: %v\n", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "repo %q updated\n", ownerRepo)
			fmt.Fprintf(cmd.OutOrStdout(), "  ci_required:  %v\n", r.CIRequired)
			fmt.Fprintf(cmd.OutOrStdout(), "  auto_merge:   %v\n", r.AutoMerge)
			fmt.Fprintf(cmd.OutOrStdout(), "  acceptance_max_parallel: %d\n", r.AcceptanceParallelLimit())
			fmt.Fprintf(cmd.OutOrStdout(), "  acceptance_required: %v\n", r.AcceptanceRequired)
			fmt.Fprintf(cmd.OutOrStdout(), "  acceptance_overlap_ci: %v\n", r.AcceptanceOverlapsCI())
			fmt.Fprintf(cmd.OutOrStdout(), "  self_upgrade: %v\n", r.SelfUpgrade)
			fmt.Fprintf(cmd.OutOrStdout(), "  worker_tags: %v\n", r.WorkerTags)
			if maxInFlightSet {
				fmt.Fprintf(cmd.OutOrStdout(), "  max_in_flight: %d\n", r.MaxInFlight)
			}
			if primaryProjectSet {
				if r.PrimaryProject == "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  primary_project: (cleared)\n")
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "  primary_project: %s\n", r.PrimaryProject)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&testCommand, "test-command", "", "repository test command (empty clears it)")
	cmd.Flags().StringVar(&ciRequired, "ci-required", "", "require successful CI before auto-merge: on or off")
	cmd.Flags().StringVar(&acceptanceOverlapCI, "acceptance-overlap-ci", "", "start acceptance during pending CI: on or off (default on when CI is required)")
	cmd.Flags().StringVar(&acceptanceRequired, "acceptance-required", "", "require current independent acceptance before auto-merge: on or off")
	cmd.Flags().StringVar(&autoMerge, "auto-merge", "", "enable/disable auto-merge: on or off")
	cmd.Flags().StringVar(&selfUpgrade, "self-upgrade", "", "rebuild and replace the installed binary after a confirmed merge into this repo (only for the repo containing clawflow's source): on or off")
	cmd.Flags().StringVar(&primaryProject, "primary-project", "", "authoritative project for context injection when the repo belongs to multiple projects (empty clears it)")
	cmd.Flags().IntVar(&acceptanceMaxParallel, "acceptance-max-parallel", 3, "max concurrent independent reviews for this repository (>=1)")
	cmd.Flags().IntVar(&maxInFlight, "max-in-flight", 0, "max issues of this repo running or awaiting review at once in the unified queue (>=1, default 2)")
	cmd.Flags().StringVar(&boundMachine, "bound-machine", "", "machine hostname allowed to process this repository (empty clears it)")
	_ = cmd.Flags().MarkDeprecated("bound-machine", "use worker_tags with host-<hostname> instead")
	return cmd
}

// loadOrNewConfig loads existing config or creates an empty one.
func loadOrNewConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		// Only a missing config initializes a new one.
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

// validateRepoCheckout checks the local origin without contacting the remote.
func validateRepoCheckout(path string, target config.RepoInfo) (string, error) {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[2:])
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if out, err := exec.Command("git", "-C", abs, "rev-parse", "--is-inside-work-tree").Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		return "", fmt.Errorf("local path %q is not a Git checkout", abs)
	}
	out, err := exec.Command("git", "-C", abs, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", fmt.Errorf("local checkout has no origin")
	}
	remote := strings.TrimSpace(string(out))
	if strings.HasPrefix(remote, "git@") {
		parts := strings.SplitN(remote, ":", 2)
		if len(parts) != 2 || !strings.Contains(parts[1], "/") {
			return "", fmt.Errorf("invalid checkout origin")
		}
	}
	origin, err := config.ParseRepoInput(remote, nil)
	if err != nil {
		return "", fmt.Errorf("cannot parse checkout origin")
	}
	host := func(info config.RepoInfo) string {
		if info.Platform == "github" {
			return "github.com"
		}
		u, _ := url.Parse(info.BaseURL)
		if u == nil {
			return ""
		}
		return strings.ToLower(u.Hostname())
	}
	if origin.Platform != target.Platform || host(origin) != host(target) || origin.OwnerRepo != target.OwnerRepo {
		return "", fmt.Errorf("checkout origin does not match target provider, host and namespace")
	}
	return abs, nil
}
