package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	rootmod "github.com/zhoushoujianwork/clawflow"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
)

// NewRunCmd wires `clawflow run`: one pass of the operator loop over every
// enabled repo (or a single repo / issue if flags are set). Schedule via cron
// or invoke ad-hoc; the CLI holds no long-running state.
func NewRunCmd() *cobra.Command {
	var (
		onlyRepo  string
		onlyIssue int
		timeout   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Scan configured repos and run matching operators",
		Long: `Execute one pass of the operator loop:
  - for each enabled repo, list open issues
  - for each issue, match against registered operators
  - on first match: add lock label, run claude -p, post result as a comment, remove lock

At most one operator runs per issue per invocation. Pass --repo and/or --issue
to narrow the scan.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if onlyIssue != 0 && onlyRepo == "" {
				return fmt.Errorf("--issue requires --repo")
			}
			return runOnce(cmd.Context(), onlyRepo, onlyIssue, timeout)
		},
	}
	cmd.Flags().StringVar(&onlyRepo, "repo", "", "Restrict to a single repo (owner/repo); default: all enabled repos")
	cmd.Flags().IntVar(&onlyIssue, "issue", 0, "Restrict to a single issue number (requires --repo)")
	cmd.Flags().DurationVar(&timeout, "timeout", 60*time.Minute, "Per-operator claude subprocess timeout")
	return cmd
}

// loadRegistry builds a Registry from the embedded skills + the user's
// ~/.clawflow/skills directory. User operators override built-ins with the
// same name.
func loadRegistry() (*operator.Registry, error) {
	reg := operator.NewRegistry()
	if err := reg.LoadEmbedded(rootmod.EmbeddedSkills, "skills"); err != nil {
		return nil, fmt.Errorf("load embedded operators: %w", err)
	}
	home, _ := os.UserHomeDir()
	userDir := filepath.Join(home, ".clawflow", "skills")
	if err := reg.LoadUserDir(userDir); err != nil {
		return nil, fmt.Errorf("load user operators from %s: %w", userDir, err)
	}
	return reg, nil
}

func runOnce(ctx context.Context, onlyRepo string, onlyIssue int, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	reg, err := loadRegistry()
	if err != nil {
		return err
	}
	if len(reg.All()) == 0 {
		return fmt.Errorf("no operators registered (embed missing? user dir empty?)")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	repos := cfg.EnabledRepos()
	if onlyRepo != "" {
		r, ok := repos[onlyRepo]
		if !ok {
			return fmt.Errorf("repo %q not found or not enabled", onlyRepo)
		}
		repos = map[string]config.Repo{onlyRepo: r}
	}
	if len(repos) == 0 {
		fmt.Println("no enabled repos to scan")
		return nil
	}

	for fullName, repoCfg := range repos {
		if err := runRepoOnce(ctx, reg, fullName, repoCfg, onlyIssue, timeout); err != nil {
			fmt.Fprintf(os.Stderr, "error on %s: %v\n", fullName, err)
		}
	}
	return nil
}

func runRepoOnce(ctx context.Context, reg *operator.Registry, fullName string, repoCfg config.Repo, onlyIssue int, timeout time.Duration) error {
	client, err := newVCSClient(repoCfg)
	if err != nil {
		return fmt.Errorf("vcs client: %w", err)
	}

	issues, err := client.ListOpenIssues(fullName)
	if err != nil {
		return fmt.Errorf("list open issues: %w", err)
	}

	for _, iss := range issues {
		if onlyIssue != 0 && iss.Number != onlyIssue {
			continue
		}
		sub := &operator.Subject{
			Number: iss.Number,
			Title:  iss.Title,
			Body:   iss.Body,
			Labels: iss.Labels,
			IsPR:   false,
		}
		for _, op := range reg.All() {
			if !operator.Matches(sub, op) {
				continue
			}
			fmt.Printf("[%s] #%d → operator %s\n", fullName, sub.Number, op.Name)

			workdir, cleanup, err := resolveWorkdir(op, repoCfg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  workdir: %v\n", err)
				continue
			}

			// Best-effort comment fetch for prompt context; ignore failures.
			comments, _ := client.ListIssueComments(fullName, sub.Number)

			err = operator.Run(ctx, op, sub, client, operator.RunOptions{
				Repo:     fullName,
				Workdir:  workdir,
				Timeout:  timeout,
				Comments: comments,
			})
			cleanup()
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ %v\n", err)
				continue
			}
			fmt.Println("  ✓ done")
			// One operator per issue per run — avoid compound state changes
			// where two ops race on the same labels.
			break
		}
	}

	// MVP: skip PRs. All 3 built-in operators target issues. When a
	// pr-target operator appears we'll add the same loop over ListOpenPRs.
	return nil
}

// resolveWorkdir picks the cwd for the claude subprocess and returns a
// cleanup callback. For operators that write code (implement), the workdir
// must be the repo's local clone. For read-only operators, a tempdir is
// fine and gets RemoveAll'd on cleanup.
func resolveWorkdir(op *operator.Operator, repoCfg config.Repo) (string, func(), error) {
	// Pragmatic heuristic: "implement" and any pr-target operator need the
	// repo. Everything else gets an ephemeral tempdir. A future schema field
	// (e.g. operator.requires_workdir: true) can replace this.
	needsRepo := op.Name == "implement" || op.Trigger.Target == "pr"
	if needsRepo {
		if repoCfg.LocalPath == "" {
			return "", func() {}, fmt.Errorf("operator %q needs repo local_path but it's empty in config", op.Name)
		}
		return repoCfg.LocalPath, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "clawflow-op-")
	if err != nil {
		return "", func() {}, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}
