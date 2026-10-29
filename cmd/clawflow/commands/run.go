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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	rootmod "github.com/zhoushoujianwork/clawflow"
	"github.com/zhoushoujianwork/clawflow/internal/api"
	"github.com/zhoushoujianwork/clawflow/internal/branch"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/gitexec"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/pilot"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
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
// (Opus by default). Lightweight operators (reply-comment,
// reply-question, track-progress) use the cheap light model (Haiku by
// default). Everything else (implement, user-supplied skills) gets the
// operator model (Sonnet by default).
func roleForOperator(opName string) string {
	if strings.HasPrefix(opName, "evaluate-") {
		return config.RoleEval
	}
	switch opName {
	case "reply-comment", "reply-question", "track-progress":
		return config.RoleLight
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
		// A failed pass returns an error for the exit code; usage text is noise.
		SilenceUsage: true,
		Long: `Execute one pass of the operator loop:
  - for each enabled repo, list open issues
  - for each issue, match against registered operators
  - on first match: execute through Offload, post the result, apply the outcome label

Concurrency is gated by an in-process per-issue mutex and machine-wide
agent slots: settings.max_concurrent_agents (default 4) is shared by all
passes, including implement, repair and acceptance. The "implement" operator is additionally serialized
per-repo because it shares a local git clone. Pass --repo and/or
--issue to narrow the scan.

Targeted run: --repo owner/repo --issue N works on a configured repo even
when it is disabled (so the web auto-run scheduler never sees it). Only that
issue is dispatched, and only PRs that close it are considered by the
acceptance harvest and the auto-merge sweep. Unknown repos still fail, and
Host affinity is enforced by worker task tags.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if onlyIssue != 0 && onlyRepo == "" {
				return fmt.Errorf("--issue requires --repo")
			}
			// PostgreSQL is required: fail fast before any work.
			release, err := gateStore(cmd.Context())
			if err != nil {
				return err
			}
			defer release()
			return runOnce(cmd.Context(), onlyRepo, onlyIssue, timeout)
		},
	}
	cmd.Flags().StringVar(&onlyRepo, "repo", "", "Restrict to a single repo (owner/repo); default: all enabled repos (with --issue the repo may be disabled)")
	cmd.Flags().IntVar(&onlyIssue, "issue", 0, "Restrict to a single issue number (requires --repo)")
	cmd.Flags().DurationVar(&timeout, "timeout", 60*time.Minute, "Per-operator Offload execution timeout")
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

// resolveRunRepos returns the repos a pass scans and, for a targeted run on a
// disabled repo, that repo's name as hidden. A targeted run (--repo plus
// --issue) may name any configured repo, enabled or not; every other run keeps
// requiring the repo to be enabled. A hidden repo is added to the returned map
// so scanning and execution use the normal path, and callers keep it out of
// the dashboard snapshots.
func resolveRunRepos(cfg *config.Config, onlyRepo string, onlyIssue int) (repos map[string]config.Repo, hidden string, err error) {
	repos = cfg.EnabledRepos()
	if onlyRepo == "" {
		return repos, "", nil
	}
	if _, ok := repos[onlyRepo]; ok {
		return repos, "", nil
	}
	if onlyIssue != 0 {
		repoCfg, ok := cfg.Repos[onlyRepo]
		if !ok {
			return nil, "", fmt.Errorf("repo %q not found in config", onlyRepo)
		}
		repos[onlyRepo] = repoCfg
		return repos, onlyRepo, nil
	}
	return nil, "", fmt.Errorf("repo %q not found or not enabled", onlyRepo)
}

// Run-lock hooks and lock-wait tuning for targeted runs; variables so tests
// can replace them.
var (
	acquireRepoRunLock = snapshot.AcquireRepoRunLock
	releaseRepoRunLock = snapshot.ReleaseRepoRunLock
	targetedLockWait   = 30 * time.Minute
	runLockPoll        = 5 * time.Second
)

// passLocks tracks the per-repo run locks one pass holds so every exit path
// (normal return, signal handler, self-watchdog) releases exactly those.
type passLocks struct {
	mu    sync.Mutex
	repos map[string]bool
}

func (p *passLocks) add(repo string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.repos == nil {
		p.repos = map[string]bool{}
	}
	p.repos[repo] = true
}

func (p *passLocks) has(repo string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.repos[repo]
}

// releaseAll releases every held lock. Safe to call more than once.
func (p *passLocks) releaseAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for repo := range p.repos {
		releaseRepoRunLock(repo)
		delete(p.repos, repo)
	}
}

// logRepoLocked records that a repo was skipped because another pass (or a
// legacy global-lock holder) is running it.
func logRepoLocked(lg *clog.Logger, repo string, lockErr error) {
	fields := []any{"pid", os.Getpid(), "reason", "repo_locked", "repo", repo}
	if holder := snapshot.RepoRunLockHolder(repo); holder != nil {
		fields = append(fields, "holder_pid", holder.PID, "holder_started", holder.StartedAt.Format(time.RFC3339))
		fmt.Fprintf(os.Stderr, "⚠ %s: clawflow run already active (pid=%d, started=%s) — skipping\n",
			repo, holder.PID, holder.StartedAt.Format(time.RFC3339))
	} else {
		if lockErr != nil {
			fields = append(fields, "err", lockErr.Error())
		}
		fmt.Fprintf(os.Stderr, "⚠ %s: %v — skipping\n", repo, lockErr)
	}
	lg.Info("run/skip", fields...)
}

// acquireRepoLockForPass takes one repo's run lock. An untargeted pass that
// loses the race reports skip=true (exit 0, the next tick retries). A targeted
// run (--repo + --issue) is an explicit request to process one issue, so
// exiting 0 would look like the work was done: it waits up to targetedLockWait
// for the pass holding that repo and returns an error on timeout.
func acquireRepoLockForPass(ctx context.Context, lg *clog.Logger, pl *passLocks, repo string, targeted bool) (skip bool, err error) {
	lockErr := acquireRepoRunLock(repo, Version)
	if lockErr == nil {
		pl.add(repo)
		return false, nil
	}
	if targeted {
		fmt.Fprintf(os.Stderr, "⚠ %v — waiting up to %s for it to finish\n", lockErr, targetedLockWait)
		lg.Info("run/lock_wait", "pid", os.Getpid(), "repo", repo, "reason", lockErr.Error(), "max_wait", targetedLockWait.String())
		deadline := time.Now().Add(targetedLockWait)
		for lockErr != nil && time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(runLockPoll):
			}
			lockErr = acquireRepoRunLock(repo, Version)
		}
		if lockErr == nil {
			pl.add(repo)
			return false, nil
		}
		lg.Warn("run/lock_timeout", "pid", os.Getpid(), "repo", repo, "reason", lockErr.Error(), "waited", targetedLockWait.String())
		return false, fmt.Errorf("another clawflow run is still active for %s after waiting %s: %w", repo, targetedLockWait, lockErr)
	}
	logRepoLocked(lg, repo, lockErr)
	return true, nil
}

// lockUnscopedRepos tries the run lock of every candidate repo and returns the
// repos whose lock this pass now holds. The others are logged as skipped.
func lockUnscopedRepos(lg *clog.Logger, pl *passLocks, candidates []string) []string {
	var got []string
	for _, repo := range candidates {
		if err := acquireRepoRunLock(repo, Version); err != nil {
			logRepoLocked(lg, repo, err)
			continue
		}
		pl.add(repo)
		got = append(got, repo)
	}
	return got
}

// repoRunsHere shares scan eligibility with the web scheduler.
func repoRunsHere(settings config.Settings, repoCfg config.Repo, hostname string) bool {
	return settings.RunsOnHost(repoCfg, hostname)
}

// syncLocked runs a config sync step (auto-pull / auto-push) under the
// cross-process config sync lock. Concurrent repo-scoped passes would
// otherwise rewrite config.yaml at the same time; a sync that cannot get the
// lock within a minute is skipped (it is best-effort and retried next pass).
func syncLocked(fn func() bool) bool {
	ok := false
	if !snapshot.WithConfigSyncLock(time.Minute, func() { ok = fn() }) {
		return false
	}
	return ok
}

func runOnce(ctx context.Context, onlyRepo string, onlyIssue int, timeout time.Duration) error {
	// Global collection for all issues from all repos
	var globalIssues []snapshot.IssueEntry
	confirmedMerges.reset()

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

	// Per-repo run locks: a pass locks only the repositories it processes, so
	// a long pass for repo A (operators, 7-12 min acceptance harvest) does not
	// block repos B and C. A repo-scoped pass (--repo R) locks R here; an
	// unscoped pass locks its candidates once the config is loaded below. On
	// contention an untargeted pass logs run/skip and exits cleanly so
	// schedulers don't treat the skip as a failure; a targeted run waits for
	// the pass holding R (see acquireRepoLockForPass). Locks of crashed
	// processes are reclaimed by the PID-liveness check, and a live holder of
	// the legacy global run lock (older binary) locks every repo.
	pl := &passLocks{}
	defer pl.releaseAll()
	if onlyRepo != "" {
		if skip, err := acquireRepoLockForPass(ctx, lg, pl, onlyRepo, onlyIssue != 0); err != nil {
			return err
		} else if skip {
			return nil
		}
	}
	// For unhandled signals (SIGINT/SIGTERM) the locks are also released
	// explicitly before os.Exit so the next tick isn't blocked by a stale file.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sigDone := make(chan struct{})
	go func() {
		select {
		case <-sigCh:
			pl.releaseAll()
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
	// watchdogDeadline is the wall-clock moment the self-watchdog below force-
	// exits the process. Threaded into pilot.Schedule (Phase 4) so it can
	// refuse to start a wake that plainly won't fit in what's left — see the
	// call site below and issue #325.
	watchdogDeadline := time.Now().Add(overall)
	selfWatchdog := time.AfterFunc(overall, func() {
		lg.Warn("run/watchdog", "pid", os.Getpid(), "msg", "overall run budget exceeded — likely a hung git/network call; dumping stacks and exiting", "budget", overall.String())
		fmt.Fprintf(os.Stderr, "\n⚠ clawflow run exceeded its overall budget %s — likely a hung git/network call.\n  Dumping goroutine stacks and exiting (issue #216 safeguard).\n", overall)
		dumpGoroutineStacks(lg)
		// Kill whatever claude subprocess is still in flight (operator or
		// Pilot wake) BEFORE exiting. Without this, os.Exit orphans it: it
		// keeps running, keeps spending, and keeps writing to the VCS with
		// no meta.json ever reaching a terminal status because the parent
		// that would write it is already gone (issue #325). The budget
		// tripping here does not mean the child is hung — a Pilot wake is
		// often a legitimate long-running claude call that simply didn't
		// fit in what was left of the overall budget; killing it is about
		// making sure it doesn't run and write unrecorded, not about it
		// having misbehaved.
		operator.TerminateAllActiveCmds()
		pl.releaseAll()
		// os.Exit skips the deferred run/exit, so emit it here with the
		// reason — this is the observable signature the watchdog path must
		// leave behind (issue #221 acceptance: run/exit reason=timeout).
		lg.Info("run/exit", "pid", os.Getpid(), "reason", "timeout", "budget", overall.String())
		os.Exit(1)
	})
	defer selfWatchdog.Stop()

	// Auto-pull: sync config from Gist before scanning so this machine
	// picks up any changes pushed from other machines (e.g. new repos,
	// updated settings). Best-effort: if sync is not configured or the
	// network is unavailable, we continue with the local config.
	if syncLocked(api.AutoPull) {
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

	// Publish on every exit after configuration is available, including failed passes.
	defer func() {
		if err := writePassPipeline(); err != nil {
			fmt.Fprintf(os.Stderr, "snapshot pipeline: %v\n", err)
		}
	}()

	allRepos, hiddenRepo, err := resolveRunRepos(cfg, onlyRepo, onlyIssue)
	if err != nil {
		return err
	}
	// Resolve the hostname for shared scheduler compatibility. Host affinity
	// is enforced only by worker task tags.
	hostname, _ := os.Hostname()

	// Narrow the pass to the repos whose run lock it holds. A repo-scoped
	// pass already holds onlyRepo's lock and scans just that repo; an
	// unscoped pass tries every repo it would process and keeps the ones it
	// won. Repos locked by other passes are left to them and survive in the
	// snapshots via preserveRepos below.
	var candidates []string
	for name, repoCfg := range allRepos {
		if repoRunsHere(cfg.Settings, repoCfg, hostname) {
			candidates = append(candidates, name)
		}
	}
	sort.Strings(candidates)
	scanRepos := make(map[string]config.Repo, len(allRepos))
	if onlyRepo != "" {
		scanRepos[onlyRepo] = allRepos[onlyRepo]
	} else {
		for _, name := range lockUnscopedRepos(lg, pl, candidates) {
			scanRepos[name] = allRepos[name]
		}
		if len(candidates) > 0 && len(scanRepos) == 0 {
			lg.Info("run/skip", "pid", os.Getpid(), "reason", "all_repos_locked", "repos", len(candidates))
			return nil
		}
	}
	// Enabled repos this machine processes but this pass does not hold: their
	// snapshot entries belong to the pass that does.
	preserveRepos := map[string]bool{}
	for _, name := range candidates {
		if _, mine := scanRepos[name]; !mine && allRepos[name].Enabled {
			preserveRepos[name] = true
		}
	}
	allRepos = scanRepos
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

	// GC empty issue-* implement worktrees left behind by early-exit
	// failures that used to be preserved on status alone regardless of
	// content (issue #329). Covers both new accumulation (going forward,
	// runOneOperator's cleanup decision now checks content too) and the
	// pre-existing pile of empty shells from before this fix.
	// Limited to repos this pass holds the run lock for: another pass may be
	// about to write into an empty-looking worktree of a repo it holds.
	pruneCfg := *cfg
	pruneCfg.Repos = allRepos
	if n := pruneEmptyIssueWorktrees(&pruneCfg); n > 0 {
		fmt.Fprintf(os.Stderr, "✓ pruned %d empty issue worktree(s)\n", n)
		lg.Info("run/prune_empty_issue_worktrees", "pruned", n)
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
	// Bound the whole scan/poll phase with its own deadline. Each VCS call
	// is already capped at 30s by the HTTP client, but the phase issues one
	// ListSubIssues per open issue across every repo — when GitHub throttles,
	// N sequential 30s calls amplify into hours of "silent" hang after
	// run/reconcile and before the first run/lock (issue #221). The scan
	// context is propagated into the HTTP layer so in-flight requests abort
	// when the budget expires, well before the overall-run watchdog.
	scanBudget := scanPhaseBudget(timeout)
	scanCtx, cancelScan := context.WithTimeout(ctx, scanBudget)
	defer cancelScan()
	lg.Info("run/scan_phase", "pid", os.Getpid(), "repos", len(allRepos), "budget", scanBudget.String())

	var pending []snapshot.PendingEntry
	var activeTasks map[string]map[int]bool
	if cfg.Settings.Coordinator.Enabled {
		activeTasks, err = activeCoordinatorIssues(ctx)
		if err != nil {
			return err
		}
	}
	var jobs []*runJob
	upgradeRepos := make(map[string]config.Repo)
	depOpen := queueDepResolver(cfg)
	for fullName, repoCfg := range allRepos {
		repoCfg.CoordinatorMode = cfg.Settings.Coordinator.Enabled
		repoCfg.CoordinatorActive = activeTasks[fullName]
		// Apply shared scheduling eligibility.
		if !repoRunsHere(cfg.Settings, repoCfg, hostname) {
			debugf("repo %s is not eligible for scheduling, skipping", fullName)
			continue
		}
		// Check base_branch before dispatching anything. When the remote has
		// answered and confirmed it has no such ref, every analysis operator
		// would fetch origin/<base>, fail with exit 128 and stamp
		// agent-failed on the issue — round after round, once per auto-run
		// tick (issue #315). Suppress dispatch instead; the scan itself still
		// runs so the dashboard keeps showing the repo, its issues and the
		// base_branch hint that explains the block.
		//
		// Unproven verdicts (offline, no credentials, probe timed out) never
		// reach ProvenInvalid, so #300's rule that an offline machine must
		// not be blocked by this check still holds.
		baseCheck := checkBaseBranch(lg, fullName, repoCfg)

		executeHere := onlyRepo == "" || onlyRepo == fullName
		if executeHere {
			upgradeRepos[fullName] = repoCfg
		}
		// A disabled repo reached through a targeted run stays out of the
		// dashboard snapshots: it is not part of the enabled set.
		issueSink := &globalIssues
		if fullName == hiddenRepo {
			issueSink = new([]snapshot.IssueEntry)
		}
		repoPending, repoJobs, err := scanRepoOnceQ(scanCtx, reg, fullName, repoCfg, executeHere, onlyIssue, issueSink, queueRuntime{Settings: cfg.Settings.Queue, DepOpen: depOpen})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error on %s: %v\n", fullName, err)
		}
		// Drop the queue, keep the scan. The issue snapshot still reaches the
		// dashboard, which already renders this repo's base_branch hint
		// (snapshot.RepoView.BaseBranchHint), and the auto-merge sweep inside
		// scanRepoOnce still converges PRs that predate the misconfig.
		// Pending goes too, for the same reason deterministicSkip drops it:
		// operators that config makes unrunnable must not pile up in the
		// dashboard queue forever. Nothing has been locked at this point —
		// locks are taken in the execution phase — so discarding is
		// side-effect free.
		if baseCheck.ProvenInvalid() {
			if len(repoJobs) > 0 || len(repoPending) > 0 {
				debugf("[%s] dropping %d queued job(s) / %d pending entry(ies): base_branch %q does not exist on origin",
					fullName, len(repoJobs), len(repoPending), baseCheck.Base)
			}
			repoJobs, repoPending = nil, nil
		}
		// Propagate the output language to each job.
		for _, j := range repoJobs {
			j.language = cfg.Settings.Language
		}
		if fullName != hiddenRepo {
			pending = append(pending, repoPending...)
		}
		jobs = append(jobs, repoJobs...)
	}

	// Phase 2 (parallel): dispatch matched jobs across `workers` goroutines.
	// Three concurrency gates:
	//   - local lockfile (~/.clawflow/locks/): cross-process lock, visible
	//     to other clawflow run processes via PID-liveness check
	//   - per-issue mutex: within this process, at most one operator per issue
	//   - per-repo mutex: serializes `implement` against the shared local
	//     git clone (worktree add + fetch are not safe in parallel).
	//     Read-only operators (custom assessments, reply-comment) skip
	//     the per-repo lock and run freely.
	// Harvest shares the worker budget and is queued first so long operator
	// jobs cannot occupy every worker before acceptance gets a turn.
	// Only repos whose run locks this pass holds are included.
	harvestCfg := *cfg
	harvestCfg.Repos = allRepos
	jobs = append([]*runJob{{harvest: func(ctx context.Context) []firedKey {
		return runHarvestPhase(ctx, &harvestCfg, onlyRepo, onlyIssue, timeout, watchdogDeadline)
	}}}, jobs...)
	var fired, failed []firedKey
	if cfg.Settings.Coordinator.Enabled {
		fired, failed, err = enqueueCoordinatorJobs(ctx, jobs, timeout)
		if err != nil {
			return err
		}
	} else {
		fired, failed = runJobsParallel(ctx, jobs, workers, timeout)
	}

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

	// Re-capture issue state for repos where an operator actually fired:
	// globalIssues was collected at scan start, but the operator phase can
	// run for many minutes and mutates labels/state itself (issue closed,
	// agent-* labels applied). Writing the scan-time data after that window
	// would roll the dashboard back. Repos with no fired job keep their
	// scan-time snapshot — Phase 2 was near-instant for them, so it's
	// still fresh and costs no extra API call.
	if len(fired) > 0 {
		rerunRepos := make(map[string]bool, len(fired))
		for _, f := range fired {
			if f.Repo != hiddenRepo {
				rerunRepos[f.Repo] = true
			}
		}
		var recaptured []snapshot.IssueEntry
		for repo := range rerunRepos {
			client, cErr := newVCSClient(allRepos[repo])
			if cErr != nil {
				// Keep the scan-time entries for this repo rather than
				// dropping it from the snapshot entirely.
				fmt.Fprintf(os.Stderr, "[%s] snapshot re-capture client: %v\n", repo, cErr)
				delete(rerunRepos, repo)
				continue
			}
			fresh := captureIssuesSnapshot(client, repo)
			if fresh == nil { // ListIssues failed — fall back to scan-time data
				delete(rerunRepos, repo)
				continue
			}
			recaptured = append(recaptured, fresh...)
		}
		kept := globalIssues[:0]
		for _, e := range globalIssues {
			if !rerunRepos[e.Repo] {
				kept = append(kept, e)
			}
		}
		globalIssues = append(kept, recaptured...)
	}

	if err := snapshot.WritePendingMerged(pending, preserveRepos); err != nil {
		fmt.Fprintf(os.Stderr, "snapshot pending: %v\n", err)
	}
	if err := snapshot.WriteIssuesMerged(globalIssues, preserveRepos); err != nil {
		fmt.Fprintf(os.Stderr, "snapshot issues: %v\n", err)
	}

	// Phase 4 (sequential): wake the project-manager for every project
	// with automation enabled and past its cooldown. PMs run AFTER
	// operators so they see the post-pass state — any issue an
	// operator just updated is visible to the PM
	// scheduling decision. PM failures are non-fatal: the run as a
	// whole is reported successful as long as the operator phase
	// completed.
	//
	// Ad-hoc single-issue runs (--issue) shouldn't wake every project's PM.
	// Every other pass may: the web scheduler now runs one repo-scoped pass
	// per repository, so gating on onlyRepo would stop scheduled Pilot wakes.
	// Passes overlap, so pilot.Schedule is guarded by a try-lock; a pass that
	// loses the race leaves the wake to the holder, and pilot.Schedule's own
	// per-project interval prevents repeated wakes across passes.
	if onlyIssue == 0 {
		if snapshot.TryAcquirePilotScheduleLock() {
			if n, err := pilot.Schedule(ctx, timeout, watchdogDeadline); err != nil {
				fmt.Fprintf(os.Stderr, "[pilot] schedule: %v\n", err)
			} else if n > 0 {
				fmt.Fprintf(os.Stderr, "[pilot] woke %d project(s)\n", n)
			}
			snapshot.ReleasePilotScheduleLock()
		} else {
			lg.Info("run/pilot_skip", "reason", "another_pass_scheduling")
		}
	}

	// Auto-push: sync config to Gist after the run so any label/config
	// changes made during this pass are visible to other machines.
	// Best-effort: push failure does not fail the run.
	if syncLocked(api.AutoPush) {
		fmt.Fprintf(os.Stderr, "✓ auto-pushed config to Gist\n")
	}

	// Final step: only poll repositories selected for execution on this machine.
	upgradeCfg := *cfg
	upgradeCfg.Repos = upgradeRepos
	failed = append(failed, runSelfUpgradePhase(ctx, &upgradeCfg, newSelfUpgrader())...)

	// Exit non-zero when any dispatched operator job ended in a terminal
	// non-success state (issue #354). Reported last so Phases 3/4 and
	// auto-push still run for the failed pass.
	return failedRunsError(failed)
}

// failedRunsError builds the error `clawflow run` returns when operator jobs
// failed in this pass, or nil when there were none. Entries are formatted as
// "repo#issue op" and sorted so the message is deterministic regardless of
// worker completion order.
func failedRunsError(failed []firedKey) error {
	if len(failed) == 0 {
		return nil
	}
	items := make([]string, 0, len(failed))
	for _, f := range failed {
		items = append(items, fmt.Sprintf("%s#%d %s", f.Repo, f.IssueNumber, f.Operator))
	}
	slices.Sort(items)
	return fmt.Errorf("%d operator run(s) failed: %s", len(items), strings.Join(items, ", "))
}

// runJob is one (issue, operator) pair that scanRepoOnce decided is
// eligible to fire. The worker pool in runJobsParallel consumes these.
type runJob struct {
	hooks    *jobHooks
	result   *jobResult
	harvest  func(context.Context) []firedKey // acceptance phase, using one pool worker
	repair   *ciRepairContext
	reopen   *reopenContext
	comments []vcs.IssueComment
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
	if op.Name == "implement" && repoCfg.LocalPath == "" && !repoCfg.CoordinatorMode {
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

// operatorPriority lets custom operators run before the default implementation.
func operatorPriority(name string) int {
	if name == "implement" {
		return 20
	}
	return 15
}

// effectiveOperator applies lifecycle eligibility without changing custom triggers.
func effectiveOperator(op *operator.Operator, sub *operator.Subject) *operator.Operator {
	if op == nil || op.Name == "repair-ci" || operator.IsRetiredBuiltin(op.Name) {
		return nil
	}
	if op.Name == "implement" && (sub.IsPR || sub.State != "open" || sub.SubTotal > 0 || sub.HasLabel("tracking") || sub.HasLabel("question")) {
		return nil
	}
	return op
}

// subIssueLister is the narrow slice of vcs.Client that isStaleLeafJob needs.
// Declaring it locally keeps the freshness recheck unit-testable with a tiny
// fake instead of a full vcs.Client implementation.
type subIssueLister interface {
	ListSubIssues(repo string, issueNumber int) ([]vcs.Issue, error)
}

// isStaleLeafJob reports whether a leaf-gated operator job has gone stale
// because sub-issues were attached to its subject between scan and execution
// (issue #270). scanRepoOnce snapshots SubTotal at poll time, but a leaf
// operator can run for minutes;
// if sub-issues land in that window the issue is now a parent and should
// defer to its coordinator (track-progress) rather than run a leaf
// evaluate/implement. Only operators declaring applies_to: leaf are checked.
// GitHub-only: GitLab's ListSubIssues returns ErrNotSupported, treated here
// as "not stale" so the recheck naturally short-circuits.
func isStaleLeafJob(client subIssueLister, op *operator.Operator, repo string, issueNumber int) (stale bool, subCount int) {
	if op == nil || op.Trigger.AppliesTo != operator.AppliesLeaf {
		return false, 0
	}
	subs, err := client.ListSubIssues(repo, issueNumber)
	if err != nil {
		return false, 0
	}
	return len(subs) > 0, len(subs)
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
func scanRepoOnce(ctx context.Context, reg *operator.Registry, fullName string, repoCfg config.Repo, executeHere bool, onlyIssue int, allIssues *[]snapshot.IssueEntry) ([]snapshot.PendingEntry, []*runJob, error) {
	return scanRepoOnceQ(ctx, reg, fullName, repoCfg, executeHere, onlyIssue, allIssues, queueRuntime{})
}

// scanRepoOnceQ is scanRepoOnce with the pass-wide queue settings.
func scanRepoOnceQ(ctx context.Context, reg *operator.Registry, fullName string, repoCfg config.Repo, executeHere bool, onlyIssue int, allIssues *[]snapshot.IssueEntry, qrt queueRuntime) ([]snapshot.PendingEntry, []*runJob, error) {
	scanStart := time.Now()
	runLog.Info("run/scan", "repo", fullName, "phase", "start")
	client, err := newVCSClient(repoCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("vcs client: %w", err)
	}
	// Propagate the scan-phase deadline into the HTTP layer so a slow /
	// rate-limited endpoint can't stretch this poll into hours; in-flight
	// requests abort when the budget expires (issue #221).
	if ctx != nil {
		if rc, ok := client.(interface{ SetRequestContext(context.Context) }); ok {
			rc.SetRequestContext(ctx)
		}
	}

	// Same-name recreation guard: owner/name is reused after delete+recreate
	// on both platforms but the numeric repo ID is not, and issue numbering
	// restarts at #1 — so without this check the new repo's runs land in (and
	// display alongside) the old repo's history (issue #272). Best-effort: an
	// ID lookup failure only skips the check for this pass.
	if ider, ok := client.(interface{ GetRepoID(string) (int64, error) }); ok {
		if repoID, idErr := ider.GetRepoID(fullName); idErr != nil {
			debugf("[%s] get repo id: %v (recreation check skipped)", fullName, idErr)
		} else if archived, recErr := snapshot.ReconcileRepoIdentity(fullName, repoID); recErr != nil {
			runLog.Warn("run/scan", "repo", fullName, "phase", "repo_identity_err", "err", recErr.Error())
		} else if archived != "" {
			runLog.Info("run/scan", "repo", fullName, "phase", "repo_recreated", "archived_to", archived)
			fmt.Fprintf(os.Stderr, "⚠ %s was deleted and recreated on the platform; previous run history archived to %s\n", fullName, archived)
		}
	}

	pendingWriteBacks := map[int]bool{}
	if executeHere {
		pendingWriteBacks = replayRepoWriteBacks(client, fullName, onlyIssue)
	}
	issues, err := client.ListOpenIssues(fullName)
	if err != nil {
		runLog.Warn("run/scan", "repo", fullName, "phase", "list_issues_err", "elapsed", time.Since(scanStart).Round(time.Millisecond), "err", err.Error())
		return nil, nil, fmt.Errorf("list open issues: %w", err)
	}

	// GitHub sub-issues are not returned by the standard /issues list API.
	// Walk every open issue and append any sub-issues that aren't already
	// in the list. Two levels deep covers the current tracking→sub→sub-sub
	// pattern; deeper nesting is uncommon and can be added later.
	seen := make(map[int]bool, len(issues))
	// hasChildren records issues known to have sub-issues (parents); the
	// work queue never schedules them.
	hasChildren := map[int]bool{}
	for _, iss := range issues {
		seen[iss.Number] = true
	}
	for _, iss := range issues {
		// Stop enriching once the scan budget is spent. Each ListSubIssues
		// call is a separate request; under throttling they each burn the
		// full 30s HTTP timeout, so without this guard N issues turn one
		// slow repo into a multi-hour hang (issue #221). Remaining sub-issues
		// are simply not discovered this pass and picked up on the next run.
		if ctx != nil && ctx.Err() != nil {
			runLog.Warn("run/scan", "repo", fullName, "phase", "subissue_budget_exceeded", "elapsed", time.Since(scanStart).Round(time.Millisecond), "err", ctx.Err().Error())
			break
		}
		subs, subErr := client.ListSubIssues(fullName, iss.Number)
		if subErr != nil {
			debugf("[%s] list sub-issues of #%d: %v (skipping)", fullName, iss.Number, subErr)
			continue
		}
		if len(subs) > 0 {
			hasChildren[iss.Number] = true
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
	// implementState records, per scanned issue, why implement would not be
	// dispatched ("" = eligible). Consulted by the work queue.
	implementState := map[int]string{}
	capturedAt := time.Now().UTC()
	targetMatch := "" // operator matched for the --issue target, if any
	for _, iss := range issues {
		if iss.HasLabel("clawflow-skip") {
			continue
		}
		sub := &operator.Subject{
			Number:   iss.Number,
			Title:    iss.Title,
			Body:     iss.Body,
			Labels:   iss.Labels,
			State:    iss.State,
			IsPR:     false,
			SubTotal: iss.SubIssuesTotal,
		}
		debugf("[%s] #%d labels=%v title=%q", fullName, sub.Number, sub.Labels, sub.Title)
		// Snapshot every operator that would match this issue's CURRENT
		// label state. The executor will fire at most one of them and
		// mutate labels, but pending.json captures the queue as it looked
		// at the start of this run — the next refresh will show the
		// post-run state.
		var reopened *reopenContext
		var reopenErr error
		if implementationSubjectAllowed(sub) {
			reopened, reopenErr = discoverReopen(client, fullName, sub)
			if reopenErr != nil {
				logReopenSkip(fullName, sub.Number, reopenErr)
			}
		}
		var firstMatch *operator.Operator
		implementState[sub.Number] = "implement_operator_unavailable"
		for _, op := range reg.All() {
			if pendingWriteBacks[sub.Number] {
				implementState[sub.Number] = "pending_writeback"
				continue
			}
			isImplement := op.Name == "implement"
			op = effectiveOperator(op, sub)
			if op == nil {
				if isImplement {
					implementState[sub.Number] = "not_eligible_leaf_issue"
				}
				continue
			}
			matchSub := sub
			if op.Name == "implement" {
				if reopenErr != nil {
					implementState[sub.Number] = "reopen_check_failed: " + reopenErr.Error()
					continue
				}
				if reopened != nil {
					matchSub = withoutImplemented(sub)
				}
			}
			ok, reason := operator.MatchesWithReason(matchSub, op)
			if !ok {
				if op.Name == "implement" {
					implementState[sub.Number] = reason
				}
				debugf("  ✗ %s: %s", op.Name, reason)
				// An exclusion-only miss is the one skip reason a human can
				// trigger by hand and get zero feedback on: the trigger labels
				// are all there, so the operator "should" fire, but a leftover
				// agent-* label silently vetoes it (issue #323). Log those to
				// run.log so "I labelled it and nothing ran" is one grep away.
				// Scoped to exclusion-only misses on purpose — logging every
				// (issue × operator) rejection would flood run.log.
				if by, excluded := operator.ExcludedBy(sub, op); excluded {
					runLog.Info("run/skip_excluded",
						"repo", fullName, "issue", sub.Number, "op", op.Name, "by", by)
				}
				continue
			}
			// Drop "deterministic skip" cases from pending so they don't
			// pile up forever in the dashboard. The execution path below
			// has the same skip for firstMatch (see "Pre-flight: skip
			// deterministic failures early"); both must agree, otherwise
			// matched-but-unrunnable operators stay queued indefinitely.
			if deterministicSkip(op, repoCfg) {
				if op.Name == "implement" {
					implementState[sub.Number] = deterministicSkipReason(op, repoCfg)
				}
				debugf("  ⊘ %s matches but config makes it unrunnable, skipping pending", op.Name)
				continue
			}
			if op.Name == "implement" {
				implementState[sub.Number] = ""
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
			// Custom operators run before the default implementation; ties
			// retain the registry's stable alphabetical ordering.
			if firstMatch == nil || operatorPriority(op.Name) < operatorPriority(firstMatch.Name) {
				firstMatch = op
			}
		}
		if reason := implementState[sub.Number]; reason != "" {
			debugf("  ✗ implement #%d: %s", sub.Number, reason)
		} else {
			debugf("  ✓ implement #%d: eligible", sub.Number)
		}
		if firstMatch == nil {
			debugf("  → no eligible operator matched #%d (check terminal labels, issue structure and custom triggers)", sub.Number)
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
			targetMatch = firstMatch.Name
		}
		if firstMatch != nil {
			// Pre-flight: skip deterministic failures early. implement
			// needs a local clone but the repo may not have local_path set.
			if deterministicSkip(firstMatch, repoCfg) {
				debugf("  · skipping %s on #%d: %s", firstMatch.Name, iss.Number, deterministicSkipReason(firstMatch, repoCfg))
				continue
			}
			// Skip issues already locked by another process.
			if snapshot.IsLocked(fullName, iss.Number) || repoCfg.CoordinatorActive[iss.Number] {
				debugf("  · skipping #%d: locked by another process", iss.Number)
				continue
			}
			var selectedReopen *reopenContext
			if firstMatch.Name == "implement" {
				selectedReopen = reopened
			}
			jobs = append(jobs, &runJob{op: firstMatch, sub: sub, repo: fullName, repoCfg: repoCfg, client: client, reopen: selectedReopen})
		}
	}

	// Unified work queue: order this repo's implement candidates with the
	// shared global model and keep only the picked ones. Targeted runs are
	// explicit authorization and bypass it.
	if executeHere && onlyIssue == 0 {
		now := qrt.now()
		state, stateErr := queue.LoadState(fullName)
		if stateErr != nil {
			runLog.Warn("queue/state_unreadable", "repo", fullName, "err", stateErr.Error())
		}
		rank, rankErr := queue.LoadRank()
		if rankErr != nil {
			runLog.Warn("queue/rank_unreadable", "repo", fullName, "err", rankErr.Error())
		}
		maxInFlight, parkAfter := queueRepoParams(repoCfg, qrt.Settings)
		implementJobs := map[int]bool{}
		for _, j := range jobs {
			if j.op != nil && j.op.Name == "implement" && j.sub != nil {
				implementJobs[j.sub.Number] = true
			}
		}
		plan := planRepoQueue(repoQueueInput{
			Repo:           fullName,
			ImplementJobs:  implementJobs,
			Issues:         issues,
			HasChildren:    hasChildren,
			ImplementState: implementState,
			Locked:         func(n int) bool { return snapshot.IsLocked(fullName, n) || repoCfg.CoordinatorActive[n] },
			OpenPRs: func() ([]vcs.PR, error) {
				prs, err := client.ListOpenPRs(fullName)
				if err == nil {
					if e := snapshot.WritePipelinePRs(fullName, prs); e != nil {
						runLogWarn("pipeline PR snapshot", e)
					}
				}
				return prs, err
			},
			FlightBlocked: func(_ vcs.Issue, pr *vcs.PR) string {
				return flightBlockedReason(ctx, client, fullName, repoCfg, pr, now)
			},
			MaxInFlight: maxInFlight, ParkAfter: parkAfter,
			Rank: rank, DepOpen: qrt.DepOpen, State: state, Now: now,
		})

		jobs = applyUnifiedQueue(fullName, jobs, plan)
		pruneRepoRank(fullName, issues)
	}

	if executeHere {
		jobs = append(jobs, scanCIRepairJobs(ctx, reg, client, fullName, repoCfg, onlyIssue, jobs)...)
	}

	// Targeted run: say why nothing will happen instead of exiting silently.
	// Not a failure; the issue simply is not eligible right now.
	if executeHere && onlyIssue != 0 {
		reportTargetSkip(fullName, onlyIssue, targetMatch, issues, jobs)
	}

	// MVP: no PR-target operators yet. All built-in operators target
	// issues. When a pr-target operator appears we'll add the same
	// (subject × operator) loop over ListOpenPRs.
	//
	// PRs do get one pass here though: the auto-merge convergence sweep.
	// It only runs when this repo has auto_merge enabled, and it exists
	// because auto-merge used to be an inline side effect of a single
	// `implement` run — a PR that missed that instant was never merged by
	// anything (issue #299). Gated on executeHere so a `--repo`-narrowed
	// run doesn't merge PRs across the whole config.
	// A targeted run sweeps only PRs that close its issue.
	if executeHere {
		sweepAutoMergePRs(ctx, client, fullName, repoCfg, onlyIssue)
	}

	// Snapshot every issue (open + closed) for the dashboard — same shape
	// as POST /api/repo/refresh-issues so the cron path and the manual
	// Sync button can't disagree about which issues are open vs closed.
	//
	// Operator matching above keeps using ListOpenIssues so the
	// per_page=100 budget for open issues is independent of any closed
	// noise.
	*allIssues = append(*allIssues, captureIssuesSnapshot(client, fullName)...)

	runLog.Info("run/scan", "repo", fullName, "phase", "done", "issues", len(issues), "pending", len(pending), "jobs", len(jobs), "elapsed", time.Since(scanStart).Round(time.Millisecond))
	return pending, jobs, nil
}

// reportTargetSkip prints one stderr line and logs run/target_skip when a
// targeted run queued nothing for its issue (no operator matched, no CI-repair
// job, or the issue is not open).
func reportTargetSkip(fullName string, issueNum int, matched string, issues []vcs.Issue, jobs []*runJob) {
	for _, j := range jobs {
		if j.sub != nil && j.sub.Number == issueNum {
			return
		}
	}
	var target *vcs.Issue
	for i := range issues {
		if issues[i].Number == issueNum {
			target = &issues[i]
			break
		}
	}
	if target == nil {
		fmt.Fprintf(os.Stderr, "[%s#%d] issue is not open or not found; nothing to run\n", fullName, issueNum)
		runLog.Info("run/target_skip", "repo", fullName, "issue", issueNum, "reason", "issue_not_open")
		return
	}
	if matched != "" {
		// Matched but not queued: locked by another process or config makes
		// it unrunnable. The reason is in the debug output.
		fmt.Fprintf(os.Stderr, "[%s#%d] operator %s matched but was not dispatched (locked or unrunnable)\n", fullName, issueNum, matched)
		runLog.Info("run/target_skip", "repo", fullName, "issue", issueNum, "reason", "not_dispatched", "op", matched)
		return
	}
	labels := "none"
	if len(target.Labels) > 0 {
		labels = strings.Join(target.Labels, ", ")
	}
	fmt.Fprintf(os.Stderr, "[%s#%d] no operator matches this issue (labels: %s)\n", fullName, issueNum, labels)
	runLog.Info("run/target_skip", "repo", fullName, "issue", issueNum, "reason", "no_operator_match", "labels", target.Labels)
}

// captureIssuesSnapshot lists every issue (open + closed) in fullName via a
// single ListIssues("all") call and converts them to dashboard snapshot
// entries, resolving sub-issue parentage. The "all" call's implicit
// ~100-issue cap (sorted updated_at desc) keeps stale ancient closed issues
// out of the view. Errors are reported on stderr and yield an empty slice —
// the snapshot is best-effort.
func captureIssuesSnapshot(client vcs.Client, fullName string) []snapshot.IssueEntry {
	capturedAt := time.Now().UTC()
	snapshotIssues, err := client.ListIssues(fullName, "all", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] snapshot list issues: %v\n", fullName, err)
		return nil
	}
	// Resolve sub-issue parentage so the dashboard can nest sub-issues
	// under their parent. Only issues whose sub_issues_summary reports
	// children (SubIssuesTotal > 0) trigger the extra ListSubIssues
	// call, so a repo with no sub-issues costs zero additional requests.
	// GitLab has no native sub-issues (SubIssuesTotal stays 0), so this
	// loop is a no-op there.
	parentOf := map[int]int{} // child issue number → parent issue number
	for _, iss := range snapshotIssues {
		if iss.SubIssuesTotal == 0 {
			continue
		}
		children, subErr := client.ListSubIssues(fullName, iss.Number)
		if subErr != nil {
			debugf("  · sub-issues of #%d: %v", iss.Number, subErr)
			continue
		}
		for _, ch := range children {
			parentOf[ch.Number] = iss.Number
		}
	}
	entries := make([]snapshot.IssueEntry, 0, len(snapshotIssues))
	for _, iss := range snapshotIssues {
		entry := snapshot.IssueEntry{
			Repo:         fullName,
			IssueNumber:  iss.Number,
			IssueTitle:   iss.Title,
			HasDecided:   pipeline.DecidedSection(iss.Body),
			UpdatedAt:    iss.UpdatedAt,
			Labels:       append([]string(nil), iss.Labels...),
			State:        iss.State,
			CapturedAt:   capturedAt,
			CreatedAt:    iss.CreatedAt,
			ClosedAt:     iss.ClosedAt,
			SubTotal:     iss.SubIssuesTotal,
			SubCompleted: iss.SubIssuesCompleted,
		}
		if p, ok := parentOf[iss.Number]; ok {
			entry.ParentNumber = &p
		}
		entries = append(entries, entry)
	}
	return entries
}

// runOneOperatorFn is the per-job executor used by runJobsParallel. It is a
// variable only so tests can drive the worker pool's aggregation without
// launching Offload.
var runOneOperatorFn = runOneOperator

// runJobsParallel dispatches `jobs` across `workers` goroutines, gating
// concurrency with a per-issue lock (always) so two operators never race
// on the same (repo, issue). The previous per-repo lock for `implement`
// has been removed: git-level lock conflicts during concurrent worktree
// operations are handled by runGitWithRetry (exponential backoff on git
// lock errors) so same-repo implement jobs can now run in parallel.
// Returns the set of jobs whose operator produced output (i.e. fired),
// so the caller can deduplicate pending entries, and the set of jobs that
// reached a terminal non-success state (failed), so the caller can exit
// non-zero (issue #354). Jobs skipped by the provider-limit circuit breaker
// below are neither fired nor failed; only the job that hit the limit counts.
//
// Provider-limit circuit breaker: when any worker detects a claude refusal
// that is account-wide rather than issue-specific — a transient rate limit or
// a billing cap (402, issue #308) — it sets a shared atomic flag. Subsequent
// workers check the flag before starting and skip their job so the entire
// queue doesn't cascade into failures. The skipped issues retain their trigger
// labels and will be retried on the next run pass.
func runJobsParallel(ctx context.Context, jobs []*runJob, workers int, timeout time.Duration) (fired []firedKey, failed []firedKey) {
	if len(jobs) == 0 {
		return nil, nil
	}
	if workers < 1 {
		workers = 1
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}

	var (
		issueLocks  sync.Map    // key: "<repo>#<issue>" → *sync.Mutex
		firedMu     sync.Mutex  // guards fired and failed
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
				if ctx.Err() != nil {
					continue
				}
				if j.harvest != nil {
					if rateLimited.Load() {
						runLog.Info("run/harvest_skip", "reason", "provider_limit")
						continue
					}
					failures := j.harvest(ctx)
					firedMu.Lock()
					failed = append(failed, failures...)
					firedMu.Unlock()
					continue
				}
				// If a previous worker hit a rate limit, skip remaining
				// jobs so they aren't permanently marked as failed. They
				// keep their trigger labels and will be retried next pass.
				if rateLimited.Load() {
					prefix := fmt.Sprintf("[%s#%d %s]", j.repo, j.sub.Number, j.op.Name)
					fmt.Fprintf(os.Stderr, "%s → skipped (provider limit hit earlier in this pass)\n", prefix)
					runLog.Info("run/skipped_rate_limit", "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name)
					continue
				}

				iMu := mu(&issueLocks, fmt.Sprintf("%s#%d", j.repo, j.sub.Number))
				iMu.Lock()
				didFire, hitRateLimit, jobFailed := runOneOperatorFn(ctx, j, timeout)
				iMu.Unlock()
				if hitRateLimit {
					rateLimited.Store(true)
				}
				if didFire || jobFailed {
					key := firedKey{Repo: j.repo, IssueNumber: j.sub.Number, Operator: j.op.Name}
					firedMu.Lock()
					if didFire {
						fired = append(fired, key)
					}
					if jobFailed {
						failed = append(failed, key)
					}
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
	return fired, failed
}

// runOneOperator executes a single operator against its issue and
// persists meta.json + events.jsonl under the dashboard data dir.
// Returns (didFire, hitRateLimit, failed):
//   - didFire: true when the operator produced non-empty stdout (outcome label applied)
//   - hitRateLimit: true when claude refused for an account-level reason (a
//     transient rate limit, or a billing cap per issue #308); the caller should
//     stop dispatching remaining jobs this pass
//   - failed: true when the job reached a terminal non-success run status
//     (failed, auth-error, no-marker, disallowed-outcome, output-limit,
//     skipped-empty, rate-limited, cost-limit). Lock contention, precondition
//     skips and other non-terminal early returns are NOT failures. `clawflow
//     run` uses this to exit non-zero (issue #354).
//
// All log lines are prefixed with "[<repo>#<issue> <op>]" so output
// from concurrent workers stays disentangleable.
func runOneOperator(ctx context.Context, j *runJob, timeout time.Duration) (didFire bool, hitRateLimit bool, failed bool) {
	j.result = &jobResult{Status: "skipped"}
	originalClient, originalCfg := j.client, j.repoCfg
	if rc, ok := j.client.(interface{ SetRequestContext(context.Context) }); ok {
		rc.SetRequestContext(ctx)
	}
	if j.hooks != nil {
		j.repoCfg.AutoMerge = false
		j.client = &fencedClient{Client: j.client, ctx: ctx, result: j.result}
	}
	defer func() {
		if ctx.Err() != nil {
			j.result.Status, j.result.Error = "lease-lost", ctx.Err().Error()
			didFire, hitRateLimit, failed = false, false, false
		}
		if j.repair != nil {
			j.result.PRNumber = j.repair.PR.Number
			j.result.PRURL = j.repair.PR.URL
			j.result.Branch = j.repair.PR.HeadBranch
			if j.result.HeadSHA == "" {
				j.result.HeadSHA = j.repair.PR.HeadSHA
			}
		}
		j.client, j.repoCfg = originalClient, originalCfg
		if j.hooks != nil && j.hooks.AfterRun != nil {
			j.hooks.AfterRun(j.result)
		}
	}()
	if ctx.Err() != nil {
		return false, false, false
	}

	prefix := fmt.Sprintf("[%s#%d %s]", j.repo, j.sub.Number, j.op.Name)
	fmt.Printf("%s → start\n", prefix)

	// Cross-process lock: acquire a local lockfile so other clawflow run
	// processes (cron, web scheduler) skip this issue. If the lock is
	// already held by a live process, bail out.
	if err := j.acquireLock(ctx); err != nil {
		j.result.Error = err.Error()
		fmt.Fprintf(os.Stderr, "%s ⚠ lock failed (another process owns it): %v\n", prefix, err)
		runLog.Warn("run/lock_failed", "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name, "err", err.Error())
		return false, false, false
	}
	defer j.releaseLock()
	runLog.Info("run/lock", "pid", os.Getpid(), "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name)
	defer runLog.Info("run/unlock", "repo", j.repo, "issue", j.sub.Number)

	// Queued jobs may outlive the scan deadline. Fresh eligibility/history
	// reads must use this execution's context rather than the expired scan.
	if rc, ok := j.client.(interface{ SetRequestContext(context.Context) }); ok {
		rc.SetRequestContext(ctx)
	}
	if j.repair != nil {
		if err := prepareCIRepairContext(ctx, j, time.Now().UTC()); err != nil {
			j.result.Error = err.Error()
			fmt.Fprintf(os.Stderr, "%s → repair skipped: %v\n", prefix, err)
			runLog.Warn("run/ci_repair_skip", "repo", j.repo, "issue", j.sub.Number, "err", err.Error())
			return false, false, false
		}
		logAcceptanceRepair(j, "start")
	}

	if j.op.Name == "implement" {
		if err := prepareImplementation(j); err != nil {
			j.result.Error = err.Error()
			fmt.Fprintf(os.Stderr, "%s → implementation skipped: %v\n", prefix, err)
			runLog.Warn("run/implementation_skip", "repo", j.repo, "issue", j.sub.Number, "err", err.Error())
			return false, false, false
		}
	}

	// Re-fetch the issue's current labels to guard against the race where
	// labels were added between the initial poll and now (e.g. user manually
	// labelled the issue while an operator was already queued).
	if j.repair == nil && j.op.Name != "implement" {
		if freshLabels, err := j.client.GetIssueLabels(j.repo, j.sub.Number); err == nil {
			freshSub := *j.sub
			freshSub.Labels = freshLabels
			freshOp := effectiveOperator(j.op, &freshSub)
			if freshOp == nil {
				return false, false, false
			}
			if ok, reason := operator.MatchesWithReason(&freshSub, freshOp); !ok {
				fmt.Printf("%s → skip (labels changed since poll: %s)\n", prefix, reason)
				if by, excluded := operator.ExcludedBy(&freshSub, freshOp); excluded {
					runLog.Info("run/skip_excluded",
						"repo", j.repo, "issue", j.sub.Number, "op", j.op.Name, "by", by, "when", "post_poll")
				}
				return false, false, false
			}
			j.op = freshOp
		}

	}

	// Sub-issue freshness recheck (issue #270). The scan captured SubTotal
	// at poll time, but a leaf operator can sit queued — and then run — for
	// minutes. If sub-issues were attached to this issue in that window it
	// is now a parent and must defer to its coordinator (track-progress),
	// not run a leaf evaluate/implement.
	//
	// This is a purely structural skip, not a verdict on the issue's
	// requirement — it must NOT write a terminal label. Marking it
	// agent-skipped used to make track-progress (and anything else keyed
	// off that label) treat "became a parent mid-flight" as "this
	// requirement is done", which is wrong: the issue is open and its
	// sub-issues haven't shipped anything yet (issue #335). The
	// applies_to: leaf gate on the operator's own trigger already keeps it
	// out of the queue on later passes (SubTotal > 0 no longer matches
	// leaf), so no label is needed to prevent a re-fire — only the log line
	// records that this pass was skipped for this reason. GitHub-only —
	// GitLab short-circuits inside isStaleLeafJob.
	if stale, n := isStaleLeafJob(j.client, j.op, j.repo, j.sub.Number); stale {
		fmt.Printf("%s → skip (became parent since poll: %d sub-issue(s))\n", prefix, n)
		runLog.Info("run/skip_became_parent", "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name, "subs", n)
		return false, false, false
	}

	// Persist per-run events.jsonl + meta.json under the dashboard
	// data dir so `clawflow web` can replay this run later. The dirs
	// and the placeholder meta are created BEFORE the workdir setup so
	// even an early failure (e.g. worktree creation) gets recorded as
	// a real run on disk.
	startedAt := time.Now()
	runDir := snapshot.RunDir(j.repo, j.sub.Number, startedAt)
	_ = os.MkdirAll(runDir, 0o755)

	machine, _ := os.Hostname()
	runningMeta := snapshot.RunMeta{
		Machine:     machine,
		Profile:     configuredDevelopmentProfile(roleForOperator(j.op.Name)),
		Operator:    j.op.Name,
		Repo:        j.repo,
		IssueNumber: j.sub.Number,
		IssueTitle:  j.sub.Title,
		IssueBody:   j.sub.Body,
		IssueState:  j.sub.State,
		StartedAt:   startedAt.UTC(),
		Status:      "running",
		Stage:       "lock-acquired",
	}
	defer func() {
		if j.result.Status == "skipped" {
			j.result.Status, j.result.Error = runningMeta.Status, runningMeta.Error
		}
	}()
	if err := snapshot.WriteRunMeta(runDir, runningMeta); err != nil {
		fmt.Fprintf(os.Stderr, "%s ✗ initial run meta: %v\n", prefix, err)
		runningMeta.Status = "failed"
		runningMeta.Error = err.Error()
		return false, false, false
	}

	// Each lifecycle transition persists metadata and upserts its SQL row, so
	// polling and SSE readers observe the same current stage.
	emitStage := func(stage string) {
		runningMeta.Stage = stage
		if err := snapshot.WriteRunMeta(runDir, runningMeta); err != nil {
			fmt.Fprintf(os.Stderr, "%s ⚠ stage meta (%s): %v\n", prefix, stage, err)
		}
	}

	// Fetch comments before resolveWorkdir so we can parse the
	// ClawFlow-Base-Branch: marker and plumb the override into worktree
	// setup. Using ListIssueCommentsDetail gives us CreatedAt for correct
	// newest-first ordering and avoids a second round-trip at line 736.
	detailComments := j.comments
	if j.op.Name != "implement" {
		var commentErr error
		detailComments, commentErr = j.client.ListIssueCommentsDetail(j.repo, j.sub.Number)
		if commentErr != nil {
			fmt.Fprintf(os.Stderr, "%s → comments unavailable: %v\n", prefix, commentErr)
			runningMeta.Status = "skipped"
			runningMeta.Error = commentErr.Error()
			_ = snapshot.WriteRunMeta(runDir, runningMeta)
			return false, false, false
		}
	}

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
		// Pre-claude failure: run/end below (line ~1393) is never reached from
		// this early return, so without this WARN the failure is invisible to
		// deployment.md's patrol grep (issue #333). stage is included because
		// it is the only signal that distinguishes claude-before vs claude-after
		// failures once this line and the real run/end converge on the same slug.
		runLog.Warn("run/end", "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name,
			"status", "failed", "stage", runningMeta.Stage, "err", overrideErr.Error(), "cost", "0.0000")
		if ctx.Err() == nil && j.hooks == nil {
			checkCircuitBreaker(j, prefix)
		}
		return false, false, true
	}
	if hasOverride {
		fmt.Fprintf(os.Stderr, "%s → base-branch override: %q (from %s)\n", prefix, overrideBranch, overrideSource)
	}

	var previousRun snapshot.RunMeta
	if j.op.Name == "implement" || j.op.Name == "repair-ci" {
		previousRun = previousDevelopmentRun(j.repo, j.sub.Number, startedAt)
	}
	var wtResult worktreeResult
	var cleanup func()
	var err error
	if j.repair != nil {
		var reused bool
		wtResult, reused = reusableRepairWorktree(ctx, previousRun, j.repo, j.repair.PR)
		if reused {
			cleanup = resumedWorktreeCleanup(j.repoCfg.LocalPath, wtResult.Path)
		} else {
			wtResult, cleanup, err = setupCIRepairWorkdir(ctx, j.repoCfg, j.repo, j.repair.PR)
		}
		if err == nil && (j.repair.MergeConflict || j.repair.Acceptance != nil) {
			err = refreshRepairBase(ctx, wtResult.Path, j)
		}
	} else if j.reopen != nil {
		wtResult, cleanup, err = setupReopenWorktreeContext(ctx, j.repoCfg, j.repo, j.sub.Number, startedAt, overrideBranch, j.reopen)
	} else {
		reused := false
		if j.op.Name == "implement" {
			wtResult, reused = reusableDevelopmentWorktree(ctx, previousRun, j.repo)
		}
		if reused {
			cleanup = resumedWorktreeCleanup(j.repoCfg.LocalPath, wtResult.Path)
		} else {
			wtResult, cleanup, err = resolveWorkdir(ctx, j.op, j.repoCfg, j.repo, j.sub.Number, startedAt, overrideBranch)
		}
	}
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
		if ctx.Err() != nil {
			runningMeta.Status = "lease-lost"
		}
		now := time.Now().UTC()
		runningMeta.EndedAt = &now
		_ = snapshot.WriteRunMeta(runDir, runningMeta)
		// Same rationale as the base-branch-marker failure above: this early
		// return skips run/end entirely, so this WARN is the only structured
		// record of a claude-before-launch failure (issue #333). This is the
		// path that covers base_branch misconfiguration (git fetch failures),
		// the highest-volume pre-claude failure family in production.
		runLog.Warn("run/end", "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name,
			"status", "failed", "stage", runningMeta.Stage, "err", err.Error(), "cost", "0.0000")
		if j.repair != nil {
			// No agent was invoked: release the reservation under the existing
			// infrastructure budget/backoff instead of terminally blocking repair.
			kind := "setup"
			if errors.Is(err, gitexec.ErrTimeout) {
				kind = "git_timeout"
			}
			if rerr := j.releaseRepair(kind, now); rerr == nil {
				runLog.Warn("run/ci_repair_infra", "repo", j.repo, "issue", j.sub.Number, "pr", j.repair.PR.Number, "attempt", j.repair.Attempt, "kind", kind)
				logAcceptanceRepair(j, "end", "status", "failed", "kind", kind, "attempt_consumed", false)
				return false, false, true
			} else {
				runLog.Warn("run/ci_repair_release_failed", "repo", j.repo, "issue", j.sub.Number, "err", rerr.Error())
			}
			_ = j.client.AddLabel(j.repo, j.sub.Number, "agent-failed")
			_ = j.client.PostIssueComment(j.repo, j.sub.Number, j.repair.label()+" repair setup failed: "+err.Error())
			logAcceptanceRepair(j, "end", "status", "failed", "err", err.Error())
		} else {
			if ctx.Err() == nil && j.hooks == nil {
				checkCircuitBreaker(j, prefix)
			}
		}
		return false, false, true
	}
	workdir := wtResult.Path
	if wtResult.Resumed {
		fmt.Fprintf(os.Stderr, "%s ✓ workdir RESUMED: %s (branch: %s)\n", prefix, workdir, wtResult.BranchName)
	} else {
		fmt.Fprintf(os.Stderr, "%s ✓ workdir ready: %s\n", prefix, workdir)
	}

	// Extract comment bodies from the already-fetched detail slice.
	comments := formattedComments(detailComments)

	eventsFile, eventsErr := os.Create(filepath.Join(runDir, "events.jsonl"))
	if eventsErr != nil {
		fmt.Fprintf(os.Stderr, "%s ⚠ events sink: %v\n", prefix, eventsErr)
	} else {
		defer eventsFile.Close()
	}

	role := roleForOperator(j.op.Name)
	fmt.Fprintf(os.Stderr, "%s → Offload (role %s, profile owns model, timeout %s)\n", prefix, role, timeout)
	runLog.Info("run/offload_start", "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name, "role", role, "timeout", timeout)
	runStart := time.Now()

	// Build resume context for the operator prompt when reusing a worktree.
	var resumeCtx string
	if wtResult.Resumed {
		resumeCtx = buildResumeContext(wtResult)
	}
	if j.repair != nil {
		resumeCtx += j.repair.prompt(j.repoCfg.TestCommand)
	}

	// Rebind the VCS client's request context before execution + write-back.
	// The client was configured in scanRepoOnce with the scan-phase budget
	// (scanCtx, clamped to 5–15 min via scanPhaseBudget). An implement run
	// can take tens of minutes, by which point scanCtx has long expired.
	// If we don't rebind, the write-back HTTP calls (post comment, add label)
	// inherit that dead context and fail immediately with "context deadline
	// exceeded" — the retry added in #278 never fires because retry.Do bails
	// out on ctx.Err() before the first attempt. Rebind to this operator's
	// full run context so write-back gets a live budget (issue #293).
	if rc, ok := j.client.(interface{ SetRequestContext(context.Context) }); ok {
		rc.SetRequestContext(ctx)
	}

	if j.reopen != nil {
		resumeCtx += j.reopen.prompt(j.sub.Number)
		if err := removeStaleImplementation(j); err != nil {
			fmt.Fprintf(os.Stderr, "%s → reopen skipped: %v\n", prefix, err)
			runningMeta.Status = "skipped"
			runningMeta.Error = err.Error()
			_ = snapshot.WriteRunMeta(runDir, runningMeta)
			return false, false, false
		}
	}

	var sessionOpts operator.RunOptions
	if j.op.Name == "implement" || j.op.Name == "repair-ci" {
		sessionOpts = developmentResumeOptions(ctx, j, previousRun, wtResult, detailComments, resumeCtx, &runningMeta, func() { _ = snapshot.WriteRunMeta(runDir, runningMeta) })
	}
	ctx = operator.WithExecutionResultObserver(ctx, func(r execution.Result) {
		runningMeta.Profile = r.Profile
		runningMeta.Backend = r.Backend
		runningMeta.Model = r.Model
		runningMeta.Usage = snapshot.UsageFromResult(r)
	})
	confirmedHead := ""
	output, outcome, runErr := runJobOperator(ctx, j.op, j.sub, j.client, operator.RunOptions{
		SessionKey:          sessionOpts.SessionKey,
		ResumePrompt:        sessionOpts.ResumePrompt,
		OnExecutionResult:   sessionOpts.OnExecutionResult,
		OnSessionResume:     sessionOpts.OnSessionResume,
		PrepareFreshSession: func() error { return archiveRejectedResume(runDir, eventsFile) },
		ValidateOutcome: func(outcome, body string) error {
			var err error
			confirmedHead, err = validateRepairOutcome(ctx, workdir, j, outcome, body)
			return err
		},
		Repo:           j.repo,
		RunID:          strings.ReplaceAll(j.repo, "/", "__") + "-" + strconv.Itoa(j.sub.Number) + "-" + filepath.Base(runDir),
		CommentSaveDir: runDir,
		Workdir:        workdir,
		Timeout:        timeout,
		Comments:       comments,
		Role:           role,
		EventWriter:    eventsFile,
		ResumeContext:  resumeCtx,
		Language:       j.language,
		StageFunc:      emitStage,
	})
	var writebackErr *operator.WriteBackError
	errors.As(runErr, &writebackErr)
	if writebackErr != nil {
		runErr = nil
	}
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
	if runErr == nil && output != "" && !(j.repair != nil && outcome == "agent-failed") &&
		confirmedHead == "" && (j.op.Name == "implement" || j.op.Name == "repair-ci") {
		var headErr error
		confirmedHead, headErr = confirmedDevelopmentHead(ctx, workdir, j)
		if errors.Is(headErr, gitexec.ErrTimeout) {
			runErr = headErr
		}
	}
	if runErr == nil && output != "" && !(j.repair != nil && outcome == "agent-failed") {
		finalizingMeta := snapshot.RunMeta{
			DevelopmentSession: runningMeta.DevelopmentSession,
			Usage:              runningMeta.Usage,
			Profile:            runningMeta.Profile, Backend: runningMeta.Backend, Model: runningMeta.Model, Machine: runningMeta.Machine,
			HeadSHA:     confirmedHead,
			Outcome:     outcome,
			Operator:    j.op.Name,
			Repo:        j.repo,
			IssueNumber: j.sub.Number,
			IssueTitle:  j.sub.Title,
			IssueBody:   j.sub.Body,
			IssueState:  j.sub.State,
			StartedAt:   startedAt.UTC(),
			Status:      "finalizing",
			Stage:       "finalizing",
			Summary:     output,
		}
		if j.repair != nil {
			finalizingMeta.RepairReason = j.repair.reason()
		}
		if outcome == "no-change" {
			finalizingMeta.OutcomeReason = output
		}
		if writebackErr != nil {
			finalizingMeta.WritebackError = writebackErr.Error()
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

	// Detect output-token-limit aborts ("response exceeded the N output token
	// maximum"). Like auth errors these are NOT fixable by retrying — the
	// operator needs a higher max_output_tokens ceiling. Record a distinct
	// status and skip the circuit breaker so we don't burn tokens re-firing
	// the same doomed run every pass (issue #286).
	isOutputLimit := runErr != nil && errors.Is(runErr, operator.ErrOutputLimit)

	// Detect billing-cap refusals (HTTP 402, "已达到每日费用限制"). These behave
	// like rate limits — the queue must abort and the issue keeps its trigger
	// labels — but they are recorded under their own status because the
	// recovery window is the next billing period, not the next few minutes.
	// Critically they must NOT count toward the circuit breaker: the run dies
	// in 1 turn at $0 and says nothing about the issue, so counting it labels
	// healthy issues agent-failed the moment an account cap is hit (issue #308).
	isCostLimit := runErr != nil && errors.Is(runErr, operator.ErrCostLimit)

	// Detect no-marker failures: claude produced output but omitted the
	// outcome marker. operator.Run now returns ErrNoOutcomeMarker for this
	// case so we can route it through the circuit breaker (status="failed")
	// instead of silently recording it as "success" and letting the issue
	// re-fire indefinitely (see issue #143).
	isNoMarker := runErr != nil && errors.Is(runErr, operator.ErrNoOutcomeMarker)

	// Detect disallowed-outcome runs: the operator emitted a trailing marker
	// whose label is not in its declared outcomes whitelist, so no terminal
	// label could be applied. Previously this returned nil and was filed as
	// status="success" with the bogus label echoed into run/end at INFO — the
	// only path that applied no label, left the trigger labels in place, and
	// produced no greppable failure signal (issue #326).
	isDisallowedOutcome := runErr != nil && errors.Is(runErr, operator.ErrDisallowedOutcome)

	// CI repair infrastructure outcomes (provider capacity/rate limit, launch,
	// transport, timeout before any tool activity) release the reserved attempt
	// so only real repair work consumes the three-attempt budget (issue #356).
	repairInfra := false
	if kind, ok := classifyRepairInfra(runErr, filepath.Join(runDir, "events.jsonl")); ok {
		j.result.InfraKind = kind
	}
	if j.repair != nil && runErr != nil {
		if kind, ok := classifyRepairInfra(runErr, filepath.Join(runDir, "events.jsonl")); ok {
			if rerr := j.releaseRepair(kind, time.Now().UTC()); rerr != nil {
				fmt.Fprintf(os.Stderr, "%s ⚠ repair attempt release failed (attempt stays consumed): %v\n", prefix, rerr)
				runLog.Warn("run/ci_repair_release_failed", "repo", j.repo, "issue", j.sub.Number, "err", rerr.Error())
			} else {
				repairInfra = true
				fmt.Fprintf(os.Stderr, "%s → %s repair infrastructure outcome (%s): attempt %d not consumed, backoff applied\n", prefix, j.repair.label(), kind, j.repair.Attempt)
				runLog.Warn("run/ci_repair_infra", "repo", j.repo, "issue", j.sub.Number, "pr", j.repair.PR.Number, "attempt", j.repair.Attempt, "kind", kind)
			}
		}
	}

	if runErr != nil {
		if isCostLimit {
			// Warn-level so patrol's grep -E "ERROR|WARN" catches the one
			// condition that silently stalls every repo at once (issue #308).
			runLog.Warn("run/cost_limit", "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name, "err", runErr.Error())
			fmt.Fprintf(os.Stderr, "%s ✗ Offload execution cost limit reached (aborting this pass): %v\n", prefix, runErr)
		} else if isRateLimit {
			fmt.Fprintf(os.Stderr, "%s ✗ Offload rate limited (will retry next pass): %v\n", prefix, runErr)
		} else if isAuthError {
			// Use Warn-level so patrol grep -E "ERROR|WARN" catches this.
			runLog.Warn("run/auth_error", "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name, "err", runErr.Error())
			fmt.Fprintf(os.Stderr, "%s ✗ Offload authentication error (check execution profile, NOT retrying): %v\n", prefix, runErr)
		} else if isNoMarker {
			fmt.Fprintf(os.Stderr, "%s ✗ Offload produced no outcome marker (write-back defect, NOT counted toward circuit breaker): %v\n", prefix, runErr)
		} else if isDisallowedOutcome {
			fmt.Fprintf(os.Stderr, "%s ✗ operator produced a disallowed outcome label — comment landed but no terminal label: %v\n", prefix, runErr)
		} else if isOutputLimit {
			runLog.Warn("run/output_limit", "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name, "err", runErr.Error())
			fmt.Fprintf(os.Stderr, "%s ✗ Offload output limit exceeded (check engine limits, NOT retrying): %v\n", prefix, runErr)
		} else {
			fmt.Fprintf(os.Stderr, "%s ✗ Offload failed: %v\n", prefix, runErr)
		}
	} else {
		fmt.Fprintf(os.Stderr, "%s ✓ Offload finished in %s\n", prefix, runDur)
	}

	endedAt := time.Now().UTC()
	rm := snapshot.RunMeta{
		DevelopmentSession: runningMeta.DevelopmentSession,
		Usage:              runningMeta.Usage,
		Profile:            runningMeta.Profile, Backend: runningMeta.Backend, Model: runningMeta.Model, Machine: runningMeta.Machine,
		Operator:    j.op.Name,
		Repo:        j.repo,
		IssueNumber: j.sub.Number,
		IssueTitle:  j.sub.Title,
		IssueBody:   j.sub.Body,
		StartedAt:   startedAt.UTC(),
		EndedAt:     &endedAt,
		Summary:     output,
	}
	switch {
	case ctx.Err() != nil:
		rm.Status, rm.Error = "lease-lost", ctx.Err().Error()
	case isCostLimit:
		// Record as "cost-limit" — deliberately distinct from "rate-limited"
		// because the two need different operator responses: a rate limit
		// clears on its own within minutes, a billing cap holds until the
		// provider's window resets, so the dashboard should read "don't bother
		// running today" rather than "retrying shortly". Excluded from the
		// circuit breaker (see snapshot.ConsecutiveFailures) — semantically
		// this is closer to auth-error than to output-limit: there is no token
		// bleed to bound, since each capped run is 1 turn at $0 (issue #308).
		rm.Status = "cost-limit"
		rm.Error = runErr.Error()
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
	case isOutputLimit:
		// Record as "output-limit" — distinct from "failed" so the dashboard
		// surfaces the specific cause (raise max_output_tokens). Unlike
		// rate-limit (self-resolves) and auth (fails fast), an output-limit run
		// deterministically fails AND burns a full large-diff generation every
		// pass, so it DOES count toward the circuit breaker to bound the token
		// bleed — eventually labeling agent-failed until the owner raises the
		// ceiling. Each pass also emits a WARN log for patrol (issue #286).
		rm.Status = "output-limit"
		rm.Error = runErr.Error()
	case isNoMarker:
		// No-marker runs are recorded as "no-marker" (distinct from generic
		// "failed") so the dashboard can surface the specific cause. They do
		// NOT count toward the circuit breaker (issue #323): the run completed
		// and produced a body, only the write-back marker was missing, so
		// agent-failed would freeze the issue for every operator that excludes
		// that label instead of letting a human push it forward. The re-fire
		// behavior remains retryable so a later run can produce a valid marker.
		rm.Status = "no-marker"
		rm.Error = runErr.Error()
	case isDisallowedOutcome:
		// Recorded as "disallowed-outcome" rather than "success": the comment
		// landed but no terminal label did, so the run is genuinely degraded.
		// It DOES count toward the circuit breaker for the same reason
		// no-marker does — the write-back is incomplete and, if the operator
		// keeps picking a label outside its whitelist, every pass pays again
		// (issue #326).
		rm.Status = "disallowed-outcome"
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
	case j.repair != nil && outcome == "agent-failed":
		rm.Status = "failed"
		rm.Error = j.repair.label() + " repair could not update existing PR"
	default:
		rm.Status = "success"
	}

	if j.repair != nil {
		rm.RepairReason = j.repair.reason()
	}
	if rm.Status == "success" && (j.op.Name == "implement" || j.op.Name == "repair-ci") {
		rm.HeadSHA = confirmedHead
	}
	rm.Outcome = outcome
	if outcome == "no-change" {
		rm.OutcomeReason = output
	}

	if writebackErr != nil {
		rm.WritebackError = writebackErr.Error()
	}
	// Conditional cleanup: only remove the worktree on success/skip.
	// On failure, preserve it so the next run can resume from the
	// partial work instead of starting from scratch.
	// skipped-empty, no-marker, and auth-error have no partial work worth
	// resuming, so clean up to avoid accumulating stale worktrees.
	// cost-limit joins them: the run was refused before claude did any work,
	// so the worktree is empty and keeping it only accumulates garbage across
	// however many passes happen before the billing window resets (issue #308).
	//
	// status == "failed" is different from the others above: it covers
	// EVERY way claude -p can exit non-zero, not just the specific reasons
	// enumerated by name. Some of those (mid-edit crash, timeout after
	// partial writes) really do leave partial work worth resuming — but
	// others (claude erroring before writing a single file, e.g. an
	// unrecognized early-exit code not yet classified as cost-limit/
	// auth-error/output-limit) leave the worktree exactly as empty as a
	// fresh checkout. Deciding solely on status caused those to be kept
	// forever: findExistingWorktree's resume check looks at *content*
	// (hasUncommitted || hasCommitsAhead), so it never picks an empty
	// "failed" worktree back up, and no cleanup path ever collected it
	// either — a monotonically growing pile of empty dirs (issue #329).
	// Probe content here with the same detectPartialWork logic so both
	// sides of the resume decision agree: keep only if there's something
	// to resume, otherwise treat it like the other no-partial-work statuses.
	hasPartialWork := true
	if rm.Status == "failed" && j.repair == nil {
		base := overrideBranch
		if base == "" {
			base = j.repoCfg.BaseBranch
		}
		if base == "" {
			base = "main"
		}
		hasPartialWork, _, _ = detectPartialWorkContext(ctx, workdir, base)
	}
	// Completed work only needs native retention when repair is enabled or
	// delivery is incomplete. Other successful runs retain normal cleanup.
	pendingDevelopment := rm.Status != "success" || rm.WritebackError != "" || outcome == "agent-failed" || ciRepairEnabled(j.repoCfg) || acceptanceRepairEnabled(j.repoCfg)
	keepSessionWorktree := pendingDevelopment && rm.DevelopmentSession.SessionID != "" && rm.DevelopmentSession.ErrorKind != "output_limit" && rm.Status != "auth-error" && rm.Status != "cost-limit" && rm.Status != "output-limit"
	if !keepSessionWorktree && (repairInfra || ((j.repair == nil || (rm.Status == "success" && (outcome == "agent-implemented" || outcome == "no-change"))) && (rm.Status == "success" || rm.Status == "skipped-empty" || rm.Status == "no-marker" || rm.Status == "disallowed-outcome" || rm.Status == "auth-error" || rm.Status == "output-limit" || rm.Status == "cost-limit" || (rm.Status == "failed" && !hasPartialWork)))) {
		cleanup()
	} else {
		fmt.Fprintf(os.Stderr, "%s → preserving worktree evidence: %s\n", prefix, workdir)
	}

	// u != nil guard: ExtractUsage returns (nil, nil) when there is nothing to
	// account for, and assigning that would blank a recorded Usage (issue #322).
	if u, uerr := snapshot.ExtractUsage(filepath.Join(runDir, "events.jsonl")); uerr == nil && u != nil {
		rm.Usage = u
	}
	if j.hooks != nil && rm.Status == "success" && ctx.Err() == nil && (j.op.Name == "implement" || j.repair != nil) {
		if events, err := acceptanceRead(filepath.Join(runDir, "events.jsonl"), acceptanceFileLimit); err == nil {
			j.result.DevelopmentEvents = string(events)
		}
	}
	j.result.Status, j.result.Error = rm.Status, rm.Error
	j.result.Outcome, j.result.Output = outcome, output
	j.result.Usage, j.result.HeadSHA = rm.Usage, rm.HeadSHA
	j.result.RateLimited, j.result.CostLimited = isRateLimit, isCostLimit
	j.result.Branch = wtResult.BranchName
	j.result.PRNumber = extractPRNumber(output)
	j.result.PRURL = rm.PRUrl
	if j.result.PRNumber > 0 && ctx.Err() == nil {
		if pr, err := j.client.GetPR(j.repo, j.result.PRNumber); err == nil {
			j.result.PRURL, j.result.Branch = pr.URL, pr.HeadBranch
			if j.result.HeadSHA == "" {
				j.result.HeadSHA = pr.HeadSHA
			}
		}
	}
	if rm.Usage != nil {
		j.result.Cost = rm.Usage.TotalCostUSD
	}
	if writebackErr != nil {
		j.result.Error = writebackErr.Error()
	}
	if err := snapshot.WriteRunMeta(runDir, rm); err != nil {
		fmt.Fprintf(os.Stderr, "%s ⚠ run meta: %v\n", prefix, err)
	}
	// Upgrade to WARN for non-success statuses so deployment.md patrol's
	// "grep -E ERROR|WARN" actively catches failures (issue #204).
	//
	// "no-marker" belongs on this list (issue #314): it means the run was paid
	// for in full, produced no label, and — because the trigger labels are
	// untouched — will be picked up and paid for again on the next pass. Eight
	// such runs cost $8.54 while logging only at INFO, so patrol never flagged
	// them and they were found by hand-grepping status=no-marker.
	logFn := runLog.Info
	switch rm.Status {
	case "failed", "auth-error", "output-limit", "no-marker", "disallowed-outcome", "cost-limit":
		logFn = runLog.Warn
	}
	// cost is emitted for every status, not just the lossy ones: a single
	// figure per run makes the per-status spend greppable straight from
	// run.log, without cross-referencing each meta.json. 0 when events.jsonl
	// carried no terminal result event (in-flight kill, ExtractUsage failure).
	var runCost float64
	if rm.Usage != nil {
		runCost = rm.Usage.TotalCostUSD
	}
	// appliedLabel is the label that actually landed on the issue, as opposed to
	// `outcome`, which is only what the operator asked for. The two diverge on
	// the disallowed-outcome path, and logging just `outcome` there made run.log
	// assert a label the issue never received — the most misleading part of
	// issue #326. Emitted alongside rather than replacing `outcome` so existing
	// greps over run.log keep working.
	appliedLabel := outcome
	if isDisallowedOutcome || writebackErr != nil || outcome == "no-change" {
		appliedLabel = ""
	}
	logFn("run/end",
		"repo", j.repo,
		"issue", j.sub.Number,
		"op", j.op.Name,
		"status", rm.Status,
		"writeback_pending", rm.WritebackError != "",
		"duration", runDur,
		"outcome", outcome,
		"applied_label", appliedLabel,
		"pr", rm.PRUrl,
		"cost", fmt.Sprintf("%.4f", runCost),
	)

	logAcceptanceRepair(j, "end", "status", rm.Status, "outcome", outcome, "infra", repairInfra)

	// Apply successful lifecycle follow-up.
	if rm.Status == "success" && j.hooks == nil {
		runPostAutomation(ctx, j, outcome, output, prefix)
	}

	// Released infrastructure outcomes are bounded by the repair ledger's own
	// infrastructure budget, so they must not also trip the circuit breaker
	// (which would label the issue agent-failed and block further repair).
	if repairInfra && rm.Status == "failed" {
		fmt.Printf("%s ✗ %s repair infrastructure failure (not counted toward circuit breaker)\n", prefix, j.repair.label())
		return false, false, true
	}

	switch rm.Status {
	case "success":
		fmt.Printf("%s ✓ done\n", prefix)
		return true, false, false
	case "rate-limited":
		// Signal the caller to abort the queue; do NOT call checkCircuitBreaker.
		return false, true, true
	case "cost-limit":
		// Abort the queue exactly like a rate limit — a billing cap is
		// account-level, so every remaining job in this pass would fail
		// identically. Without this the whole queue re-fires and each issue
		// burns a circuit-breaker slot on someone else's spend (issue #308).
		fmt.Printf("%s ✗ cost limit reached (aborting pass; retries when the billing window resets)\n", prefix)
		return false, true, true
	case "auth-error":
		// Do NOT signal queue abort (unlike rate-limit, other issues may still
		// succeed with the same session). Do NOT call checkCircuitBreaker —
		// agent-failed would be misleading for a credential problem.
		fmt.Printf("%s ✗ auth error (check session/API key)\n", prefix)
		return false, false, true
	case "no-marker":
		// Do NOT call checkCircuitBreaker: a missing marker line is a
		// write-back defect, not an unprocessable issue (issue #323).
		fmt.Printf("%s ✗ no outcome marker (not counted toward circuit breaker)\n", prefix)
		return false, false, true
	case "disallowed-outcome":
		fmt.Printf("%s ✗ disallowed outcome label %q (comment landed, no terminal label; circuit breaker counting)\n", prefix, outcome)
		if ctx.Err() == nil && j.hooks == nil {
			checkCircuitBreaker(j, prefix)
		}
		return false, false, true
	case "output-limit":
		fmt.Printf("%s ✗ output token limit (raise max_output_tokens; circuit breaker counting)\n", prefix)
		if ctx.Err() == nil && j.hooks == nil {
			checkCircuitBreaker(j, prefix)
		}
		return false, false, true
	case "skipped-empty":
		fmt.Printf("%s ✗ empty output (circuit breaker counting)\n", prefix)
		if ctx.Err() == nil && j.hooks == nil {
			checkCircuitBreaker(j, prefix)
		}
		return false, false, true
	default:
		if ctx.Err() == nil && j.hooks == nil {
			checkCircuitBreaker(j, prefix)
		}
		return false, false, true
	}
}

var runJobOperator = operator.Run

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
		// Structured trail for the running failure count, not just stderr
		// (issue #333) — cron/web scheduler runs never surface stderr, so
		// this was the only place the pre-threshold count existed.
		runLog.Info("run/failure_count", "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name, "count", count, "max", maxFails)
		return
	}
	fmt.Fprintf(os.Stderr, "%s ✗ circuit breaker: %d consecutive failures, adding agent-failed\n", prefix, count)
	if err := j.client.AddLabel(j.repo, j.sub.Number, "agent-failed"); err != nil {
		fmt.Fprintf(os.Stderr, "%s ⚠ circuit breaker label failed: %v\n", prefix, err)
		return
	}
	// Every agent-failed application must be auditable from run.log alone
	// (issue #333): before this, the label landed on production issues with
	// zero trace in structured logs, so patrol had no timestamp or count to
	// justify later removing it.
	runLog.Warn("run/circuit_breaker", "repo", j.repo, "issue", j.sub.Number, "op", j.op.Name, "count", count, "label", "agent-failed")
	// No accompanying comment by design: the failure trail lives in
	// events.jsonl + dashboard runs, and chatter on the issue itself
	// just adds noise users have to scroll past. PM patrol may remove
	// `agent-failed` later if the underlying issue looks recoverable.
}

// runPostAutomation handles merge and parent-progress follow-up.
func runPostAutomation(ctx context.Context, j *runJob, outcome, output, prefix string) {
	// Auto-merge: after implement produces agent-implemented, take one CI
	// reading and merge if it is already green. Never wait here: this runs
	// while the global run lock and a worker slot are held, so blocking up to
	// ci_timeout stalled every other repository. A fresh PR's checks are
	// normally still pending; the per-scan sweep (sweepAutoMergePRs, #299) and
	// the harvest phase converge it on the next pass. Both paths share
	// attemptAutoMerge so they can't drift.
	if outcome == "agent-implemented" && j.repoCfg.AutoMerge {
		prNum := extractPRNumber(output)
		if prNum == 0 {
			fmt.Fprintf(os.Stderr, "%s ⚠ auto-merge: could not extract PR number from output (sweep will retry next pass)\n", prefix)
			return
		}
		if result := attemptAutoMergeResult(autoMergeAttempt{
			ctx:      ctx,
			client:   j.client,
			repo:     j.repo,
			repoCfg:  j.repoCfg,
			prNum:    prNum,
			issueNum: j.sub.Number,
			prefix:   prefix,
			ciPoll:   true,
			quiet:    true,
		}); !result.Merged {
			result.log("run/automerge_skip", j.repo, prNum, j.sub.Number)
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
func resolveWorkdir(ctx context.Context, op *operator.Operator, repoCfg config.Repo, fullName string, issueNum int, startedAt time.Time, overrideBranch string) (worktreeResult, func(), error) {
	if err := ctx.Err(); err != nil {
		return worktreeResult{}, func() {}, err
	}
	// implement and pr-target operators need a mutable per-issue worktree.
	needsRepo := op.Name == "implement" || op.Trigger.Target == "pr"
	if needsRepo {
		if repoCfg.LocalPath == "" {
			return worktreeResult{}, func() {}, fmt.Errorf("operator %q needs repo local_path but it's empty in config", op.Name)
		}
		return setupWorktree(ctx, repoCfg, fullName, issueNum, startedAt, overrideBranch)
	}

	// Analysis operators need read access to the repo source so the LLM can
	// grep/read actual code rather than guessing from issue text alone.
	// Use a persistent analysis worktree that is always reset to origin/<base>
	// before use so the operator sees the latest committed code.
	if repoCfg.LocalPath != "" {
		return ensureAnalysisWorktreeContext(ctx, repoCfg, fullName)
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
	return validateLocalPathOriginContext(context.Background(), localPath, fullName)
}
func validateLocalPathOriginContext(ctx context.Context, localPath, fullName string) error {
	out, err := exec.CommandContext(ctx, "git", "-C", localPath, "remote", "get-url", "origin").Output()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
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
	wtRoot := filepath.Join(snapshot.StateDir(), "worktrees")

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

// issueWorktreeDirRE matches the issue-<N>-<timestamp> directory naming
// scheme used by setupWorktree for per-issue implement worktrees.
var issueWorktreeDirRE = regexp.MustCompile(`^issue-(\d+)-`)

// pruneEmptyIssueWorktrees scans ~/.clawflow/worktrees/<slug>/issue-<N>-* for
// every enabled repo and removes any worktree that has no partial work
// (detectPartialWork returns false) and is not currently locked by an
// in-flight run. This is the one-time-and-ongoing GC for the empty-shell
// accumulation described in issue #329: before the runOneOperator cleanup
// fix, every "failed" status preserved its worktree regardless of content,
// so early-exit failures (claude erroring before writing any file) piled up
// permanent empty directories that findExistingWorktree would never resume
// and no other cleanup path ever collected.
//
// Runs during each `clawflow run` reconcile pass, same as
// pruneOrphanedAnalysisWorktrees. Skips locked issues so a worktree backing
// an in-flight run is never touched.
//
// Returns the number of worktrees removed.
func pruneEmptyIssueWorktrees(cfg *config.Config) int {
	wtRoot := filepath.Join(snapshot.StateDir(), "worktrees")

	removed := 0
	for fullName, repoCfg := range cfg.Repos {
		if !repoCfg.Enabled || repoCfg.LocalPath == "" {
			continue
		}
		base := repoCfg.BaseBranch
		if base == "" {
			base = "main"
		}
		slug := strings.ReplaceAll(fullName, "/", "__")
		slugDir := filepath.Join(wtRoot, slug)
		entries, err := os.ReadDir(slugDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "issue-") {
				continue
			}
			m := issueWorktreeDirRE.FindStringSubmatch(e.Name())
			if m == nil {
				continue
			}
			issueNum, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			// Never touch a worktree backing a run that's currently
			// in flight, even if it looks empty right now (it may be
			// about to receive its first write).
			if snapshot.IsLocked(fullName, issueNum) {
				continue
			}
			candidate := filepath.Join(slugDir, e.Name())
			if _, err := os.Stat(filepath.Join(candidate, ".git")); err != nil {
				continue
			}
			previous := previousDevelopmentRun(fullName, issueNum, time.Now())
			if sessionResumeReason(previous, config.RoleOperator, configuredDevelopmentProfile(config.RoleOperator), candidate, 0, false) == "" {
				continue // Native context is useful even before the first edit.
			}
			if hasWork, _, _ := detectPartialWork(candidate, base); hasWork {
				continue // real WIP — leave it for resume
			}
			cleanClaudeWorktrees(repoCfg.LocalPath, candidate)
			rm := exec.Command("git", "-C", repoCfg.LocalPath, "worktree", "remove", "--force", candidate)
			rm.Stdout = os.Stderr
			rm.Stderr = os.Stderr
			if rmErr := rm.Run(); rmErr != nil {
				fmt.Fprintf(os.Stderr, "  ⚠ prune empty worktree %s: %v\n", candidate, rmErr)
				continue
			}
			fmt.Fprintf(os.Stderr, "  ✓ pruned empty issue worktree: %s\n", candidate)
			removed++
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
	return ensureAnalysisWorktreeContext(context.Background(), repoCfg, fullName)
}
func ensureAnalysisWorktreeContext(ctx context.Context, repoCfg config.Repo, fullName string) (worktreeResult, func(), error) {
	if err := ctx.Err(); err != nil {
		return worktreeResult{}, func() {}, err
	}

	base := repoCfg.BaseBranch
	if base == "" {
		base = "main"
	}
	slug := strings.ReplaceAll(fullName, "/", "__")
	localPath := repoCfg.LocalPath
	wtPath := filepath.Join(snapshot.StateDir(), "worktrees", slug, "analysis-"+base)

	// Validate that localPath's git origin matches fullName before we
	// register any worktree against it. A misconfigured local_path (e.g.
	// pointing at the wrong clone, or shared across repos) would cause
	// git to register the worktree entry inside the wrong repository's
	// .git/worktrees/, producing the cross-project contamination described
	// in issue #211.
	if err := validateLocalPathOriginContext(ctx, localPath, fullName); err != nil {
		return worktreeResult{}, func() {}, err
	}

	// Serialize setup/refresh within this process so concurrent analysis
	// operators on the same repo don't race on fetch+reset.
	mu := getAnalysisWorktreeLock(slug, base)
	for !mu.TryLock() {
		select {
		case <-ctx.Done():
			return worktreeResult{}, func() {}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
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
		if out, err := runGitWithRetryOutputContext(ctx, func() *exec.Cmd {
			return exec.CommandContext(ctx, "git", "-C", localPath, "fetch", "origin", base)
		}); err != nil {
			if ctx.Err() != nil {
				return worktreeResult{}, func() {}, ctx.Err()
			}
			return worktreeResult{}, func() {}, describeGitFetchFailure(
				base, localPath, out, branch.RemoteDefaultBranch(localPath), err)
		}

		if err := runGitWithRetryContext(ctx, func() *exec.Cmd {
			return exec.CommandContext(ctx, "git", "-C", localPath, "worktree", "add", "--detach", wtPath, "origin/"+base)
		}); err != nil {
			// Fall back to the local base branch ref (e.g. brand-new clone
			// that hasn't pushed yet and has no origin/<base> remote ref).
			fmt.Fprintf(os.Stderr, "  ⚠ worktree add origin/%s failed, falling back to local %s\n", base, base)
			if err2 := runGitWithRetryContext(ctx, func() *exec.Cmd {
				return exec.CommandContext(ctx, "git", "-C", localPath, "worktree", "add", "--detach", wtPath, base)
			}); err2 != nil {
				return worktreeResult{}, func() {}, fmt.Errorf("git worktree add failed (origin/%s: %v; %s: %w)", base, err, base, err2)
			}
		}
		fmt.Fprintf(os.Stderr, "  ✓ analysis worktree initialized (detached HEAD at origin/%s)\n", base)
	} else {
		// Existing worktree: fetch + reset to latest origin/<base>.
		fmt.Fprintf(os.Stderr, "  → analysis worktree: refreshing to origin/%s\n", base)
		if out, err := runGitWithRetryOutputContext(ctx, func() *exec.Cmd {
			return exec.CommandContext(ctx, "git", "-C", localPath, "fetch", "origin", base)
		}); err != nil {
			if ctx.Err() != nil {
				return worktreeResult{}, func() {}, ctx.Err()
			}
			return worktreeResult{}, func() {}, describeGitFetchFailure(
				base, localPath, out, branch.RemoteDefaultBranch(localPath), err)
		}

		resetCmd := exec.CommandContext(ctx, "git", "-C", wtPath, "reset", "--hard", "origin/"+base)
		resetCmd.Stdout = os.Stderr
		resetCmd.Stderr = os.Stderr
		if err := resetCmd.Run(); err != nil {
			return worktreeResult{}, func() {}, fmt.Errorf("git reset --hard origin/%s: %w", base, err)
		}

		cleanCmd := exec.CommandContext(ctx, "git", "-C", wtPath, "clean", "-fdx")
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

// detectPartialWork inspects a worktree directory and reports whether it
// holds any work worth preserving: uncommitted changes (working tree or
// staged), untracked files, or commits ahead of origin/<base>. This is the
// single source of truth for "does this worktree have partial work" —
// shared by findExistingWorktree (resume decision) and the post-run cleanup
// decision in runOneOperator, so the two can no longer disagree about an
// empty worktree the way they did before issue #329 (worktrees preserved on
// status="failed" that findExistingWorktree would never resume because they
// had no content, and no cleanup path ever collected them either).
//
// Returns hasWork plus a diff summary and the checked-out branch name for
// callers that want to log/report on the discovered work.
func detectPartialWork(candidate, base string) (hasWork bool, diffStat, branch string) {
	return detectPartialWorkContext(context.Background(), candidate, base)
}
func detectPartialWorkContext(ctx context.Context, candidate, base string) (hasWork bool, diffStat, branch string) {
	// Check for uncommitted changes
	diffCmd := exec.CommandContext(ctx, "git", "-C", candidate, "diff", "--stat", "HEAD")
	diffOut, _ := diffCmd.Output()
	// Check for staged changes
	stagedCmd := exec.CommandContext(ctx, "git", "-C", candidate, "diff", "--stat", "--cached")
	stagedOut, _ := stagedCmd.Output()
	// Check for untracked files
	untrackedCmd := exec.CommandContext(ctx, "git", "-C", candidate, "ls-files", "--others", "--exclude-standard")
	untrackedOut, _ := untrackedCmd.Output()
	// Check current branch
	branchCmd := exec.CommandContext(ctx, "git", "-C", candidate, "rev-parse", "--abbrev-ref", "HEAD")
	branchOut, _ := branchCmd.Output()
	currentBranch := strings.TrimSpace(string(branchOut))

	hasUncommitted := len(strings.TrimSpace(string(diffOut))) > 0 ||
		len(strings.TrimSpace(string(stagedOut))) > 0 ||
		len(strings.TrimSpace(string(untrackedOut))) > 0

	// Check for commits ahead of origin/base (branch was created and committed to)
	hasCommitsAhead := false
	if currentBranch != "" && currentBranch != "HEAD" {
		aheadCmd := exec.CommandContext(ctx, "git", "-C", candidate, "log", "--oneline", "origin/"+base+"..HEAD")
		aheadOut, _ := aheadCmd.Output()
		hasCommitsAhead = len(strings.TrimSpace(string(aheadOut))) > 0
	}

	if !hasUncommitted && !hasCommitsAhead {
		return false, "", currentBranch
	}

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
		aheadCmd := exec.CommandContext(ctx, "git", "-C", candidate, "log", "--oneline", "origin/"+base+"..HEAD")
		aheadOut, _ := aheadCmd.Output()
		statParts = append(statParts, "(commits ahead of origin/"+base+")\n"+strings.TrimSpace(string(aheadOut)))
	}
	return true, strings.Join(statParts, "\n"), currentBranch
}

// findExistingWorktree scans ~/.clawflow/worktrees/<slug>/issue-<N>-* for a
// worktree that has uncommitted changes or commits ahead of origin/<base>.
// Returns the path and a short diff summary if found, or ("", "", "") if none.
func findExistingWorktree(parent string, issueNum int, localPath, base string) (wtPath, diffStat, branch string) {
	return findExistingWorktreeContext(context.Background(), parent, issueNum, localPath, base)
}
func findExistingWorktreeContext(ctx context.Context, parent string, issueNum int, localPath, base string) (wtPath, diffStat, branch string) {
	prefix := fmt.Sprintf("issue-%d-", issueNum)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return "", "", ""
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return "", "", ""
		}
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		candidate := filepath.Join(parent, e.Name())
		// Check it's a valid git worktree (has .git file)
		if _, err := os.Stat(filepath.Join(candidate, ".git")); err != nil {
			continue
		}
		if hasWork, stat, currentBranch := detectPartialWorkContext(ctx, candidate, base); hasWork {
			return candidate, stat, currentBranch
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
//
// mergeRetryAttempts is how many EXTRA merge attempts auto-merge makes
// after a transient base-moved race, on top of the initial attempt.
const mergeRetryAttempts = 2

// mergeRetryDelay is the pause between transient-merge retries, giving
// GitHub a moment to settle after a sibling PR lands on the base.
const mergeRetryDelay = 3 * time.Second

// isTransientMergeError reports whether a merge failure is a known
// retryable race rather than a permanent rejection. GitHub returns
// HTTP 405 "Base branch was modified" when the base HEAD moves between
// the mergeability check and the merge call (a sibling PR landing at
// the same instant) — GitHub's own docs say to re-fetch the base and
// retry. Everything else (branch protection, auth, real conflicts) is
// permanent and must NOT be retried. See issue #224.
func isTransientMergeError(s string) bool {
	for _, pattern := range []string{
		"Base branch was modified",
		"405",
	} {
		if strings.Contains(s, pattern) {
			return true
		}
	}
	return false
}

// mergeWithRetry runs merge() and, on a transient base-moved race (see
// isTransientMergeError), retries up to mergeRetryAttempts times. Before
// each retry it re-confirms via recheck() that the base is still
// MergeStatusClean — if a real conflict appeared in the meantime it
// stops and surfaces that instead of blindly hammering the merge API.
// sleep is injected so tests can run without real delays. The initial
// attempt plus retries means at most 1+mergeRetryAttempts merge calls.
func mergeWithRetry(prefix string, merge func() error, recheck func() (vcs.MergeStatus, error), sleep func(time.Duration)) error {
	err := merge()
	if err == nil {
		return nil
	}
	for attempt := 1; attempt <= mergeRetryAttempts && isTransientMergeError(err.Error()); attempt++ {
		fmt.Fprintf(os.Stderr, "%s → auto-merge: transient race (%v), retry %d/%d after re-checking base\n",
			prefix, err, attempt, mergeRetryAttempts)
		sleep(mergeRetryDelay)
		status, rerr := recheck()
		if rerr != nil {
			// Can't confirm the base is still clean — don't risk a
			// blind retry. Surface the original transient error.
			fmt.Fprintf(os.Stderr, "%s ⚠ auto-merge: mergeability re-check failed: %v\n", prefix, rerr)
			return err
		}
		if status != vcs.MergeStatusClean {
			return fmt.Errorf("base no longer mergeable after transient failure (status: %s): %w", status, err)
		}
		if err = merge(); err == nil {
			fmt.Fprintf(os.Stderr, "%s ✓ auto-merge: succeeded on retry %d\n", prefix, attempt)
			return nil
		}
	}
	return err
}

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
// scanPhaseBudget bounds the VCS scan/poll phase (listing open issues +
// enriching with sub-issues across every repo). The scan is normally a few
// seconds; a slow or rate-limited endpoint is the pathological case. We give
// it a generous fraction of the per-operator timeout so a healthy poll never
// trips it, while still firing long before the overall-run watchdog so a hung
// poll can't hold run.lock for hours (issue #221).
func scanPhaseBudget(perOp time.Duration) time.Duration {
	if perOp <= 0 {
		perOp = 60 * time.Minute
	}
	b := perOp / 4
	if b < 5*time.Minute {
		b = 5 * time.Minute
	}
	if b > 15*time.Minute {
		b = 15 * time.Minute
	}
	return b
}

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

// hardenedGitEnv augments a git command's environment so it can never
// block on interactive input. GIT_TERMINAL_PROMPT=0 makes git fail
// instead of prompting for HTTP credentials; the BatchMode/ConnectTimeout
// SSH options do the same for git-over-SSH (no passphrase prompt, bounded
// TCP connect). base may be nil, in which case the current environment is
// used. See issue #216 — the run hung as an `S+` foreground process with
// ~0 CPU, the classic signature of git waiting on a credential prompt.
func hardenedGitEnv(base []string) []string {
	return gitexec.Env(base)
}

// Local operations retain their pre-existing watchdog. Network operations
// use the configurable shared transport deadline instead.
var gitAttemptTimeout = 3 * time.Minute

// runBoundedGit runs a git command with a hard wall-clock ceiling. The
// command is started in its own process group so that on timeout we can
// SIGKILL the entire group (git plus any ssh/credential-helper children),
// not just the git parent. Returns the command's own error on normal
// completion, or a descriptive timeout error if the ceiling was hit.
// timeout <= 0 means "no ceiling" (delegates straight to cmd.Run).
func runBoundedGit(cmd *exec.Cmd, timeout time.Duration) error {
	return runBoundedGitContext(context.Background(), cmd, timeout)
}
func runBoundedGitContext(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		<-done
		return ctx.Err()
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
	return runGitWithRetryContext(context.Background(), makeCmd)
}
func runGitWithRetryContext(ctx context.Context, makeCmd func() *exec.Cmd) error {
	_, err := runGitWithRetryOutputContext(ctx, makeCmd)
	return err
}

// runGitWithRetryOutput is runGitWithRetry plus the git combined output of the
// final attempt. Callers that need to classify *why* git failed (network vs
// auth vs "remote has no such ref") need the stderr text — the exit status
// alone is exit 128 for all three, which is what made the base_branch
// misconfiguration in issue #300 read as a network outage for three weeks.
func runGitWithRetryOutput(makeCmd func() *exec.Cmd) (string, error) {
	return runGitWithRetryOutputContext(context.Background(), makeCmd)
}
func runGitWithRetryOutputContext(ctx context.Context, makeCmd func() *exec.Cmd) (string, error) {
	const maxGitRetries = 5
	const baseGitDelay = 100 * time.Millisecond

	for attempt := 0; attempt <= maxGitRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		cmd := makeCmd()
		var buf bytes.Buffer
		// Tee output: observable on stderr AND captured for error classification.
		mw := io.MultiWriter(os.Stderr, &buf)
		cmd.Stdout = mw
		cmd.Stderr = mw
		var err error
		if gitexec.IsNetwork(cmd.Args[1:]...) {
			err = gitexec.Run(ctx, cmd)
		} else {
			cmd.Env = hardenedGitEnv(cmd.Env)
			err = runBoundedGitContext(ctx, cmd, gitAttemptTimeout)
		}
		if err == nil {
			return buf.String(), nil
		}

		output := buf.String()
		if errors.Is(err, gitexec.ErrTimeout) || !isGitLockError(output) {
			// Permanent error (incl. attempt timeout) — return
			// immediately, no retry. A timeout won't fix itself by
			// retrying and would only delay surfacing the failure.
			return output, err
		}
		if attempt == maxGitRetries {
			return output, fmt.Errorf("%w (retried %d times after git lock conflicts)", err, maxGitRetries)
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
		select {
		case <-ctx.Done():
			return output, ctx.Err()
		case <-time.After(delay):
		}
	}
	return "", nil // unreachable
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
func setupWorktree(ctx context.Context, repoCfg config.Repo, fullName string, issueNum int, startedAt time.Time, overrideBranch string) (worktreeResult, func(), error) {
	if err := ctx.Err(); err != nil {
		return worktreeResult{}, func() {}, err
	}
	slug := strings.ReplaceAll(fullName, "/", "__")
	parent := filepath.Join(snapshot.StateDir(), "worktrees", slug)
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
	if existingPath, diffStat, branch := findExistingWorktreeContext(ctx, parent, issueNum, localPath, base); existingPath != "" {
		fmt.Fprintf(os.Stderr, "  ✓ resuming existing worktree: %s (branch: %s)\n", existingPath, branch)
		result := worktreeResult{
			Path:       existingPath,
			Resumed:    true,
			DiffStat:   diffStat,
			BranchName: branch,
		}
		cleanup := func() {
			fmt.Fprintln(os.Stderr, "  → cleanup: removing worktree")
			// Clean up Claude Code CLI sub-worktrees created inside our worktree
			// during operator execution (e.g. <wtPath>/.claude/worktrees/wf_*).
			// These are NOT cleaned up by claude -p itself when run non-interactively,
			// causing git to refuse branch deletion later (issue #297).
			cleanClaudeWorktrees(localPath, existingPath)
			rm := exec.CommandContext(ctx, "git", "-C", localPath, "worktree", "remove", "--force", existingPath)
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
	if out, err := runGitWithRetryOutputContext(ctx, func() *exec.Cmd {
		return exec.CommandContext(ctx, "git", "-C", localPath, "fetch", "origin", base)
	}); err != nil {
		if ctx.Err() != nil {
			return worktreeResult{}, func() {}, ctx.Err()
		}
		// Non-fatal here (we can still start from a cached origin/<base>),
		// but name the cause so a misconfigured base_branch is visible in
		// the implement path too, not just the analysis path.
		fmt.Fprintf(os.Stderr, "  ⚠ fetch failed (continuing): %v\n",
			describeGitFetchFailure(base, localPath, out, branch.RemoteDefaultBranch(localPath), err))
	}

	fmt.Fprintf(os.Stderr, "  → setup: creating worktree at %s\n", wtPath)
	if err := runGitWithRetryContext(ctx, func() *exec.Cmd {
		return exec.CommandContext(ctx, "git", "-C", localPath, "worktree", "add", "--detach", wtPath, "origin/"+base)
	}); err != nil {
		// Fall back to the local base branch ref. Brand-new clones may
		// not have origin/<base> yet (e.g. the user just `git init`'d
		// and added a remote without pushing anything).
		fmt.Fprintf(os.Stderr, "  ⚠ worktree add origin/%s failed, falling back to local %s\n", base, base)
		if err2 := runGitWithRetryContext(ctx, func() *exec.Cmd {
			return exec.CommandContext(ctx, "git", "-C", localPath, "worktree", "add", "--detach", wtPath, base)
		}); err2 != nil {
			return worktreeResult{}, func() {}, fmt.Errorf("git worktree add failed (origin/%s: %v; %s: %w)", base, err, base, err2)
		}
	}
	fmt.Fprintf(os.Stderr, "  ✓ worktree ready (detached HEAD at origin/%s)\n", base)

	result := worktreeResult{Path: wtPath}
	cleanup := func() {
		fmt.Fprintln(os.Stderr, "  → cleanup: removing worktree")
		// Clean up Claude Code CLI sub-worktrees created inside our worktree
		// during operator execution (e.g. <wtPath>/.claude/worktrees/wf_*).
		// These are NOT cleaned up by claude -p itself when run non-interactively,
		// causing git to refuse branch deletion later (issue #297).
		cleanClaudeWorktrees(localPath, wtPath)
		rm := exec.CommandContext(ctx, "git", "-C", localPath, "worktree", "remove", "--force", wtPath)
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

// cleanClaudeWorktrees removes the sub-worktrees that Claude Code CLI creates
// inside a ClawFlow worktree during non-interactive (claude -p) execution.
// Claude Code creates these under <wtPath>/.claude/worktrees/wf_<uuid> for
// tool-call isolation but does not clean them up after the process exits.
// Without this cleanup, git refuses to delete the associated branches
// (worktree-wf_<uuid>), breaking Play 1 branch cleanup (issue #297).
//
// localPath is the repo root (used for `git -C`).
// wtPath is the ClawFlow worktree directory that may contain a .claude/worktrees/ subdir.
func cleanClaudeWorktrees(localPath, wtPath string) {
	claudeWtDir := filepath.Join(wtPath, ".claude", "worktrees")
	entries, err := os.ReadDir(claudeWtDir)
	if err != nil {
		// Directory doesn't exist — nothing to clean, not an error.
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(claudeWtDir, e.Name())
		rm := exec.Command("git", "-C", localPath, "worktree", "remove", "--force", sub)
		rm.Stderr = os.Stderr
		if err := rm.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ claude worktree remove failed (%s): %v\n", sub, err)
		} else {
			fmt.Fprintf(os.Stderr, "  ✓ claude worktree removed: %s\n", sub)
		}
	}
}
