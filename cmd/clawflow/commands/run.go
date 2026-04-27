package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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
		// Track the operator that fired (and reached "done") for this issue
		// so we can drop its pending entry below. Failed runs leave firedOp
		// empty so the entry stays — the user wants to see "still queued"
		// and retry on the next clawflow run.
		var firedOp string
		for _, op := range reg.All() {
			if !operator.Matches(sub, op) {
				continue
			}
			fmt.Printf("[%s] #%d → operator %s\n", fullName, sub.Number, op.Name)

			// Persist per-run events.jsonl + meta.json under the dashboard
			// data dir so `clawflow web` can replay this run later. The
			// dirs and the placeholder meta are created BEFORE the workdir
			// setup so that even an early failure (e.g. worktree creation)
			// gets recorded as a real run on disk.
			startedAt := time.Now()
			runDir := snapshot.RunDir(fullName, sub.Number, startedAt)
			_ = os.MkdirAll(runDir, 0o755)

			// Write a placeholder meta.json with status="running" so the
			// dashboard's polling refresh sees this run as in-flight,
			// rather than waiting for the operator to terminate before any
			// row appears. The final meta is written below after Run()
			// returns and overwrites this stub.
			runningMeta := snapshot.RunMeta{
				Operator:    op.Name,
				Repo:        fullName,
				IssueNumber: sub.Number,
				IssueTitle:  sub.Title,
				StartedAt:   startedAt.UTC(),
				Status:      "running",
			}
			if err := snapshot.WriteRunMeta(runDir, runningMeta); err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ initial run meta: %v\n", err)
			}
			// Refresh runs.json now so the dashboard shows the running
			// row immediately. Discard the entries — the post-run refresh
			// at the top of runOnce rewrites the index with final state.
			if _, err := snapshot.WriteRunsIndex(50); err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ snapshot runs index (running): %v\n", err)
			}

			workdir, cleanup, err := resolveWorkdir(op, repoCfg, fullName, sub.Number, startedAt)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ✗ workdir: %v\n", err)
				// Record the failure so the running-row doesn't get
				// orphaned in the index.
				runningMeta.Status = "failed"
				runningMeta.Error = err.Error()
				runningMeta.EndedAt = time.Now().UTC()
				_ = snapshot.WriteRunMeta(runDir, runningMeta)
				continue
			}
			fmt.Fprintf(os.Stderr, "  ✓ workdir ready: %s\n", workdir)

			// Best-effort comment fetch for prompt context; ignore failures.
			comments, _ := client.ListIssueComments(fullName, sub.Number)

			eventsFile, eventsErr := os.Create(filepath.Join(runDir, "events.jsonl"))
			if eventsErr != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ events sink: %v\n", eventsErr)
			}

			fmt.Fprintf(os.Stderr, "  → running claude (timeout %s)\n", timeout)
			runStart := time.Now()
			output, runErr := operator.Run(ctx, op, sub, client, operator.RunOptions{
				Repo:        fullName,
				Workdir:     workdir,
				Timeout:     timeout,
				Comments:    comments,
				EventWriter: eventsFile,
			})
			runDur := time.Since(runStart).Round(time.Second)
			if eventsFile != nil {
				_ = eventsFile.Close()
			}
			if runErr != nil {
				fmt.Fprintf(os.Stderr, "  ✗ claude failed: %v\n", runErr)
			} else {
				fmt.Fprintf(os.Stderr, "  ✓ claude finished in %s\n", runDur)
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
				fmt.Fprintf(os.Stderr, "  ⚠ run meta: %v\n", err)
			}

			if runErr != nil {
				continue
			}
			fmt.Println("  ✓ done")
			firedOp = op.Name
			// One operator per issue per run — avoid compound state changes
			// where two ops race on the same labels.
			break
		}
		// Drop the pending entry for the operator that just completed so the
		// dashboard's "Pending" section reflects post-run state. Without this
		// the row resurfaces as `queued` once status flips from running →
		// success and the running-vs-pending dedup on the frontend stops
		// hiding it.
		if firedOp != "" {
			issueNum := iss.Number
			pending = slices.DeleteFunc(pending, func(p snapshot.PendingEntry) bool {
				return p.IssueNumber == issueNum && p.Operator == firedOp
			})
		}
	}

	// MVP: skip PRs. All 3 built-in operators target issues. When a
	// pr-target operator appears we'll add the same loop over ListOpenPRs.
	return pending, nil
}

// resolveWorkdir picks the cwd for the claude subprocess and returns a
// cleanup callback. For operators that write code (implement) or target
// PRs, the workdir must be a fresh git worktree backed by the repo's
// local clone — that way the operator's branch/commit/checkout commands
// don't stomp on whatever the user has open in their primary clone. For
// read-only operators, a tempdir is fine and gets RemoveAll'd on cleanup.
func resolveWorkdir(op *operator.Operator, repoCfg config.Repo, fullName string, issueNum int, startedAt time.Time) (string, func(), error) {
	// Pragmatic heuristic: "implement" and any pr-target operator need the
	// repo. Everything else gets an ephemeral tempdir. A future schema field
	// (e.g. operator.requires_workdir: true) can replace this.
	needsRepo := op.Name == "implement" || op.Trigger.Target == "pr"
	if needsRepo {
		if repoCfg.LocalPath == "" {
			return "", func() {}, fmt.Errorf("operator %q needs repo local_path but it's empty in config", op.Name)
		}
		return setupWorktree(repoCfg, fullName, issueNum, startedAt)
	}
	dir, err := os.MkdirTemp("", "clawflow-op-")
	if err != nil {
		return "", func() {}, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// setupWorktree provisions a fresh git worktree at
// ~/.clawflow/worktrees/<repo-slug>/issue-<N>-<ts> backed by the user's
// local clone. The worktree starts on detached HEAD at the latest
// origin/<base_branch> so the operator can `git checkout -b fix/issue-N`
// without ever touching the user's checked-out branch. Cleanup removes
// the worktree (force) but leaves any branches the operator created
// alone — pushed branches stay locally for the user to inspect/delete.
func setupWorktree(repoCfg config.Repo, fullName string, issueNum int, startedAt time.Time) (string, func(), error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", func() {}, fmt.Errorf("home dir: %w", err)
	}
	slug := strings.ReplaceAll(fullName, "/", "__")
	ts := startedAt.UTC().Format("2006-01-02T15-04-05Z")
	parent := filepath.Join(home, ".clawflow", "worktrees", slug)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", func() {}, fmt.Errorf("mkdir worktree parent: %w", err)
	}
	wtPath := filepath.Join(parent, fmt.Sprintf("issue-%d-%s", issueNum, ts))

	base := repoCfg.BaseBranch
	if base == "" {
		base = "main"
	}
	localPath := repoCfg.LocalPath

	// Best-effort fetch to align origin/<base> with the remote. A repo
	// without network reachability (offline dev, private bastion) should
	// still be able to spin up a worktree at the local origin/<base>.
	fmt.Fprintf(os.Stderr, "  → setup: fetching origin/%s\n", base)
	fetchCmd := exec.Command("git", "-C", localPath, "fetch", "origin", base)
	fetchCmd.Stdout = os.Stderr
	fetchCmd.Stderr = os.Stderr
	if err := fetchCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠ fetch failed (continuing): %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "  → setup: creating worktree at %s\n", wtPath)
	addCmd := exec.Command("git", "-C", localPath, "worktree", "add", "--detach", wtPath, "origin/"+base)
	addCmd.Stdout = os.Stderr
	addCmd.Stderr = os.Stderr
	if err := addCmd.Run(); err != nil {
		// Fall back to the local base branch ref. Brand-new clones may
		// not have origin/<base> yet (e.g. the user just `git init`'d
		// and added a remote without pushing anything).
		fmt.Fprintf(os.Stderr, "  ⚠ worktree add origin/%s failed, falling back to local %s\n", base, base)
		addLocal := exec.Command("git", "-C", localPath, "worktree", "add", "--detach", wtPath, base)
		addLocal.Stdout = os.Stderr
		addLocal.Stderr = os.Stderr
		if err2 := addLocal.Run(); err2 != nil {
			return "", func() {}, fmt.Errorf("git worktree add failed (origin/%s: %v; %s: %w)", base, err, base, err2)
		}
	}
	fmt.Fprintf(os.Stderr, "  ✓ worktree ready (detached HEAD at origin/%s)\n", base)

	cleanup := func() {
		fmt.Fprintln(os.Stderr, "  → cleanup: removing worktree")
		rm := exec.Command("git", "-C", localPath, "worktree", "remove", "--force", wtPath)
		rm.Stdout = os.Stderr
		rm.Stderr = os.Stderr
		if err := rm.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ worktree remove failed: %v\n", err)
			return
		}
		fmt.Fprintln(os.Stderr, "  ✓ worktree removed")
	}
	return wtPath, cleanup, nil
}
