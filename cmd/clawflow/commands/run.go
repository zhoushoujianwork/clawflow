package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	rootmod "github.com/zhoushoujianwork/clawflow"
	"github.com/zhoushoujianwork/clawflow/internal/api"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/pilot"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// runLog is the package-level run.log handle, set by NewRunCmd's RunE
// so that runJob worker functions can write structured lifecycle events
// without each layer threading the logger through. Nil-safe (the log
// package's nil receiver is a no-op).
var runLog *clog.Logger

// roleForOperator picks which config.Role slot an operator should
// resolve its model from. evaluate-* operators read existing context
// and produce structured analysis, so they get the heavier eval model
// (Opus by default). Everything else (implement, reply-comment,
// user-supplied skills) gets the cheaper operator model (Sonnet).
func roleForOperator(opName string) string {
	if strings.HasPrefix(opName, "evaluate-") {
		return config.RoleEval
	}
	return config.RoleOperator
}

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
  - on first match: run claude -p, post result as a comment, apply outcome label

Concurrency is gated by an in-process per-issue mutex; up to
settings.max_concurrent_agents (default 4) operators run in parallel
across issues. The "implement" operator is additionally serialized
per-repo because it shares a local git clone. Pass --repo and/or
--issue to narrow the scan.`,
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
	// Global collection for all issues from all repos
	var globalIssues []snapshot.IssueEntry

	if ctx == nil {
		ctx = context.Background()
	}

	// Open the run.log sink. nil-safe — if Open fails (read-only home,
	// disk full) the rest of the command still runs; only the file
	// trail is missing. Install it as the snapshot.ReconcileLog sink so
	// reconciler skip events land in the same file as run lifecycle.
	lg, _ := clog.Open("run")
	defer lg.Close()
	runLog = lg
	if lg != nil {
		snapshot.ReconcileLog = lg
	}

	// Single-instance lock: prevent concurrent clawflow run invocations
	// (e.g. cron firing while a previous run is still active). On
	// contention, log run/skip and exit cleanly so cron doesn't treat
	// the skip as a failure. Stale locks from crashed processes are
	// reclaimed automatically via PID-liveness check.
	if err := snapshot.AcquireRunLock(Version); err != nil {
		if holder, readErr := snapshot.ReadRunLock(); readErr == nil {
			lg.Info("run/skip",
				"pid", os.Getpid(),
				"holder_pid", holder.PID,
				"holder_started", holder.StartedAt.Format(time.RFC3339),
			)
			fmt.Fprintf(os.Stderr, "⚠ clawflow run already active (pid=%d, started=%s) — skipping\n",
				holder.PID, holder.StartedAt.Format(time.RFC3339))
		} else {
			lg.Info("run/skip", "pid", os.Getpid(), "reason", err.Error())
			fmt.Fprintf(os.Stderr, "⚠ %v — skipping\n", err)
		}
		return nil
	}
	// Release the run lock on normal exit. For unhandled signals (SIGINT/
	// SIGTERM) the lock is also released explicitly before os.Exit so the
	// next cron tick isn't blocked by a stale file. The PID-liveness check
	// in AcquireRunLock handles any remaining crash scenarios.
	defer snapshot.ReleaseRunLock()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sigDone := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			snapshot.ReleaseRunLock()
			os.Exit(0)
		case <-sigDone:
		}
	}()
	defer func() {
		signal.Stop(sigCh)
		close(sigDone)
	}()

	lg.Info("run/start", "pid", os.Getpid(), "version", Version, "only_repo", onlyRepo, "only_issue", onlyIssue, "timeout", timeout)
	defer lg.Info("run/exit", "pid", os.Getpid())

	// Self-watchdog: a downstream call that blocks forever (a git
	// subprocess stuck on a credential prompt, a network read that never
	// returns) can leave this process alive indefinitely — exactly what
	// issue #216 observed, where the run sat idle for 8h+ after reconcile
	// and stalled the web auto-run scheduler waiting on it. A plain
	// context deadline does NOT save us here: cancelling a context cannot
	// interrupt a blocking exec.Command or syscall. So we arm an
	// independent timer that, on expiry, dumps every goroutine stack (the
	// diagnostic the original report lacked — it pinpoints the exact
	// blocked call) and force-exits. This covers standalone/cron runs;
	// web-spawned runs are additionally bounded by api.TriggerRun's
	// process-level watchdog.
	overall := overallRunBudget(timeout)
	selfWatchdog := time.AfterFunc(overall, func() {
		lg.Warn("run/watchdog", "pid", os.Getpid(), "msg", "overall run budget exceeded — likely a hung git/network call; dumping stacks and exiting", "budget", overall.String())
		fmt.Fprintf(os.Stderr, "\n⚠ clawflow run exceeded its overall budget %s — likely a hung git/network call.\n  Dumping goroutine stacks and exiting (issue #216 safeguard).\n", overall)
		dumpGoroutineStacks(lg)
		snapshot.ReleaseRunLock()
		os.Exit(1)
	})
	defer selfWatchdog.Stop()

	// Auto-pull: sync config from Gist before scanning so this machine
	// picks up any changes pushed from other machines (e.g. new repos,
	// updated settings). Best-effort: if sync is not configured or the
	// network is unavailable, we continue with the local config.
	if api.AutoPull() {
		fmt.Fprintf(os.Stderr, "✓ auto-pulled config from Gist\n")
		lg.Info("run/auto_pull", "result", "ok")
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
	workers := cfg.Settings.MaxConcurrentAgents
	if workers <= 0 {
		workers = 4
	}
	debugf("loaded %d operator(s); scanning %d enabled repo(s) (onlyRepo=%q onlyIssue=%d timeout=%s workers=%d)",
		len(reg.All()), len(allRepos), onlyRepo, onlyIssue, timeout, workers)
	for _, op := range reg.All() {
		debugf("  operator %s target=%s required=%v excluded=%v",
			op.Name, op.Trigger.Target, op.Trigger.LabelsRequired, op.Trigger.LabelsExcluded)
	}

	// Resolve the current machine's hostname once. Used to skip repos that
	// are bound to a different machine (BoundMachine field). Errors are
	// non-fatal: an empty hostname means no repos will be skipped by the
	// bound_machine check (safe default).
	hostname, _ := os.Hostname()

	// Reconcile any runs whose on-disk state is inconsistent (stuck
	// "running", missing meta.json) BEFORE we touch anything else, so the
	// dashboard's first refresh of this run picks up the fixed state. The
	// staleAfter threshold matches the per-operator default timeout — any
	// run still showing "running" past that is definitively dead.
	if n, err := snapshot.ReconcileStaleRuns(timeout); err == nil {
		if n > 0 {
			fmt.Fprintf(os.Stderr, "✓ reconciled %d stale run(s) on disk\n", n)
		}
		lg.Info("run/reconcile", "fixed", n, "stale_after", timeout)
	} else {
		fmt.Fprintf(os.Stderr, "⚠ reconcile stale runs: %v\n", err)
		lg.Warn("run/reconcile", "err", err.Error())
	}

	// GC analysis worktrees for repos that are no longer in config. When a
	// repo is removed from clawflow config the persistent analysis worktree
	// it created would otherwise remain registered in the source clone's
	// .git/worktrees/ forever (issue #211). This is cheap (just a directory
	// scan) and idempotent.
	if n := pruneOrphanedAnalysisWorktrees(cfg); n > 0 {
		fmt.Fprintf(os.Stderr, "✓ pruned %d orphaned analysis worktree(s)\n", n)
		lg.Info("run/prune_analysis_worktrees", "pruned", n)
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
	//
	// Phase 1 (sequential): scan every repo, build the pending snapshot,
	// and queue the (issue × first-matching-operator) pairs that are
	// eligible to run. Issues locked by another process (local lockfile)
	// are skipped at queue time.
	var pending []snapshot.PendingEntry
	var jobs []*runJob
	for fullName, repoCfg := range allRepos {
		// Skip repos bound to a different machine. A repo with no BoundMachine
		// (empty string) is processed by every machine — the common case.
		// When hostname resolution failed (empty string), we conservatively
		// process all repos so a misconfigured machine doesn't silently drop work.
		if repoCfg.BoundMachine != "" && hostname != "" && repoCfg.BoundMachine != hostname {
			debugf("repo %s is bound to %s, skipping (current machine: %s)", fullName, repoCfg.BoundMachine, hostname)
			continue
		}
		if cfg.Settings.RequireBinding && repoCfg.BoundMachine == "" && hostname != "" {
			debugf("repo %s has no bound_machine and require_binding is set, skipping", fullName)
			continue
		}
		executeHere := onlyRepo == "" || onlyRepo == fullName
		repoPending, repoJobs, err := scanRepoOnce(reg, fullName, repoCfg, executeHere, onlyIssue, &globalIssues)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error on %s: %v\n", fullName, err)
		}
		// Propagate language preference to each job so the operator runner
		// can inject the correct language directive into the system prompt.
		for _, j := range repoJobs {
			j.language = cfg.Settings.Language
		}
		pending = append(pending, repoPending...)
		jobs = append(jobs, repoJobs...)
	}

	// Phase 2 (parallel): dispatch matched jobs across `workers` goroutines.
	// Three concurrency gates:
	//   - local lockfile (~/.clawflow/locks/): cross-process lock, visible
	//     to other clawflow run processes via PID-liveness check
	//   - per-issue mutex: within this process, at most one operator per issue
	//   - per-repo mutex: serializes `implement` against the shared local
	//     git clone (worktree add + fetch are not safe in parallel).
	//     Read-only operators (classify, evaluate-*, reply-comment) skip
	//     the per-repo lock and run freely.
	fired := runJobsParallel(ctx, jobs, workers, timeout)

	// Phase 3 (sequential): drop pending entries that fired, then write
	// the snapshots so the dashboard reflects the post-run state.
	if len(fired) > 0 {
		pending = slices.DeleteFunc(pending, func(p snapshot.PendingEntry) bool {
			for _, f := range fired {
				if f.Repo == p.Repo && f.IssueNumber == p.IssueNumber && f.Operator == p.Operator {
					return true
				}
			}
			return false
		})
	}

	// Refresh the runs index so the dashboard shows this run at the top.
	// WriteRunsIndex returns the FULL entry set so we can hand it to
	// WriteUsageSummary without re-walking the runs tree.
	allEntries, err := snapshot.WriteRunsIndex(50)
	if err != nil {
		fmt.Fprintf(os.Stderr, "snapshot runs index: %v\n", err)
	}
	if err := snapshot.WriteUsageSummary(allEntries, cfg.Settings.BillingCycleDay); err != nil {
		fmt.Fprintf(os.Stderr, "snapshot usage summary: %v\n", err)
	}
	if err := snapshot.WritePending(pending); err != nil {
		fmt.Fprintf(os.Stderr, "snapshot pending: %v\n", err)
	}
	if err := snapshot.WriteIssues(globalIssues); err != nil {
		fmt.Fprintf(os.Stderr, "snapshot issues: %v\n", err)
	}

	// Phase 4 (sequential): wake the project-manager for every project
	// with automation enabled and past its cooldown. PMs run AFTER
	// operators so they see the post-pass state — any issue an
	// operator just marked agent-evaluated is visible to the PM
	// scheduling decision. PM failures are non-fatal: the run as a
	// whole is reported successful as long as the operator phase
	// completed.
	//
	// Gated on --repo / --issue narrowing: ad-hoc single-issue runs
	// shouldn't wake every project's PM. Only the unscoped pass
	// (the one a cron / hook normally invokes) triggers PMs.
	if onlyRepo == "" && onlyIssue == 0 {
		if n, err := pilot.Schedule(ctx, timeout); err != nil {
			fmt.Fprintf(os.Stderr, "[pilot] schedule: %v\n", err)
		} else if n > 0 {
			fmt.Fprintf(os.Stderr, "[pilot] woke %d project(s)\n", n)
		}
	}

	// Auto-push: sync config to Gist after the run so any label/config
	// changes made during this pass are visible to other machines.
	// Best-effort: push failure does not fail the run.
	if api.AutoPush() {
		fmt.Fprintf(os.Stderr, "✓ auto-pushed config to Gist\n")
	}

	return nil
}

// runJob is one (issue, operator) pair that scanRepoOnce decided is
// eligible to fire. The worker pool in runJobsParallel consumes these.
type runJob struct {
	op       *operator.Operator
	sub      *operator.Subject
	repo     string
	repoCfg  config.Repo
	client   vcs.Client
	language string // Settings.Language propagated from cfg at job creation time
}

// firedKey identifies a (repo, issue, operator) triple that ran to a
// non-empty success/skip outcome. Used to dedup pending entries that
// already fired in this pass.
type firedKey struct {
	Repo        string
	IssueNumber int
	Operator    string
}

// deterministicSkip reports whether `op` is guaranteed to be skipped at
// execution time on `repoCfg` no matter what the issue's labels look like.
// Used both to suppress the matched-but-unrunnable entry from pending.json
// (so the dashboard doesn't pile up forever-queued rows) and to short-
// circuit firstMatch execution. Both call sites must agree, otherwise a
// pending entry would re-appear on every scan.
//
// Currently the only deterministic-skip case is `implement` against a repo
// with no local clone (worktree add needs a working tree on disk). Other
// skips — locked by another process, rate-limited, label-state changes —
// are state-dependent and may resolve on the next pass, so they belong in
// pending and are filtered out elsewhere.
func deterministicSkip(op *operator.Operator, repoCfg config.Repo) bool {
	if op == nil {
		return false
	}
	if op.Name == "implement" && repoCfg.LocalPath == "" {
		return true
	}
	return false
}

// deterministicSkipReason returns a human-readable explanation for the
// matching deterministicSkip case. Empty string if the op would not be
// skipped — callers should gate this behind a deterministicSkip check.
func deterministicSkipReason(op *operator.Operator, repoCfg config.Repo) string {
	if op != nil && op.Name == "implement" && repoCfg.LocalPath == "" {
		return "implement requires local_path but it's empty"
	}
	return ""
}

// scanRepoOnce lists every open issue in the repo and returns the full
// set of (issue × matching-operator) pending entries plus the runJobs
// that the executor should fire (at most one per issue, the first
// matching operator). Operator execution scope is gated on
// `executeHere` (whole-repo opt-in) and `onlyIssue` (single-issue
// opt-in) — pending collection ignores both.
//
// Stale `agent-running` labels (legacy of the abandoned label-lock
// model) are stripped here before matching, so an issue that was left
// labeled by a crashed run from an older binary cleanly re-enters the
// pipeline without manual intervention.
func scanRepoOnce(reg *operator.Registry, fullName string, repoCfg config.Repo, executeHere bool, onlyIssue int, allIssues *[]snapshot.IssueEntry) ([]snapshot.PendingEntry, []*runJob, error) {
	client, err := newVCSClient(repoCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("vcs client: %w", err)
	}

	issues, err := client.ListOpenIssues(fullName)
	if err != nil {
		return nil, nil, fmt.Errorf("list open issues: %w", err)
	}

	// GitHub sub-issues are not returned by the standard /issues list API.
	// Walk every open issue and append any sub-issues that aren't already
	// in the list. Two levels deep covers the current tracking→sub→sub-sub
	// pattern; deeper nesting is uncommon and can be added later.
	seen := make(map[int]bool, len(issues))
	for _, iss := range issues {
		seen[iss.Number] = true
	}
	for _, iss := range issues {
		subs, subErr := client.ListSubIssues(fullName, iss.Number)
		if subErr != nil {
			debugf("[%s] list sub-issues of #%d: %v (skipping)", fullName, iss.Number, subErr)
			continue
		}
		for _, sub := range subs {
			if seen[sub.Number] || sub.State != "open" {
				continue
			}
			seen[sub.Number] = true
			issues = append(issues, sub)
			debugf("[%s] discovered sub-issue #%d (parent #%d)", fullName, sub.Number, iss.Number)
		}
	}

	debugf("[%s] %d open issue(s) fetched (executeHere=%v onlyIssue=%d)",
		fullName, len(issues), executeHere, onlyIssue)

	// NOTE: cross-process locking now uses local lockfiles (~/.clawflow/locks/)
	// instead of the `agent-running` VCS label. Stale lockfiles from crashed
	// processes are cleaned up by CleanStaleLocks (called in ReconcileStaleRuns)
	// and by AcquireLock's PID-liveness check.

	var pending []snapshot.PendingEntry
	var jobs []*runJob
	capturedAt := time.Now().UTC()
	for _, iss := range issues {
		sub := &operator.Subject{
			Number: iss.Number,
			Title:  iss.Title,
			Body:   iss.Body,
			Labels: iss.Labels,
			State:  iss.State,
			IsPR:   false,
		}
		debugf("[%s] #%d labels=%v title=%q", fullName, sub.Number, sub.Labels, sub.Title)
		// Snapshot every operator that would match this issue's CURRENT
		// label state. The executor will fire at most one of them and
		// mutate labels, but pending.json captures the queue as it looked
		// at the start of this run — the next refresh will show the
		// post-run state.
		var firstMatch *operator.Operator
		for _, op := range reg.All() {
			ok, reason := operator.MatchesWithReason(sub, op)
			if !ok {
				debugf("  ✗ %s: %s", op.Name, reason)
				continue
			}
			// Drop "deterministic skip" cases from pending so they don't
			// pile up forever in the dashboard. The execution path below
			// has the same skip for firstMatch (see "Pre-flight: skip
			// deterministic failures early"); both must agree, otherwise
			// matched-but-unrunnable operators stay queued indefinitely.
			if deterministicSkip(op, repoCfg) {
				debugf("  ⊘ %s matches but config makes it unrunnable, skipping pending", op.Name)
				continue
			}
			debugf("  ✓ %s matches", op.Name)
			pending = append(pending, snapshot.PendingEntry{
				Repo:        fullName,
				IssueNumber: sub.Number,
				IssueTitle:  sub.Title,
				Operator:    op.Name,
				Labels:      append([]string(nil), sub.Labels...),
				CapturedAt:  capturedAt,
			})
			if firstMatch == nil {
				firstMatch = op
			}
		}
		if firstMatch == nil {
			debugf("  → no operator matched #%d (label its required trigger to enqueue, e.g. \"clawflow label add --repo %s --issue %d --label feat\")",
				sub.Number, fullName, sub.Number)
		}
		// Execution scope: skip queuing operators on this issue when the
		// caller restricted the run to a different repo / different issue.
		// Pending collection above already happened.
		if !executeHere {
			debugf("  · skipping execution on %s#%d (--repo restricts execution to a different repo)", fullName, iss.Number)
			continue
		}
		if onlyIssue != 0 && iss.Number != onlyIssue {
			debugf("  · skipping execution on #%d (--issue=%d)", iss.Number, onlyIssue)
			continue
		}
		if firstMatch != nil {
			// Pre-flight: skip deterministic failures early. implement
			// needs a local clone but the repo may not have local_path set.
			if deterministicSkip(firstMatch, repoCfg) {
				debugf("  · skipping %s on #%d: %s", firstMatch.Name, iss.Number, deterministicSkipReason(firstMatch, repoCfg))
				continue
			}
			// Skip issues already locked by another process.
			if snapshot.IsLocked(fullName, iss.Number) {
				debugf("  · skipping #%d: locked by another process", iss.Number)
				continue
			}
			jobs = append(jobs, &runJob{op: firstMatch, sub: sub, repo: fullName, repoCfg: repoCfg, client: client})
		}
	}

	// MVP: skip PRs. All 3 built-in operators target issues. When a
	// pr-target operator appears we'll add the same loop over ListOpenPRs.

	// Snapshot every issue (open + closed) for the dashboard via a
	// single ListIssues("all") call — same shape as POST
	// /api/repo/refresh-issues so the cron path and the manual Sync
	// button can't disagree about which issues are open vs closed.
	//
	// Operator matching above keeps using ListOpenIssues so the
	// per_page=100 budget for open issues is independent of any closed
	// noise. Snapshot side accepts the implicit ~100-issue cap from
	// the "all" call (sorted updated_at desc), which keeps stale
	// ancient closed issues out of the view.
	if snapshotIssues, sErr := client.ListIssues(fullName, "all", nil); sErr == nil {
		for _, iss := range snapshotIssues {
			*allIssues = append(*allIssues, snapshot.IssueEntry{
				Repo:        fullName,
				IssueNumber: iss.Number,
				IssueTitle:  iss.Title,
				Labels:      append([]string(nil), iss.Labels...),
				State:       iss.State,
				CapturedAt:  capturedAt,
				CreatedAt:   iss.CreatedAt,
				ClosedAt:    iss.ClosedAt,
			})
		}
	} else {
		fmt.Fprintf(os.Stderr, "[%s] snapshot list issues: %v\n", fullName, sErr)
	}

	return pending, jobs, nil
}

// runJobsParallel dispatches `jobs` across `workers` goroutines, gating
// concurrency with a per-issue lock (always) so two operators never race
// on the same (repo, issue). The previous per-repo lock for `implement`
// has been removed: git-level lock conflicts during concurrent worktree
// operations are handled by runGitWithRetry (exponential backoff on git
// lock errors) so same-repo implement jobs can now run in parallel.
// Returns the set of jobs whose operator produced output (i.e. fired),
// so the caller can deduplicate pending entries.
//
// Rate-limit circuit breaker: when any worker detects a transient rate-limit
// from claude, it sets a shared atomic flag. Subsequent workers check the flag
// before starting and skip their job (recording status="rate-limited") so the
// entire queue doesn't cascade into failures. The skipped issues retain their
// trigger labels and will be retried on the next run pass.
func runJobsParallel(ctx context.Context, jobs []*runJob, workers int, timeout time.Duration) []firedKey {
	if len(jobs) == 0 {
		return nil
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}

	var (
		issueLocks  sync.Map // key: "<repo>#<issue>" → *sync.Mutex
		fired       []firedKey
		firedMu     sync.Mutex
		rateLimited atomic.Bool // set when any worker hits a rate limit
	)

	mu := func(m *sync.Map, key string) *sync.Mutex {
		actual, _ := m.LoadOrStore(key, &sync.Mutex{})
		return actual.(*sync.Mutex)
	}

	jobCh := make(chan *runJob, len(jobs))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				// If a previous worker hit a rate limit, skip remaining
				// jobs so they aren't permanently marked as failed. They
				// keep their trigger labels and will be retried next pass.
				if rateLimited.Load() {
					prefix := fmt.Sprintf("[%s#%d %s]", j.repo, j.sub.Number, j.op.Name)
					fmt.Fprintf(os.Stderr, "%s → skipped (rate limit hit earlier in this pass)\n", prefix)
					runLog.Info("run/skipped_rate_limit", "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name)
					continue
				}

				iMu := mu(&issueLocks, fmt.Sprintf("%s#%d", j.repo, j.sub.Number))
				iMu.Lock()
				didFire, hitRateLimit := runOneOperator(ctx, j, timeout)
				iMu.Unlock()
				if hitRateLimit {
					rateLimited.Store(true)
				}
				if didFire {
					firedMu.Lock()
					fired = append(fired, firedKey{Repo: j.repo, IssueNumber: j.sub.Number, Operator: j.op.Name})
					firedMu.Unlock()
				}
			}
		}()
	}
	for _, j := range jobs {
		jobCh <- j
	}
	close(jobCh)
	wg.Wait()
	return fired
}

// runOneOperator executes a single operator against its issue and
// persists meta.json + events.jsonl under the dashboard data dir.
// Returns (didFire, hitRateLimit):
//   - didFire: true when the operator produced non-empty stdout (outcome label applied)
//   - hitRateLimit: true when claude exited with a transient rate-limit error;
//     the caller should stop dispatching remaining jobs this pass
//
// All log lines are prefixed with "[<repo>#<issue> <op>]" so output
// from concurrent workers stays disentangleable.
func runOneOperator(ctx context.Context, j *runJob, timeout time.Duration) (didFire bool, hitRateLimit bool) {
	prefix := fmt.Sprintf("[%s#%d %s]", j.repo, j.sub.Number, j.op.Name)
	fmt.Printf("%s → start\n", prefix)

	// Cross-process lock: acquire a local lockfile so other clawflow run
	// processes (cron, web scheduler) skip this issue. If the lock is
	// already held by a live process, bail out.
	if err := snapshot.AcquireLock(j.repo, j.sub.Number, j.op.Name); err != nil {
		fmt.Fprintf(os.Stderr, "%s ⚠ lock failed (another process owns it): %v\n", prefix, err)
		runLog.Warn("run/lock_failed", "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name, "err", err.Error())
		return false, false
	}
	defer snapshot.ReleaseLock(j.repo, j.sub.Number)
	runLog.Info("run/lock", "pid", os.Getpid(), "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name)
	defer runLog.Info("run/unlock", "repo", j.repo, "issue", j.sub.Number)

	// Re-fetch the issue's current labels to guard against the race where
	// labels were added between the initial poll and now (e.g. user manually
	// labelled the issue while classify was already queued).
	if freshLabels, err := j.client.GetIssueLabels(j.repo, j.sub.Number); err == nil {
		freshSub := *j.sub
		freshSub.Labels = freshLabels
		if ok, reason := operator.MatchesWithReason(&freshSub, j.op); !ok {
			fmt.Printf("%s → skip (labels changed since poll: %s)\n", prefix, reason)
			return false, false
		}
	}

	// Persist per-run events.jsonl + meta.json under the dashboard
	// data dir so `clawflow web` can replay this run later. The dirs
	// and the placeholder meta are created BEFORE the workdir setup so
	// even an early failure (e.g. worktree creation) gets recorded as
	// a real run on disk.
	startedAt := time.Now()
	runDir := snapshot.RunDir(j.repo, j.sub.Number, startedAt)
	_ = os.MkdirAll(runDir, 0o755)

	runningMeta := snapshot.RunMeta{
		Operator:    j.op.Name,
		Repo:        j.repo,
		IssueNumber: j.sub.Number,
		IssueTitle:  j.sub.Title,
		IssueState:  j.sub.State,
		StartedAt:   startedAt.UTC(),
		Status:      "running",
	}
	if err := snapshot.WriteRunMeta(runDir, runningMeta); err != nil {
		fmt.Fprintf(os.Stderr, "%s ✗ initial run meta: %v\n", prefix, err)
	}
	if _, err := snapshot.WriteRunsIndex(50); err != nil {
		fmt.Fprintf(os.Stderr, "%s ⚠ snapshot runs index (running): %v\n", prefix, err)
	}

	// Fetch comments before resolveWorkdir so we can parse the
	// ClawFlow-Base-Branch: marker and plumb the override into worktree
	// setup. Using ListIssueCommentsDetail gives us CreatedAt for correct
	// newest-first ordering and avoids a second round-trip at line 736.
	detailComments, _ := j.client.ListIssueCommentsDetail(j.repo, j.sub.Number)

	// Parse optional per-issue base-branch override.
	overrideBranch, overrideSource, hasOverride, overrideErr := ParseBaseBranchOverride(j.sub.Body, detailComments)
	if overrideErr != nil {
		// Hard-fail: the marker exists but is invalid. Surface the error
		// the same way as workdir failures (meta.json + circuit breaker)
		// so the dashboard and PM patrol can see it without posting a
		// VCS comment (operator contract: runner owns VCS side-effects).
		fmt.Fprintf(os.Stderr, "%s ✗ base-branch marker invalid: %v\n", prefix, overrideErr)
		runningMeta.Status = "failed"
		runningMeta.Error = overrideErr.Error()
		now := time.Now().UTC()
		runningMeta.EndedAt = &now
		_ = snapshot.WriteRunMeta(runDir, runningMeta)
		checkCircuitBreaker(j, prefix)
		return false, false
	}
	if hasOverride {
		fmt.Fprintf(os.Stderr, "%s → base-branch override: %q (from %s)\n", prefix, overrideBranch, overrideSource)
	}

	wtResult, cleanup, err := resolveWorkdir(j.op, j.repoCfg, j.repo, j.sub.Number, startedAt, overrideBranch)
	if err != nil {
		// Failure path: do NOT post a comment to the issue. The full
		// error is captured in events.jsonl and the run row on the
		// dashboard; the circuit breaker below is the only thing that
		// touches the issue (an `agent-failed` label after enough
		// consecutive failures). PM patrol can clear that label later
		// if the underlying problem looks recovered.
		fmt.Fprintf(os.Stderr, "%s ✗ workdir: %v\n", prefix, err)
		runningMeta.Status = "failed"
		runningMeta.Error = err.Error()
		now := time.Now().UTC()
		runningMeta.EndedAt = &now
		_ = snapshot.WriteRunMeta(runDir, runningMeta)
		checkCircuitBreaker(j, prefix)
		return false, false
	}
	workdir := wtResult.Path
	if wtResult.Resumed {
		fmt.Fprintf(os.Stderr, "%s ✓ workdir RESUMED: %s (branch: %s)\n", prefix, workdir, wtResult.BranchName)
	} else {
		fmt.Fprintf(os.Stderr, "%s ✓ workdir ready: %s\n", prefix, workdir)
	}

	// Extract comment bodies from the already-fetched detail slice.
	comments := make([]string, len(detailComments))
	for i, c := range detailComments {
		comments[i] = c.Body
	}

	eventsFile, eventsErr := os.Create(filepath.Join(runDir, "events.jsonl"))
	if eventsErr != nil {
		fmt.Fprintf(os.Stderr, "%s ⚠ events sink: %v\n", prefix, eventsErr)
	}

	creds, _ := config.LoadCredentials()
	role := roleForOperator(j.op.Name)
	// Resolve a preview model string for logging only — the actual
	// per-provider resolution happens inside RunClaude's failover loop.
	previewModel := config.ResolveModelForRole(creds, role)
	fmt.Fprintf(os.Stderr, "%s → claude (role %s, preview-model %s, timeout %s)\n", prefix, role, previewModel, timeout)
	runLog.Info("run/claude_start", "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name, "role", role, "model", previewModel, "timeout", timeout)
	debugf("%s using role %q (preview-model %q)", prefix, role, previewModel)
	runStart := time.Now()

	// Build resume context for the operator prompt when reusing a worktree.
	var resumeCtx string
	if wtResult.Resumed {
		resumeCtx = buildResumeContext(wtResult)
	}

	output, outcome, runErr := operator.Run(ctx, j.op, j.sub, j.client, operator.RunOptions{
		Repo:          j.repo,
		Workdir:       workdir,
		Timeout:       timeout,
		Comments:      comments,
		Role:          role,
		EventWriter:   eventsFile,
		ResumeContext: resumeCtx,
		Language:      j.language,
	})
	runDur := time.Since(runStart).Round(time.Second)
	if eventsFile != nil {
		_ = eventsFile.Close()
	}

	// Write status=finalizing immediately after operator.Run() returns
	// successfully. This is the first post-claude action, before usage
	// extraction, worktree cleanup, or any other work. If the process is
	// killed in the window between here and the final WriteRunMeta below,
	// the reconciler will see "finalizing" (not "running") and know that
	// VCS side-effects already completed — it will promote to "success"
	// without re-queuing the issue for a duplicate run.
	if runErr == nil && output != "" {
		finalizingMeta := snapshot.RunMeta{
			Operator:    j.op.Name,
			Repo:        j.repo,
			IssueNumber: j.sub.Number,
			IssueTitle:  j.sub.Title,
			IssueState:  j.sub.State,
			StartedAt:   startedAt.UTC(),
			Status:      "finalizing",
			Summary:     output,
		}
		if err := snapshot.WriteRunMeta(runDir, finalizingMeta); err != nil {
			fmt.Fprintf(os.Stderr, "%s ⚠ finalizing meta: %v\n", prefix, err)
		}
	}

	// Detect transient rate-limit errors before deciding on the run status.
	// A rate-limited run is NOT a permanent failure: the issue keeps its
	// trigger labels and will be retried on the next pass. We also signal
	// the caller so it can abort the remaining job queue.
	isRateLimit := runErr != nil && errors.Is(runErr, operator.ErrRateLimit)

	// Detect auth errors (403 / "request not allowed"). These are distinct
	// from rate limits and generic failures: retrying immediately won't help —
	// the session token or account permission needs human intervention.
	// We do NOT count auth errors toward the circuit breaker to avoid
	// auto-labeling agent-failed on a configuration problem (issue #204).
	isAuthError := runErr != nil && errors.Is(runErr, operator.ErrAuthError)

	// Detect no-marker failures: claude produced output but omitted the
	// outcome marker. operator.Run now returns ErrNoOutcomeMarker for this
	// case so we can route it through the circuit breaker (status="failed")
	// instead of silently recording it as "success" and letting the issue
	// re-fire indefinitely (see issue #143).
	isNoMarker := runErr != nil && errors.Is(runErr, operator.ErrNoOutcomeMarker)

	if runErr != nil {
		if isRateLimit {
			fmt.Fprintf(os.Stderr, "%s ✗ claude rate limited (will retry next pass): %v\n", prefix, runErr)
		} else if isAuthError {
			// Use Warn-level so patrol grep -E "ERROR|WARN" catches this.
			runLog.Warn("run/auth_error", "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name, "err", runErr.Error())
			fmt.Fprintf(os.Stderr, "%s ✗ claude auth error 403 (check session/API key, NOT retrying): %v\n", prefix, runErr)
		} else if isNoMarker {
			fmt.Fprintf(os.Stderr, "%s ✗ claude produced no outcome marker (will count toward circuit breaker): %v\n", prefix, runErr)
		} else {
			fmt.Fprintf(os.Stderr, "%s ✗ claude failed: %v\n", prefix, runErr)
		}
	} else {
		fmt.Fprintf(os.Stderr, "%s ✓ claude finished in %s\n", prefix, runDur)
	}

	endedAt := time.Now().UTC()
	rm := snapshot.RunMeta{
		Operator:    j.op.Name,
		Repo:        j.repo,
		IssueNumber: j.sub.Number,
		IssueTitle:  j.sub.Title,
		StartedAt:   startedAt.UTC(),
		EndedAt:     &endedAt,
		Summary:     output,
	}
	switch {
	case isRateLimit:
		// Record as "rate-limited" so the dashboard shows the real reason
		// and ConsecutiveFailures doesn't count this toward the circuit breaker.
		rm.Status = "rate-limited"
		rm.Error = runErr.Error()
	case isAuthError:
		// Record as "auth-error" — distinct from "failed" so the dashboard
		// can surface the specific cause. Does NOT count toward circuit breaker
		// since agent-failed would be misleading (the issue itself is fine;
		// the credentials are broken). Operator should investigate the session
		// or configure an independent API key provider (issue #204).
		rm.Status = "auth-error"
		rm.Error = runErr.Error()
	case isNoMarker:
		// No-marker runs are recorded as "no-marker" (distinct from generic
		// "failed") so the dashboard can surface the specific cause. They DO
		// count toward the circuit breaker — the issue stays unlabeled and
		// will re-fire on every subsequent pass otherwise (issue #143).
		rm.Status = "no-marker"
		rm.Error = runErr.Error()
	case runErr != nil:
		rm.Status = "failed"
		rm.Error = runErr.Error()
	case output == "":
		// Empty output: claude exited cleanly but produced nothing. Like
		// no-marker, the issue stays unlabeled and will re-fire. Count toward
		// the circuit breaker so a stuck issue eventually gets agent-failed
		// instead of looping forever (issue #143).
		rm.Status = "skipped-empty"
	default:
		rm.Status = "success"
	}

	// Conditional cleanup: only remove the worktree on success/skip.
	// On failure, preserve it so the next run can resume from the
	// partial work instead of starting from scratch.
	// skipped-empty, no-marker, and auth-error have no partial work worth
	// resuming, so clean up to avoid accumulating stale worktrees.
	if rm.Status == "success" || rm.Status == "skipped-empty" || rm.Status == "no-marker" || rm.Status == "auth-error" {
		cleanup()
	} else {
		fmt.Fprintf(os.Stderr, "%s → preserving worktree for resume on next run: %s\n", prefix, workdir)
	}

	if u, uerr := snapshot.ExtractUsage(filepath.Join(runDir, "events.jsonl")); uerr == nil {
		rm.Usage = u
	}
	if err := snapshot.WriteRunMeta(runDir, rm); err != nil {
		fmt.Fprintf(os.Stderr, "%s ⚠ run meta: %v\n", prefix, err)
	}
	// Upgrade to WARN for non-success statuses so deployment.md patrol's
	// "grep -E ERROR|WARN" actively catches failures (issue #204).
	logFn := runLog.Info
	if rm.Status == "failed" || rm.Status == "auth-error" {
		logFn = runLog.Warn
	}
	logFn("run/end",
		"repo", j.repo,
		"issue", j.sub.Number,
		"op", j.op.Name,
		"status", rm.Status,
		"duration", runDur,
		"outcome", outcome,
		"pr", rm.PRUrl,
	)

	// Post-run automation: auto-approve and auto-merge
	if rm.Status == "success" {
		runPostAutomation(j, outcome, output, prefix)
	}

	switch rm.Status {
	case "success":
		fmt.Printf("%s ✓ done\n", prefix)
		return true, false
	case "rate-limited":
		// Signal the caller to abort the queue; do NOT call checkCircuitBreaker.
		return false, true
	case "auth-error":
		// Do NOT signal queue abort (unlike rate-limit, other issues may still
		// succeed with the same session). Do NOT call checkCircuitBreaker —
		// agent-failed would be misleading for a credential problem.
		fmt.Printf("%s ✗ auth error (check session/API key)\n", prefix)
		return false, false
	case "no-marker":
		fmt.Printf("%s ✗ no outcome marker (circuit breaker counting)\n", prefix)
		checkCircuitBreaker(j, prefix)
		return false, false
	case "skipped-empty":
		fmt.Printf("%s ✗ empty output (circuit breaker counting)\n", prefix)
		checkCircuitBreaker(j, prefix)
		return false, false
	default:
		checkCircuitBreaker(j, prefix)
		return false, false
	}
}

var prURLRE = regexp.MustCompile(`(?:pull|merge_requests)/(\d+)`)

// buildResumeContext produces a markdown section that tells the operator about
// the existing partial work in the worktree. This is injected into the user
// message so claude knows to continue rather than start from scratch.
func buildResumeContext(wt worktreeResult) string {
	var b strings.Builder
	b.WriteString("## ⚠️ Resume Context (previous run was interrupted)\n\n")
	b.WriteString("This worktree contains **partial work from a previous interrupted run**. ")
	b.WriteString("Do NOT start from scratch. Inspect the existing changes and continue where the previous attempt left off.\n\n")
	if wt.BranchName != "" && wt.BranchName != "HEAD" {
		fmt.Fprintf(&b, "**Current branch:** `%s`\n\n", wt.BranchName)
	}
	if wt.DiffStat != "" {
		b.WriteString("**Existing changes:**\n```\n")
		b.WriteString(wt.DiffStat)
		b.WriteString("\n```\n\n")
	}
	b.WriteString("**Instructions:**\n")
	b.WriteString("1. Review the existing uncommitted/committed changes with `git status` and `git diff`\n")
	b.WriteString("2. Understand what was already done and what remains\n")
	b.WriteString("3. Continue the implementation from where it stopped\n")
	b.WriteString("4. If the branch already exists, stay on it — do NOT create a new branch\n")
	b.WriteString("5. If the existing changes look correct, commit and push them\n")
	b.WriteString("6. If they need fixes, fix them before committing\n\n")
	return b.String()
}

// checkCircuitBreaker counts consecutive failures for this (repo, issue)
// and auto-adds `agent-failed` when the threshold is exceeded, preventing
// infinite retry loops on permanently broken issues.
func checkCircuitBreaker(j *runJob, prefix string) {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	maxFails := cfg.Settings.MaxConsecutiveFailures
	if maxFails <= 0 {
		maxFails = 3
	}
	count := snapshot.ConsecutiveFailures(j.repo, j.sub.Number)
	if count < maxFails {
		fmt.Fprintf(os.Stderr, "%s ⚠ failure %d/%d (circuit breaker at %d)\n", prefix, count, maxFails, maxFails)
		return
	}
	fmt.Fprintf(os.Stderr, "%s ✗ circuit breaker: %d consecutive failures, adding agent-failed\n", prefix, count)
	if err := j.client.AddLabel(j.repo, j.sub.Number, "agent-failed"); err != nil {
		fmt.Fprintf(os.Stderr, "%s ⚠ circuit breaker label failed: %v\n", prefix, err)
		return
	}
	// No accompanying comment by design: the failure trail lives in
	// events.jsonl + dashboard runs, and chatter on the issue itself
	// just adds noise users have to scroll past. PM patrol may remove
	// `agent-failed` later if the underlying issue looks recoverable.
}

// runPostAutomation handles auto-approve and auto-merge after an operator completes.
func runPostAutomation(j *runJob, outcome, output, prefix string) {
	// Auto-approve: after evaluate produces agent-evaluated, auto-add ready-for-agent.
	// No accompanying comment by design — the label change itself is the
	// signal, and the issue timeline shows clawflow as the actor. Adding
	// a "Auto-approved by ClawFlow" comment was duplicating that.
	if outcome == "agent-evaluated" && j.repoCfg.AutoApprove {
		fmt.Fprintf(os.Stderr, "%s → auto-approve: adding ready-for-agent\n", prefix)
		if err := j.client.AddLabel(j.repo, j.sub.Number, "ready-for-agent"); err != nil {
			fmt.Fprintf(os.Stderr, "%s ⚠ auto-approve failed: %v\n", prefix, err)
			return
		}
		fmt.Fprintf(os.Stderr, "%s ✓ auto-approved\n", prefix)
	}

	// Auto-merge: after implement produces agent-implemented, wait CI then merge
	if outcome == "agent-implemented" && j.repoCfg.AutoMerge {
		prNum := extractPRNumber(output)
		if prNum == 0 {
			fmt.Fprintf(os.Stderr, "%s ⚠ auto-merge: could not extract PR number from output\n", prefix)
			return
		}
		fmt.Fprintf(os.Stderr, "%s → auto-merge: PR #%d\n", prefix, prNum)

		if j.repoCfg.CIRequired {
			ciTimeout := j.repoCfg.CITimeout
			if ciTimeout <= 0 {
				ciTimeout = 600
			}
			status := waitForCI(j.client, j.repo, prNum, ciTimeout, prefix)
			switch status {
			case vcs.CIStatusSuccess, vcs.CIStatusNone:
				// proceed to merge
			case vcs.CIStatusFailure:
				_ = j.client.PostIssueComment(j.repo, j.sub.Number,
					"🤖 CI failed on PR #"+strconv.Itoa(prNum)+". Skipping auto-merge.")
				fmt.Fprintf(os.Stderr, "%s ✗ CI failed, skipping auto-merge\n", prefix)
				return
			default:
				_ = j.client.PostIssueComment(j.repo, j.sub.Number,
					"🤖 CI did not pass within timeout for PR #"+strconv.Itoa(prNum)+". Skipping auto-merge.")
				fmt.Fprintf(os.Stderr, "%s ✗ CI timeout, skipping auto-merge\n", prefix)
				return
			}
		}

		mergeStatus, err := j.client.GetPRMergeability(j.repo, prNum)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s ⚠ auto-merge: mergeability check failed: %v\n", prefix, err)
			return
		}
		if mergeStatus != vcs.MergeStatusClean {
			_ = j.client.PostIssueComment(j.repo, j.sub.Number,
				"🤖 PR #"+strconv.Itoa(prNum)+" is not mergeable (status: "+string(mergeStatus)+"). Skipping auto-merge.")
			fmt.Fprintf(os.Stderr, "%s ✗ PR not mergeable (%s)\n", prefix, mergeStatus)
			return
		}

		if err := j.client.MergePR(j.repo, prNum); err != nil {
			// Keep the failure comment: the user explicitly enabled
			// auto_merge, the merge attempt failed, and the reason
			// (auth, branch protection, network) isn't otherwise
			// surfaced on the PR. This is the one auto-merge comment
			// that pulls real weight.
			fmt.Fprintf(os.Stderr, "%s ⚠ auto-merge failed: %v\n", prefix, err)
			_ = j.client.PostIssueComment(j.repo, j.sub.Number,
				"🤖 Auto-merge failed for PR #"+strconv.Itoa(prNum)+": "+err.Error())
			return
		}
		// No success comment: the PR's "merged by clawflow-bot" state
		// is already shown in GitHub/GitLab UI. Adding a comment on
		// top was redundant noise.
		fmt.Fprintf(os.Stderr, "%s ✓ auto-merged PR #%d\n", prefix, prNum)

		// Clean up the remote head branch once the merge lands. We read
		// the PR again instead of reusing an earlier fetch because the
		// mergeability check happened before merge and the branch name
		// lives on the PR object anyway. Failures here are non-fatal:
		// the merge already succeeded, and stale branches are a minor
		// housekeeping issue, not something worth surfacing on the issue.
		if pr, err := j.client.GetPR(j.repo, prNum); err != nil {
			fmt.Fprintf(os.Stderr, "%s ⚠ branch cleanup: lookup PR failed: %v\n", prefix, err)
		} else if head := pr.HeadBranch; head != "" && head != j.repoCfg.BaseBranch {
			if err := j.client.DeleteBranch(j.repo, head); err != nil {
				fmt.Fprintf(os.Stderr, "%s ⚠ branch cleanup: delete %s failed: %v\n", prefix, head, err)
			} else {
				fmt.Fprintf(os.Stderr, "%s ✓ deleted branch %s\n", prefix, head)
			}
		}
	}

	// Tracking issue: after decompose creates sub-issues, add progress-check
	// so track-progress fires on the next run.
	if outcome == "agent-decomposed" {
		if err := j.client.AddLabel(j.repo, j.sub.Number, "progress-check"); err != nil {
			fmt.Fprintf(os.Stderr, "%s ⚠ progress-check label failed: %v\n", prefix, err)
		} else {
			fmt.Fprintf(os.Stderr, "%s ✓ progress-check added\n", prefix)
		}
	}

	// Tracking issue: sub-issues still pending — re-add progress-check so
	// track-progress fires again on the next run.
	if outcome == "agent-watching" {
		if err := j.client.AddLabel(j.repo, j.sub.Number, "progress-check"); err != nil {
			fmt.Fprintf(os.Stderr, "%s ⚠ re-add progress-check failed: %v\n", prefix, err)
		} else {
			fmt.Fprintf(os.Stderr, "%s ✓ progress-check re-added (sub-issues pending)\n", prefix)
		}
	}

	// Tracking issue: all sub-issues done — close the parent issue.
	if outcome == "agent-closed" {
		if err := j.client.CloseIssue(j.repo, j.sub.Number); err != nil {
			fmt.Fprintf(os.Stderr, "%s ⚠ auto-close tracking issue failed: %v\n", prefix, err)
		} else {
			fmt.Fprintf(os.Stderr, "%s ✓ tracking issue closed\n", prefix)
		}
	}
}

func extractPRNumber(output string) int {
	m := prURLRE.FindStringSubmatch(output)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func waitForCI(client vcs.Client, repo string, prNum int, timeoutSec int, prefix string) vcs.CIStatus {
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	for time.Now().Before(deadline) {
		status, err := client.GetCIStatus(repo, prNum)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s ⚠ CI status check: %v\n", prefix, err)
			time.Sleep(30 * time.Second)
			continue
		}
		if status != vcs.CIStatusPending {
			return status
		}
		fmt.Fprintf(os.Stderr, "%s ⏳ CI pending, waiting...\n", prefix)
		time.Sleep(30 * time.Second)
	}
	return vcs.CIStatusPending
}

// resolveWorkdir picks the cwd for the claude subprocess and returns a
// cleanup callback.
//
//   - implement / pr-target → per-issue mutable worktree (setupWorktree)
//   - all other operators with local_path → persistent analysis worktree
//     (ensureAnalysisWorktree) reset to origin/<base> before each run
//   - all other operators without local_path → ephemeral tempdir + warning
func resolveWorkdir(op *operator.Operator, repoCfg config.Repo, fullName string, issueNum int, startedAt time.Time, overrideBranch string) (worktreeResult, func(), error) {
	// implement and pr-target operators need a mutable per-issue worktree.
	needsRepo := op.Name == "implement" || op.Trigger.Target == "pr"
	if needsRepo {
		if repoCfg.LocalPath == "" {
			return worktreeResult{}, func() {}, fmt.Errorf("operator %q needs repo local_path but it's empty in config", op.Name)
		}
		return setupWorktree(repoCfg, fullName, issueNum, startedAt, overrideBranch)
	}

	// Analysis operators need read access to the repo source so the LLM can
	// grep/read actual code rather than guessing from issue text alone.
	// Use a persistent analysis worktree that is always reset to origin/<base>
	// before use so the operator sees the latest committed code.
	if repoCfg.LocalPath != "" {
		return ensureAnalysisWorktree(repoCfg, fullName)
	}

	// No local_path configured: fall back to an empty tempdir. Analysis quality
	// will be limited since the operator cannot read source code.
	fmt.Fprintf(os.Stderr, "  ⚠ no local_path configured for %s — operator %q will run without source code context (configure local_path for better analysis quality)\n", fullName, op.Name)
	dir, err := os.MkdirTemp("", "clawflow-op-")
	if err != nil {
		return worktreeResult{}, func() {}, err
	}
	return worktreeResult{Path: dir}, func() { _ = os.RemoveAll(dir) }, nil
}

// analysisWorktreeLocks serializes setup/refresh of analysis worktrees per
// (slug, base) within a single process. Cross-process races are benign since
// fetch+reset is idempotent.
var analysisWorktreeLocks sync.Map // key "slug/base" → *sync.Mutex

func getAnalysisWorktreeLock(slug, base string) *sync.Mutex {
	key := slug + "/" + base
	v, _ := analysisWorktreeLocks.LoadOrStore(key, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// validateLocalPathOrigin checks that the git repository at localPath has an
// origin remote whose URL resolves to fullName (owner/repo). This prevents
// cross-project contamination: if local_path accidentally points at a
// different repo's clone, git would register the analysis worktree entry
// inside the wrong repository's .git/worktrees/ (issue #211).
//
// The check is best-effort: if we cannot determine the origin URL (no git,
// offline, custom setup) we log a warning and allow the operation to proceed
// rather than blocking the operator entirely.
func validateLocalPathOrigin(localPath, fullName string) error {
	out, err := exec.Command("git", "-C", localPath, "remote", "get-url", "origin").Output()
	if err != nil {
		// Can't validate (no remote, offline, non-git dir) — allow with warning.
		fmt.Fprintf(os.Stderr, "  ⚠ cannot verify local_path origin for %s (get-url failed: %v); proceeding\n", fullName, err)
		return nil
	}
	remoteURL := strings.TrimSpace(string(out))
	parsed := extractOwnerRepo(remoteURL)
	if parsed == "" {
		// Unrecognised URL format — allow with warning.
		fmt.Fprintf(os.Stderr, "  ⚠ cannot parse origin URL %q for %s; proceeding without validation\n", remoteURL, fullName)
		return nil
	}
	if !strings.EqualFold(parsed, fullName) {
		return fmt.Errorf(
			"local_path %q origin is %q (parsed: %q), expected %q — "+
				"local_path is misconfigured or shared across repos; "+
				"fix local_path in your clawflow config to avoid cross-project worktree contamination (issue #211)",
			localPath, remoteURL, parsed, fullName,
		)
	}
	return nil
}

// extractOwnerRepo parses owner/repo out of common git remote URL formats:
//   - https://github.com/owner/repo
//   - https://github.com/owner/repo.git
//   - git@github.com:owner/repo.git
//   - https://gitlab.company.com/owner/repo
//
// Returns "" if the format is not recognised.
func extractOwnerRepo(rawURL string) string {
	rawURL = strings.TrimSuffix(rawURL, ".git")

	// SSH: git@host:owner/repo
	if strings.HasPrefix(rawURL, "git@") {
		// git@github.com:owner/repo → owner/repo
		if idx := strings.Index(rawURL, ":"); idx >= 0 {
			return rawURL[idx+1:]
		}
		return ""
	}

	// HTTPS / HTTP
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	// path is "/owner/repo" — strip leading slash and take exactly two segments
	parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// pruneOrphanedAnalysisWorktrees scans ~/.clawflow/worktrees/*/analysis-* and
// removes any analysis worktree whose slug no longer corresponds to a
// configured + enabled repo. This GC runs during each `clawflow run` reconcile
// pass so that repos removed from config don't leave permanent git worktree
// entries cluttering the source clone (issue #211).
//
// The approach:
//  1. Build the set of active slugs from cfg.
//  2. Scan ~/.clawflow/worktrees/* for slug dirs that are NOT in the active set.
//  3. For each orphaned slug dir, remove any analysis-* subdirs via
//     removeAnalysisWorktree.
//
// Returns the number of worktrees removed.
func pruneOrphanedAnalysisWorktrees(cfg *config.Config) int {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0
	}
	wtRoot := filepath.Join(home, ".clawflow", "worktrees")

	// Build set of active slug directories from current config.
	activeSlugs := make(map[string]struct{}, len(cfg.Repos))
	for fullName, repoCfg := range cfg.Repos {
		if !repoCfg.Enabled {
			continue
		}
		activeSlugs[strings.ReplaceAll(fullName, "/", "__")] = struct{}{}
	}

	slugDirs, err := os.ReadDir(wtRoot)
	if err != nil {
		return 0 // worktrees dir not yet created
	}

	removed := 0
	for _, slugEntry := range slugDirs {
		if !slugEntry.IsDir() {
			continue
		}
		slug := slugEntry.Name()
		if _, active := activeSlugs[slug]; active {
			continue // still in use — keep it
		}
		// Orphaned slug: remove all analysis-* worktrees inside it.
		slugDir := filepath.Join(wtRoot, slug)
		analysisEntries, err := os.ReadDir(slugDir)
		if err != nil {
			continue
		}
		for _, entry := range analysisEntries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "analysis-") {
				continue
			}
			wtPath := filepath.Join(slugDir, entry.Name())
			if removeAnalysisWorktree(wtPath) {
				fmt.Fprintf(os.Stderr, "  ✓ pruned orphaned analysis worktree: %s\n", wtPath)
				removed++
			}
		}
	}
	return removed
}

// removeAnalysisWorktree removes a single analysis worktree directory along
// with its git registration. It reads the .git pointer file inside the
// worktree to locate the owning repository, then calls
// `git worktree remove --force` so the .git/worktrees/<name> metadata is also
// cleaned up. Falls back to `os.RemoveAll` if the git step fails.
//
// Returns true if any cleanup was performed.
func removeAnalysisWorktree(wtPath string) bool {
	// Read the .git pointer file to find the owning clone's git dir.
	// Format: "gitdir: /path/to/clone/.git/worktrees/<name>\n"
	gitFile := filepath.Join(wtPath, ".git")
	data, err := os.ReadFile(gitFile)
	if err == nil {
		line := strings.TrimSpace(string(data))
		if after, ok := strings.CutPrefix(line, "gitdir:"); ok {
			// mainGitWorktreesEntry = /path/to/clone/.git/worktrees/<name>
			mainGitWorktreesEntry := strings.TrimSpace(after)
			// mainGitDir = /path/to/clone/.git
			mainGitDir := filepath.Dir(filepath.Dir(mainGitWorktreesEntry))
			// mainRepoPath = /path/to/clone
			mainRepoPath := filepath.Dir(mainGitDir)
			rm := exec.Command("git", "-C", mainRepoPath, "worktree", "remove", "--force", wtPath)
			rm.Stdout = os.Stderr
			rm.Stderr = os.Stderr
			if rmErr := rm.Run(); rmErr == nil {
				// git worktree remove already deleted the directory.
				return true
			}
			// git step failed — fall through to os.RemoveAll and manual prune.
		}
	}

	// Fallback: delete the directory directly.
	if _, statErr := os.Stat(wtPath); statErr == nil {
		if removeErr := os.RemoveAll(wtPath); removeErr != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ could not remove analysis worktree dir %s: %v\n", wtPath, removeErr)
			return false
		}
	}
	return true
}

// ensureAnalysisWorktree provisions or refreshes a persistent read-only
// analysis worktree at ~/.clawflow/worktrees/<slug>/analysis-<base>.
//
// Unlike setupWorktree (which creates a per-issue ephemeral worktree for
// implement), this worktree is long-lived and shared across all analysis
// operators for the same repo+branch. It is always reset to origin/<base>
// before use so the operator sees the latest committed code.
//
// Fetch failure blocks the operator — we refuse to run analysis on stale or
// absent code rather than silently producing low-quality output.
//
// The returned cleanup is a no-op: the worktree persists for future runs.
func ensureAnalysisWorktree(repoCfg config.Repo, fullName string) (worktreeResult, func(), error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return worktreeResult{}, func() {}, fmt.Errorf("home dir: %w", err)
	}

	base := repoCfg.BaseBranch
	if base == "" {
		base = "main"
	}
	slug := strings.ReplaceAll(fullName, "/", "__")
	localPath := repoCfg.LocalPath
	wtPath := filepath.Join(home, ".clawflow", "worktrees", slug, "analysis-"+base)

	// Validate that localPath's git origin matches fullName before we
	// register any worktree against it. A misconfigured local_path (e.g.
	// pointing at the wrong clone, or shared across repos) would cause
	// git to register the worktree entry inside the wrong repository's
	// .git/worktrees/, producing the cross-project contamination described
	// in issue #211.
	if err := validateLocalPathOrigin(localPath, fullName); err != nil {
		return worktreeResult{}, func() {}, err
	}

	// Serialize setup/refresh within this process so concurrent analysis
	// operators on the same repo don't race on fetch+reset.
	mu := getAnalysisWorktreeLock(slug, base)
	mu.Lock()
	defer mu.Unlock()

	// Check whether the worktree already exists (has a .git pointer file).
	_, statErr := os.Stat(filepath.Join(wtPath, ".git"))
	worktreeExists := statErr == nil

	if !worktreeExists {
		// First time: fetch then create the worktree.
		if err := os.MkdirAll(filepath.Dir(wtPath), 0o755); err != nil {
			return worktreeResult{}, func() {}, fmt.Errorf("mkdir analysis worktree parent: %w", err)
		}

		fmt.Fprintf(os.Stderr, "  → analysis worktree: initializing at %s\n", wtPath)
		fmt.Fprintf(os.Stderr, "  → analysis worktree: fetching origin/%s\n", base)
		if err := runGitWithRetry(func() *exec.Cmd {
			return exec.Command("git", "-C", localPath, "fetch", "origin", base)
		}); err != nil {
			return worktreeResult{}, func() {}, fmt.Errorf("git fetch origin/%s: %w — cannot initialize analysis worktree without network access", base, err)
		}

		if err := runGitWithRetry(func() *exec.Cmd {
			return exec.Command("git", "-C", localPath, "worktree", "add", "--detach", wtPath, "origin/"+base)
		}); err != nil {
			// Fall back to the local base branch ref (e.g. brand-new clone
			// that hasn't pushed yet and has no origin/<base> remote ref).
			fmt.Fprintf(os.Stderr, "  ⚠ worktree add origin/%s failed, falling back to local %s\n", base, base)
			if err2 := runGitWithRetry(func() *exec.Cmd {
				return exec.Command("git", "-C", localPath, "worktree", "add", "--detach", wtPath, base)
			}); err2 != nil {
				return worktreeResult{}, func() {}, fmt.Errorf("git worktree add failed (origin/%s: %v; %s: %w)", base, err, base, err2)
			}
		}
		fmt.Fprintf(os.Stderr, "  ✓ analysis worktree initialized (detached HEAD at origin/%s)\n", base)
	} else {
		// Existing worktree: fetch + reset to latest origin/<base>.
		fmt.Fprintf(os.Stderr, "  → analysis worktree: refreshing to origin/%s\n", base)
		if err := runGitWithRetry(func() *exec.Cmd {
			return exec.Command("git", "-C", localPath, "fetch", "origin", base)
		}); err != nil {
			return worktreeResult{}, func() {}, fmt.Errorf("git fetch origin/%s: %w — analysis blocked to avoid stale-code evaluation", base, err)
		}

		resetCmd := exec.Command("git", "-C", wtPath, "reset", "--hard", "origin/"+base)
		resetCmd.Stdout = os.Stderr
		resetCmd.Stderr = os.Stderr
		if err := resetCmd.Run(); err != nil {
			return worktreeResult{}, func() {}, fmt.Errorf("git reset --hard origin/%s: %w", base, err)
		}

		cleanCmd := exec.Command("git", "-C", wtPath, "clean", "-fdx")
		cleanCmd.Stdout = os.Stderr
		cleanCmd.Stderr = os.Stderr
		if err := cleanCmd.Run(); err != nil {
			// Non-fatal: untracked files left behind won't affect analysis.
			fmt.Fprintf(os.Stderr, "  ⚠ git clean -fdx (non-fatal): %v\n", err)
		}
		fmt.Fprintf(os.Stderr, "  ✓ analysis worktree refreshed (origin/%s)\n", base)
	}

	// No cleanup: the analysis worktree is persistent across runs.
	return worktreeResult{Path: wtPath}, func() {}, nil
}

// worktreeResult holds the outcome of setupWorktree so the caller knows
// whether this is a fresh worktree or a resumed one with prior work.
type worktreeResult struct {
	Path       string // absolute path to the worktree
	Resumed    bool   // true if we reused an existing worktree with WIP
	DiffStat   string // `git diff --stat` output (non-empty only when Resumed)
	BranchName string // branch checked out in the worktree (non-empty only when Resumed)
}

// findExistingWorktree scans ~/.clawflow/worktrees/<slug>/issue-<N>-* for a
// worktree that has uncommitted changes or commits ahead of origin/<base>.
// Returns the path and a short diff summary if found, or ("", "", "") if none.
func findExistingWorktree(parent string, issueNum int, localPath, base string) (wtPath, diffStat, branch string) {
	prefix := fmt.Sprintf("issue-%d-", issueNum)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return "", "", ""
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		candidate := filepath.Join(parent, e.Name())
		// Check it's a valid git worktree (has .git file)
		if _, err := os.Stat(filepath.Join(candidate, ".git")); err != nil {
			continue
		}
		// Check for uncommitted changes
		diffCmd := exec.Command("git", "-C", candidate, "diff", "--stat", "HEAD")
		diffOut, _ := diffCmd.Output()
		// Check for staged changes
		stagedCmd := exec.Command("git", "-C", candidate, "diff", "--stat", "--cached")
		stagedOut, _ := stagedCmd.Output()
		// Check for untracked files
		untrackedCmd := exec.Command("git", "-C", candidate, "ls-files", "--others", "--exclude-standard")
		untrackedOut, _ := untrackedCmd.Output()
		// Check current branch
		branchCmd := exec.Command("git", "-C", candidate, "rev-parse", "--abbrev-ref", "HEAD")
		branchOut, _ := branchCmd.Output()
		currentBranch := strings.TrimSpace(string(branchOut))

		hasUncommitted := len(strings.TrimSpace(string(diffOut))) > 0 ||
			len(strings.TrimSpace(string(stagedOut))) > 0 ||
			len(strings.TrimSpace(string(untrackedOut))) > 0

		// Check for commits ahead of origin/base (branch was created and committed to)
		hasCommitsAhead := false
		if currentBranch != "" && currentBranch != "HEAD" {
			aheadCmd := exec.Command("git", "-C", candidate, "log", "--oneline", "origin/"+base+"..HEAD")
			aheadOut, _ := aheadCmd.Output()
			hasCommitsAhead = len(strings.TrimSpace(string(aheadOut))) > 0
		}

		if hasUncommitted || hasCommitsAhead {
			// Build a combined diff stat for the resume context
			var statParts []string
			if s := strings.TrimSpace(string(diffOut)); s != "" {
				statParts = append(statParts, s)
			}
			if s := strings.TrimSpace(string(stagedOut)); s != "" {
				statParts = append(statParts, "(staged)\n"+s)
			}
			if s := strings.TrimSpace(string(untrackedOut)); s != "" {
				statParts = append(statParts, "(untracked)\n"+s)
			}
			if hasCommitsAhead {
				aheadCmd := exec.Command("git", "-C", candidate, "log", "--oneline", "origin/"+base+"..HEAD")
				aheadOut, _ := aheadCmd.Output()
				statParts = append(statParts, "(commits ahead of origin/"+base+")\n"+strings.TrimSpace(string(aheadOut)))
			}
			return candidate, strings.Join(statParts, "\n"), currentBranch
		}
	}
	return "", "", ""
}

// isGitLockError reports whether the combined output of a failed git command
// indicates a transient lock conflict. These errors are safe to retry; all
// other errors should fail immediately.
//
// Common patterns across git versions/locales:
//   - "index.lock: File exists" — index lock left by a previous process
//   - "cannot lock ref" — ref lock conflict
//   - "Another git process seems to be running" — general process lock
//   - ".lock" — catch-all for any lock-file mention
func isGitLockError(output string) bool {
	for _, pattern := range []string{
		"index.lock",
		"cannot lock ref",
		"Another git process seems to be running",
		".lock: File exists",
	} {
		if strings.Contains(output, pattern) {
			return true
		}
	}
	return false
}

// runGitWithRetry executes a git command produced by makeCmd(). On exit
// failure it inspects the combined output: if the error matches a transient
// git lock conflict it retries up to maxGitRetries times with exponential
// backoff (baseGitDelay*2^n + up-to-50%-jitter). Non-lock errors fail
// immediately without retrying.
//
// All output is written to os.Stderr for observability regardless of
// whether the command ultimately succeeds or fails.
//
// makeCmd is called once per attempt so each retry gets a fresh *exec.Cmd
// (reusing a Cmd after Run returns is not supported by the standard library).
// overallRunBudget returns the wall-clock budget for a whole `clawflow
// run` process before the self-watchdog force-exits it. The per-operator
// timeout already bounds each claude/operator; this bounds everything
// else (scan, git fetch/push, pilot waves, snapshot writes) as a whole.
// It is 2× the per-operator budget, floored at per-op + 30 minutes, so a
// healthy multi-operator run never trips it. Kept strictly below
// api.TriggerRun's watchdog so a slow run self-terminates (with a stack
// dump) before the external process-level kill.
func overallRunBudget(perOp time.Duration) time.Duration {
	if perOp <= 0 {
		perOp = 60 * time.Minute
	}
	budget := perOp * 2
	if floor := perOp + 30*time.Minute; budget < floor {
		budget = floor
	}
	return budget
}

// dumpGoroutineStacks writes a full goroutine stack trace to
// ~/.clawflow/logs/run-stackdump-<ts>.txt. Called by the self-watchdog
// when a run overruns its budget so the next investigation can see the
// exact blocked call — the missing evidence noted in issue #216. Best
// effort: errors are reported to stderr but never block the exit path.
func dumpGoroutineStacks(lg *clog.Logger) {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠ stack dump: cannot resolve home dir: %v\n%s\n", err, buf[:n])
		return
	}
	dir := filepath.Join(home, ".clawflow", "logs")
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		fmt.Fprintf(os.Stderr, "  ⚠ stack dump: mkdir %s: %v\n%s\n", dir, mkErr, buf[:n])
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("run-stackdump-%s.txt", time.Now().UTC().Format("20060102T150405Z")))
	if wErr := os.WriteFile(path, buf[:n], 0o644); wErr != nil {
		fmt.Fprintf(os.Stderr, "  ⚠ stack dump: write %s: %v\n%s\n", path, wErr, buf[:n])
		return
	}
	fmt.Fprintf(os.Stderr, "  → goroutine stack dump written to %s\n", path)
	if lg != nil {
		lg.Warn("run/watchdog", "stackdump", path)
	}
}

// gitAttemptTimeout bounds a single git subprocess invocation inside
// runGitWithRetry. A package var (not const) so tests can shrink it.
var gitAttemptTimeout = 3 * time.Minute

// hardenedGitEnv augments a git command's environment so it can never
// block on interactive input. GIT_TERMINAL_PROMPT=0 makes git fail
// instead of prompting for HTTP credentials; the BatchMode/ConnectTimeout
// SSH options do the same for git-over-SSH (no passphrase prompt, bounded
// TCP connect). base may be nil, in which case the current environment is
// used. See issue #216 — the run hung as an `S+` foreground process with
// ~0 CPU, the classic signature of git waiting on a credential prompt.
func hardenedGitEnv(base []string) []string {
	if base == nil {
		base = os.Environ()
	}
	return append(base,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=10",
	)
}

// runBoundedGit runs a git command with a hard wall-clock ceiling. The
// command is started in its own process group so that on timeout we can
// SIGKILL the entire group (git plus any ssh/credential-helper children),
// not just the git parent. Returns the command's own error on normal
// completion, or a descriptive timeout error if the ceiling was hit.
// timeout <= 0 means "no ceiling" (delegates straight to cmd.Run).
func runBoundedGit(cmd *exec.Cmd, timeout time.Duration) error {
	if timeout <= 0 {
		return cmd.Run()
	}
	// Own process group so a negative-PID kill reaps descendants too.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		if cmd.Process != nil {
			// Negative PID targets the whole process group.
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done // reap the now-killed process so we don't leak it
		return fmt.Errorf("git command exceeded %s and was killed (no network response or stuck on credentials)", timeout)
	}
}

func runGitWithRetry(makeCmd func() *exec.Cmd) error {
	const maxGitRetries = 5
	const baseGitDelay = 100 * time.Millisecond

	for attempt := 0; attempt <= maxGitRetries; attempt++ {
		cmd := makeCmd()
		var buf bytes.Buffer
		// Tee output: observable on stderr AND captured for error classification.
		mw := io.MultiWriter(os.Stderr, &buf)
		cmd.Stdout = mw
		cmd.Stderr = mw
		// Never let a git subprocess block forever. Two complementary
		// guards (issue #216):
		//   1. hardenedGitEnv disables interactive credential/SSH prompts
		//      so git fails fast instead of waiting on a TTY that will
		//      never answer (the most likely cause of the 8h hang).
		//   2. runBoundedGit kills the whole git process group if the
		//      attempt overruns gitAttemptTimeout, so a black-holed
		//      network read can't wedge the run indefinitely.
		cmd.Env = hardenedGitEnv(cmd.Env)

		err := runBoundedGit(cmd, gitAttemptTimeout)
		if err == nil {
			return nil
		}

		output := buf.String()
		if !isGitLockError(output) {
			// Permanent error (incl. attempt timeout) — return
			// immediately, no retry. A timeout won't fix itself by
			// retrying and would only delay surfacing the failure.
			return err
		}
		if attempt == maxGitRetries {
			return fmt.Errorf("%w (retried %d times after git lock conflicts)", err, maxGitRetries)
		}

		// Exponential backoff: baseGitDelay * 2^attempt, capped at 5 s,
		// plus up to 50% random jitter to reduce thundering-herd.
		delay := baseGitDelay * time.Duration(1<<uint(attempt))
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
		jitter := time.Duration(rand.Int63n(int64(delay / 2))) //nolint:gosec
		delay += jitter

		fmt.Fprintf(os.Stderr, "  ⚠ git lock conflict (attempt %d/%d), retrying in %v\n",
			attempt+1, maxGitRetries, delay.Round(time.Millisecond))
		time.Sleep(delay)
	}
	return nil // unreachable
}

// setupWorktree provisions a git worktree for the implement operator.
//
// Resume logic: before creating a fresh worktree, it scans for an existing
// worktree from a previous interrupted run that has uncommitted changes or
// commits ahead of origin/<base>. If found, it reuses that worktree so the
// operator can continue where it left off instead of starting from scratch.
//
// Fresh path: ~/.clawflow/worktrees/<repo-slug>/issue-<N>-<ts>, starting on
// detached HEAD at origin/<base_branch>.
//
// Cleanup removes the worktree (force) but leaves any branches the operator
// created alone — pushed branches stay locally for the user to inspect/delete.
//
// overrideBranch, when non-empty, replaces repoCfg.BaseBranch for this single
// run only — it is the integration point for the ClawFlow-Base-Branch: marker
// (see ParseBaseBranchOverride in basebranch.go) and is never persisted.
//
// NOTE: this override intentionally does NOT propagate to ensureAnalysisWorktree.
// Analysis worktrees are long-lived and shared across all issues for the same
// (repo, branch) pair; a per-issue override would either pollute that shared
// worktree or require a separate per-issue analysis worktree (out of scope).
func setupWorktree(repoCfg config.Repo, fullName string, issueNum int, startedAt time.Time, overrideBranch string) (worktreeResult, func(), error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return worktreeResult{}, func() {}, fmt.Errorf("home dir: %w", err)
	}
	slug := strings.ReplaceAll(fullName, "/", "__")
	parent := filepath.Join(home, ".clawflow", "worktrees", slug)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return worktreeResult{}, func() {}, fmt.Errorf("mkdir worktree parent: %w", err)
	}

	// Resolve base branch: per-issue override takes highest priority,
	// then the repo config value, then the hardcoded "main" default.
	base := overrideBranch
	if base == "" {
		base = repoCfg.BaseBranch
	}
	if base == "" {
		base = "main"
	}
	localPath := repoCfg.LocalPath

	// Resume: check for an existing worktree with work-in-progress.
	if existingPath, diffStat, branch := findExistingWorktree(parent, issueNum, localPath, base); existingPath != "" {
		fmt.Fprintf(os.Stderr, "  ✓ resuming existing worktree: %s (branch: %s)\n", existingPath, branch)
		result := worktreeResult{
			Path:       existingPath,
			Resumed:    true,
			DiffStat:   diffStat,
			BranchName: branch,
		}
		cleanup := func() {
			fmt.Fprintln(os.Stderr, "  → cleanup: removing worktree")
			rm := exec.Command("git", "-C", localPath, "worktree", "remove", "--force", existingPath)
			rm.Stdout = os.Stderr
			rm.Stderr = os.Stderr
			if err := rm.Run(); err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ worktree remove failed: %v\n", err)
				return
			}
			fmt.Fprintln(os.Stderr, "  ✓ worktree removed")
		}
		return result, cleanup, nil
	}

	// Fresh worktree: create a new one at issue-<N>-<ts>.
	ts := startedAt.UTC().Format("2006-01-02T15-04-05Z")
	wtPath := filepath.Join(parent, fmt.Sprintf("issue-%d-%s", issueNum, ts))

	// Best-effort fetch to align origin/<base> with the remote. A repo
	// without network reachability (offline dev, private bastion) should
	// still be able to spin up a worktree at the local origin/<base>.
	// runGitWithRetry handles transient git lock conflicts from concurrent
	// parallel implement jobs on the same repo.
	fmt.Fprintf(os.Stderr, "  → setup: fetching origin/%s\n", base)
	if err := runGitWithRetry(func() *exec.Cmd {
		return exec.Command("git", "-C", localPath, "fetch", "origin", base)
	}); err != nil {
		fmt.Fprintf(os.Stderr, "  ⚠ fetch failed (continuing): %v\n", err)
	}

	fmt.Fprintf(os.Stderr, "  → setup: creating worktree at %s\n", wtPath)
	if err := runGitWithRetry(func() *exec.Cmd {
		return exec.Command("git", "-C", localPath, "worktree", "add", "--detach", wtPath, "origin/"+base)
	}); err != nil {
		// Fall back to the local base branch ref. Brand-new clones may
		// not have origin/<base> yet (e.g. the user just `git init`'d
		// and added a remote without pushing anything).
		fmt.Fprintf(os.Stderr, "  ⚠ worktree add origin/%s failed, falling back to local %s\n", base, base)
		if err2 := runGitWithRetry(func() *exec.Cmd {
			return exec.Command("git", "-C", localPath, "worktree", "add", "--detach", wtPath, base)
		}); err2 != nil {
			return worktreeResult{}, func() {}, fmt.Errorf("git worktree add failed (origin/%s: %v; %s: %w)", base, err, base, err2)
		}
	}
	fmt.Fprintf(os.Stderr, "  ✓ worktree ready (detached HEAD at origin/%s)\n", base)

	result := worktreeResult{Path: wtPath}
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
	return result, cleanup, nil
}
