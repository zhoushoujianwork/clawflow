package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	rootmod "github.com/zhoushoujianwork/clawflow"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/projectpm"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// modelForOperator picks which credentials-configured model an
// operator should run on. evaluate-* operators read existing context
// and produce structured analysis, so they get the heavier eval model
// (Opus by default). Everything else (implement, reply-comment,
// user-supplied skills) gets the cheaper operator model (Sonnet).
// `creds` may be nil — the Effective*Model helpers handle that.
func modelForOperator(creds *config.Credentials, opName string) string {
	if strings.HasPrefix(opName, "evaluate-") {
		return creds.EffectiveEvalModel()
	}
	return creds.EffectiveOperatorModel()
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

	// Reconcile any runs whose on-disk state is inconsistent (stuck
	// "running", missing meta.json) BEFORE we touch anything else, so the
	// dashboard's first refresh of this run picks up the fixed state. The
	// staleAfter threshold matches the per-operator default timeout — any
	// run still showing "running" past that is definitively dead.
	if n, err := snapshot.ReconcileStaleRuns(timeout); err == nil && n > 0 {
		fmt.Fprintf(os.Stderr, "✓ reconciled %d stale run(s) on disk\n", n)
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ reconcile stale runs: %v\n", err)
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
		executeHere := onlyRepo == "" || onlyRepo == fullName
		repoPending, repoJobs, err := scanRepoOnce(reg, fullName, repoCfg, executeHere, onlyIssue, &globalIssues)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error on %s: %v\n", fullName, err)
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
		if n, err := projectpm.Schedule(ctx, timeout); err != nil {
			fmt.Fprintf(os.Stderr, "[pm] schedule: %v\n", err)
		} else if n > 0 {
			fmt.Fprintf(os.Stderr, "[pm] woke %d project(s)\n", n)
		}
	}
	return nil
}

// runJob is one (issue, operator) pair that scanRepoOnce decided is
// eligible to fire. The worker pool in runJobsParallel consumes these.
type runJob struct {
	op      *operator.Operator
	sub     *operator.Subject
	repo    string
	repoCfg config.Repo
	client  vcs.Client
}

// firedKey identifies a (repo, issue, operator) triple that ran to a
// non-empty success/skip outcome. Used to dedup pending entries that
// already fired in this pass.
type firedKey struct {
	Repo        string
	IssueNumber int
	Operator    string
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
			if firstMatch.Name == "implement" && repoCfg.LocalPath == "" {
				debugf("  · skipping %s on #%d: implement requires local_path but it's empty", fullName, iss.Number)
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
	
	// Collect all issues (open and closed) for dashboard display
	repoIssues := []snapshot.IssueEntry{}
	
	// Add open issues
	for _, iss := range issues {
		repoIssues = append(repoIssues, snapshot.IssueEntry{
			Repo:        fullName,
			IssueNumber: iss.Number,
			IssueTitle:  iss.Title,
			Labels:      append([]string(nil), iss.Labels...),
			State:       iss.State,
			CapturedAt:  capturedAt,
		})
	}
	
	// Add closed issues (recent 10)
	closedIssues, err := client.ListIssues(fullName, "closed", nil)
	if err == nil {
		// Limit to recent 10 closed issues to keep dashboard clean
		limit := 10
		if len(closedIssues) > limit {
			closedIssues = closedIssues[:limit]
		}
		for _, iss := range closedIssues {
			repoIssues = append(repoIssues, snapshot.IssueEntry{
				Repo:        fullName,
				IssueNumber: iss.Number,
				IssueTitle:  iss.Title,
				Labels:      append([]string(nil), iss.Labels...),
				State:       iss.State,
				CapturedAt:  capturedAt,
			})
		}
	}
	
	// Add to global collection (thread-safe since we're in single-threaded context)
	*allIssues = append(*allIssues, repoIssues...)

	return pending, jobs, nil
}

// runJobsParallel dispatches `jobs` across `workers` goroutines, gating
// concurrency with two in-process locks: per-issue (always) so two
// operators never race on the same (repo, issue), and per-repo (only
// for `implement`) so worktree add / git fetch on the shared local
// clone stay serialized. Returns the set of jobs whose operator
// produced output (i.e. fired), so the caller can deduplicate pending
// entries.
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
		issueLocks sync.Map // key: "<repo>#<issue>" → *sync.Mutex
		repoLocks  sync.Map // key: "<repo>"          → *sync.Mutex
		fired      []firedKey
		firedMu    sync.Mutex
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
				iMu := mu(&issueLocks, fmt.Sprintf("%s#%d", j.repo, j.sub.Number))
				iMu.Lock()
				// `implement` mutates a shared local clone (worktree
				// add + fetch), so two implement jobs in the same repo
				// must serialize. Read-only operators don't touch the
				// clone and run unrestricted.
				var rMu *sync.Mutex
				if j.op.Name == "implement" {
					rMu = mu(&repoLocks, j.repo)
					rMu.Lock()
				}
				didFire := runOneOperator(ctx, j, timeout)
				if rMu != nil {
					rMu.Unlock()
				}
				iMu.Unlock()
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
// Returns true when the operator produced non-empty stdout (i.e.
// "fired" — its outcome label was applied or its comment was posted).
// All log lines are prefixed with "[<repo>#<issue> <op>]" so output
// from concurrent workers stays disentangleable.
func runOneOperator(ctx context.Context, j *runJob, timeout time.Duration) bool {
	prefix := fmt.Sprintf("[%s#%d %s]", j.repo, j.sub.Number, j.op.Name)
	fmt.Printf("%s → start\n", prefix)

	// Cross-process lock: acquire a local lockfile so other clawflow run
	// processes (cron, web scheduler) skip this issue. If the lock is
	// already held by a live process, bail out.
	if err := snapshot.AcquireLock(j.repo, j.sub.Number, j.op.Name); err != nil {
		fmt.Fprintf(os.Stderr, "%s ⚠ lock failed (another process owns it): %v\n", prefix, err)
		return false
	}
	defer snapshot.ReleaseLock(j.repo, j.sub.Number)

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
		StartedAt:   startedAt.UTC(),
		Status:      "running",
	}
	if err := snapshot.WriteRunMeta(runDir, runningMeta); err != nil {
		fmt.Fprintf(os.Stderr, "%s ✗ initial run meta: %v\n", prefix, err)
	}
	if _, err := snapshot.WriteRunsIndex(50); err != nil {
		fmt.Fprintf(os.Stderr, "%s ⚠ snapshot runs index (running): %v\n", prefix, err)
	}

	workdir, cleanup, err := resolveWorkdir(j.op, j.repoCfg, j.repo, j.sub.Number, startedAt)
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
		return false
	}
	fmt.Fprintf(os.Stderr, "%s ✓ workdir ready: %s\n", prefix, workdir)

	comments, _ := j.client.ListIssueComments(j.repo, j.sub.Number)

	eventsFile, eventsErr := os.Create(filepath.Join(runDir, "events.jsonl"))
	if eventsErr != nil {
		fmt.Fprintf(os.Stderr, "%s ⚠ events sink: %v\n", prefix, eventsErr)
	}

	creds, _ := config.LoadCredentials()
	model := modelForOperator(creds, j.op.Name)
	fmt.Fprintf(os.Stderr, "%s → claude (model %s, timeout %s)\n", prefix, model, timeout)
	debugf("%s using model %q", prefix, model)
	runStart := time.Now()
	output, outcome, runErr := operator.Run(ctx, j.op, j.sub, j.client, operator.RunOptions{
		Repo:        j.repo,
		Workdir:     workdir,
		Timeout:     timeout,
		Comments:    comments,
		Model:       model,
		EventWriter: eventsFile,
	})
	runDur := time.Since(runStart).Round(time.Second)
	if eventsFile != nil {
		_ = eventsFile.Close()
	}
	if runErr != nil {
		fmt.Fprintf(os.Stderr, "%s ✗ claude failed: %v\n", prefix, runErr)
	} else {
		fmt.Fprintf(os.Stderr, "%s ✓ claude finished in %s\n", prefix, runDur)
	}
	cleanup()

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
	case runErr != nil:
		rm.Status = "failed"
		rm.Error = runErr.Error()
	case output == "":
		rm.Status = "skipped"
	default:
		rm.Status = "success"
	}
	if u, uerr := snapshot.ExtractUsage(filepath.Join(runDir, "events.jsonl")); uerr == nil {
		rm.Usage = u
	}
	if err := snapshot.WriteRunMeta(runDir, rm); err != nil {
		fmt.Fprintf(os.Stderr, "%s ⚠ run meta: %v\n", prefix, err)
	}

	// Post-run automation: auto-approve and auto-merge
	if rm.Status == "success" {
		runPostAutomation(j, outcome, output, prefix)
	}

	switch rm.Status {
	case "success":
		fmt.Printf("%s ✓ done\n", prefix)
		return true
	case "skipped":
		return true
	default:
		checkCircuitBreaker(j, prefix)
		return false
	}
}

var prURLRE = regexp.MustCompile(`(?:pull|merge_requests)/(\d+)`)

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
