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
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
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

	allRepos := cfg.EnabledRepos()
	if onlyRepo != "" {
		if _, ok := allRepos[onlyRepo]; !ok {
			return fmt.Errorf("repo %q not found or not enabled", onlyRepo)
		}
	}
	if len(allRepos) == 0 {
		fmt.Println("no enabled repos to scan")
		return nil
	}

	// Snapshot the static state so the dashboard can render it even if no
	// operator fires this run. Failures are best-effort logged, not fatal.
	if err := snapshot.WriteRepos(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "snapshot repos: %v\n", err)
	}
	if err := snapshot.WriteOperators(reg); err != nil {
		fmt.Fprintf(os.Stderr, "snapshot operators: %v\n", err)
	}
	if err := snapshot.WriteMeta(Version); err != nil {
		fmt.Fprintf(os.Stderr, "snapshot meta: %v\n", err)
	}

	// Two-axis scan:
	//   - Pending snapshot covers every enabled repo, every open issue.
	//     Narrowing flags (--repo / --issue) must NOT shrink the queue view,
	//     otherwise the dashboard loses sight of work outside the current run.
	//   - Operator execution is restricted to the requested scope so an
	//     ad-hoc "rerun for issue 7" doesn't accidentally fire across the
	//     whole org.
	var pending []snapshot.PendingEntry
	for fullName, repoCfg := range allRepos {
		executeHere := onlyRepo == "" || onlyRepo == fullName
		repoPending, err := scanRepoOnce(ctx, reg, fullName, repoCfg, executeHere, onlyIssue, timeout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error on %s: %v\n", fullName, err)
		}
		pending = append(pending, repoPending...)
	}

	// Refresh the runs index so the dashboard shows this run at the top.
	// WriteRunsIndex returns the FULL entry set so we can hand it to
	// WriteUsageSummary without re-walking the runs tree.
	allEntries, err := snapshot.WriteRunsIndex(50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "snapshot runs index: %v\n", err)
	}
	if err := snapshot.WriteUsageSummary(allEntries); err != nil {
		fmt.Fprintf(os.Stderr, "snapshot usage summary: %v\n", err)
	}
	if err := snapshot.WritePending(pending); err != nil {
		fmt.Fprintf(os.Stderr, "snapshot pending: %v\n", err)
	}
	return nil
}

// scanRepoOnce always lists every open issue in the repo and returns the full
// set of (issue × matching-operator) pending entries. Operator execution is
// gated on `executeHere` (whole-repo opt-in) and `onlyIssue` (single-issue
// opt-in within that repo) — pending collection ignores both.
func scanRepoOnce(ctx context.Context, reg *operator.Registry, fullName string, repoCfg config.Repo, executeHere bool, onlyIssue int, timeout time.Duration) ([]snapshot.PendingEntry, error) {
	client, err := newVCSClient(repoCfg)
	if err != nil {
		return nil, fmt.Errorf("vcs client: %w", err)
	}

	issues, err := client.ListOpenIssues(fullName)
	if err != nil {
		return nil, fmt.Errorf("list open issues: %w", err)
	}

	var pending []snapshot.PendingEntry
	capturedAt := time.Now().UTC()
	for _, iss := range issues {
		sub := &operator.Subject{
			Number: iss.Number,
			Title:  iss.Title,
			Body:   iss.Body,
			Labels: iss.Labels,
			IsPR:   false,
		}
		// Snapshot every operator that would match this issue's CURRENT
		// label state. The runner below will fire at most one of them and
		// mutate labels, but pending.json captures the queue as it looked
		// at the start of this run — the next refresh will show the
		// post-run state.
		for _, op := range reg.All() {
			if !operator.Matches(sub, op) {
				continue
			}
			pending = append(pending, snapshot.PendingEntry{
				Repo:        fullName,
				IssueNumber: sub.Number,
				IssueTitle:  sub.Title,
				Operator:    op.Name,
				Labels:      append([]string(nil), sub.Labels...),
				CapturedAt:  capturedAt,
			})
		}
		// Execution scope: skip running operators on this issue when the
		// caller restricted the run to a different repo or different issue.
		// Pending collection above already happened.
		if !executeHere {
			continue
		}
		if onlyIssue != 0 && iss.Number != onlyIssue {
			continue
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

			// Persist per-run events.jsonl + meta.json under the dashboard
			// data dir so `clawflow web` can replay this run later.
			startedAt := time.Now()
			runDir := snapshot.RunDir(fullName, sub.Number, startedAt)
			_ = os.MkdirAll(runDir, 0o755)
			eventsFile, eventsErr := os.Create(filepath.Join(runDir, "events.jsonl"))
			if eventsErr != nil {
				fmt.Fprintf(os.Stderr, "  events sink: %v\n", eventsErr)
			}

			output, runErr := operator.Run(ctx, op, sub, client, operator.RunOptions{
				Repo:        fullName,
				Workdir:     workdir,
				Timeout:     timeout,
				Comments:    comments,
				EventWriter: eventsFile,
			})
			if eventsFile != nil {
				_ = eventsFile.Close()
			}
			cleanup()

			// Write the run's meta.json regardless of outcome.
			rm := snapshot.RunMeta{
				Operator:    op.Name,
				Repo:        fullName,
				IssueNumber: sub.Number,
				IssueTitle:  sub.Title,
				StartedAt:   startedAt.UTC(),
				EndedAt:     time.Now().UTC(),
				Summary:     output,
			}
			if runErr != nil {
				rm.Status = "failed"
				rm.Error = runErr.Error()
			} else if output == "" {
				rm.Status = "skipped"
			} else {
				rm.Status = "success"
			}
			// Best-effort usage extraction. The events file has been closed
			// at this point so the terminal "result" line (if any) is fully
			// flushed to disk. Failures fall back to nil — the index walker
			// will retry on the next refresh.
			if u, uerr := snapshot.ExtractUsage(filepath.Join(runDir, "events.jsonl")); uerr == nil {
				rm.Usage = u
			}
			if err := snapshot.WriteRunMeta(runDir, rm); err != nil {
				fmt.Fprintf(os.Stderr, "  run meta: %v\n", err)
			}

			if runErr != nil {
				fmt.Fprintf(os.Stderr, "  ✗ %v\n", runErr)
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
	return pending, nil
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
