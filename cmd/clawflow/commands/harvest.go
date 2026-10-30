package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
)

// Harvest timing. A run needs room for planning plus review, so a repo is
// skipped when less than harvestMinRemaining is left once the safety margin
// before the self-watchdog is reserved.
const (
	harvestWatchdogMargin = 2 * time.Minute
	harvestMinRemaining   = 5 * time.Minute
	// harvestSummaryLimit bounds the reviewer summary echoed into a comment.
	harvestSummaryLimit = 2000
)

// runAcceptanceFn is the acceptance entry point; tests replace it.
var runAcceptanceFn = runAcceptance

// harvestNow is the clock for the infrastructure cooldown; tests replace it.
var harvestNow = time.Now

// runHarvestPhase is the automatic acceptance phase of `clawflow run`. The
// implementation agent opens the PR; a separate acceptance agent judges it here;
// deterministic code in a later sweep merges. Discovery occupies a worker
// briefly; background reviews reserve their own machine-wide agent slots.
//
// Reviews are discovered under the pass lock, then launched round-robin across
// repositories. Launching never waits for a review; merges remain in the sweep.
//
// A targeted run (onlyRepo and onlyIssue both set) processes only that repo,
// which may be disabled, and only PRs whose closing keyword names the issue.
func runHarvestPhase(ctx context.Context, cfg *config.Config, onlyRepo string, onlyIssue int, timeout time.Duration, watchdogDeadline time.Time) []firedKey {
	if cfg == nil || (onlyIssue != 0 && onlyRepo == "") {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	repos := cfg.EnabledRepos()
	if onlyIssue != 0 {
		repos = map[string]config.Repo{}
		if repoCfg, ok := cfg.Repos[onlyRepo]; ok {
			repos[onlyRepo] = repoCfg
		}
	}
	names := make([]string, 0, len(repos))
	for name := range repos {
		names = append(names, name)
	}
	slices.Sort(names)

	var queues [][]harvestTask
	for _, fullName := range names {
		if ctx.Err() != nil {
			break
		}
		if onlyRepo != "" && onlyRepo != fullName {
			continue
		}
		rc := repos[fullName]
		rc.CoordinatorMode = cfg.Settings.Coordinator.Enabled
		queues = append(queues, harvestCandidates(ctx, fullName, rc, onlyIssue, timeout, watchdogDeadline))
	}
	return dispatchHarvestQueues(ctx, queues, cfg.Settings.MaxConcurrentAgents)
}

func dispatchHarvestQueues(ctx context.Context, queues [][]harvestTask, machineLimit int) []firedKey {
	var failed []firedKey
	for round := 0; ; round++ {
		any := false
		for _, tasks := range queues {
			if round >= len(tasks) {
				continue
			}
			any = true
			task := tasks[round]
			if ctx.Err() != nil {
				return failed
			}
			var err error
			if task.rc.CoordinatorMode {
				err = enqueueWorkerAcceptanceFn(ctx, task.client, task.repo, task.rc, task.pr, task.timeout)
			} else {
				err = launchHarvestFn(ctx, task, machineLimit)
			}
			if err != nil {
				if errors.Is(err, errHarvestBusy) {
					runLog.Info("run/harvest_skip", "repo", task.repo, "pr", task.pr.Number, "reason", "acceptance_capacity_or_pr_busy")
					continue
				}
				runLog.Warn("run/acceptance_launch", "repo", task.repo, "pr", task.pr.Number, "err", err.Error())
				failed = append(failed, firedKey{Repo: task.repo, IssueNumber: extractClosesIssue(task.pr.Body), Operator: "acceptance"})
			}
		}
		if !any {
			break
		}
	}
	return failed
}

type harvestTask struct {
	repo    string
	rc      config.Repo
	client  vcs.Client
	pr      vcs.PR
	timeout time.Duration
}

// harvestCandidates captures eligible PRs without holding a lock across reviews.
func harvestCandidates(ctx context.Context, fullName string, repoCfg config.Repo, onlyIssue int, timeout time.Duration, watchdogDeadline time.Time) []harvestTask {
	skip := func(reason string, extra ...any) {
		runLog.Info("run/harvest_skip", append([]any{"repo", fullName, "reason", reason}, extra...)...)
	}
	if !repoCfg.AutoMerge {
		return nil // Not harvest-eligible; stay quiet like the sweep.
	}
	// Re-read current config: enrollment can change between load and now.
	enrolled, err := acceptanceEnrollment(fullName)
	if err != nil {
		skip("enrollment_unreadable", "err", err.Error())
		return nil
	}
	if !enrolled {
		return nil
	}

	// Per-run timeout: leave a margin before the self-watchdog force-exits.
	if timeout <= 0 {
		timeout = 60 * time.Minute
	}
	runTimeout := timeout
	if remaining := time.Until(watchdogDeadline) - harvestWatchdogMargin; remaining < runTimeout {
		runTimeout = remaining
	}
	if runTimeout < harvestMinRemaining {
		skip("insufficient_budget", "remaining", runTimeout.Round(time.Second).String())
		return nil
	}

	client, err := acceptanceClientFactory(repoCfg)
	if err != nil {
		skip("client_error", "err", err.Error())
		return nil
	}
	if _, ok := client.(vcs.ConditionalMerger); !ok {
		skip("no_conditional_merge")
		return nil
	}
	setCtx := func(c context.Context) {
		if rc, ok := client.(interface{ SetRequestContext(context.Context) }); ok {
			rc.SetRequestContext(c)
		}
	}
	ctx = github.WithStage(ctx, "harvest")
	setCtx(ctx)
	if !pollingAllowed(client) {
		return nil
	}

	prs, err := client.ListOpenPRs(fullName)
	if err != nil {
		runLog.Warn("run/harvest_skip", "repo", fullName, "reason", "list_open_prs_error", "err", err.Error())
		return nil
	}

	state, err := loadHarvestSchedule(fullName)
	if err != nil {
		skip("schedule_unreadable", "err", err.Error())
		return nil
	}
	// Keep only open PR identities; closed PRs need no fairness history.
	open := map[int]bool{}
	for _, pr := range prs {
		open[pr.Number] = true
	}
	for pr := range state.Heads {
		if !open[pr] {
			delete(state.Heads, pr)
		}
	}
	var eligible []vcs.PR
	for _, pr := range prs {
		if ctx.Err() != nil {
			break
		}
		if onlyIssue != 0 && extractClosesIssue(pr.Body) != onlyIssue {
			continue
		}
		issueNum, _, prSkipped := autoMergeCandidate(client, fullName, pr)
		if prSkipped != nil {
			prSkipped.log("run/harvest_skip", fullName, pr.Number)
			continue
		}
		if repoCfg.CIRequired {
			status, ciErr := client.GetCIStatus(fullName, pr.Number)
			if ciErr != nil || (status != vcs.CIStatusSuccess && !(status == vcs.CIStatusPending && !repoCfg.CoordinatorMode && repoCfg.AcceptanceOverlapsCI())) {
				skip("ci_not_success", "pr", pr.Number, "issue", issueNum)
				continue
			}
		}
		_, latest, evErr := currentAcceptance(ctx, client, fullName, pr.Number)
		if publishErr := reconcileAcceptanceReport(ctx, client, latest); publishErr != nil {
			skip("report_publish_error", "pr", pr.Number, "err", publishErr.Error())
			continue
		}
		switch {
		case evErr == nil:
			// Already accepted: the sweep merges it.
			skip("evidence_present", "pr", pr.Number, "issue", issueNum)
			continue
		case !errors.Is(evErr, errAcceptanceMissing) || acceptanceStoreExists(fullName, pr.Number):
			// An existing record, unreadable evidence or a contract that cannot
			// be built (e.g. no development provenance). Only a latest record
			// that is an infrastructure attempt may be retried.
			d, info, infraErr := harvestInfraDecision(ctx, client, fullName, pr.Number, harvestNow())
			switch {
			case infraErr != nil || (d == harvestVerdict && !errors.Is(evErr, errAcceptanceMissing)):
				skip("evidence_not_missing", "pr", pr.Number, "issue", issueNum, "err", evErr.Error())
				continue
			case d == harvestInfraCoolingDown:
				skip("infra_cooldown", "pr", pr.Number, "issue", issueNum, "attempts", info.attempts, "retry_after", info.retryAt.UTC().Format(time.RFC3339))
				continue
			case d == harvestInfraExhausted:
				skip("infra_exhausted", "pr", pr.Number, "issue", issueNum, "attempts", info.attempts)
				autoMergeAttempt{ctx: ctx, client: client, repo: fullName, repoCfg: repoCfg, prNum: pr.Number, issueNum: issueNum, prefix: "[" + fullName + "]"}.
					notify(harvestInfraComment(fullName, pr.Number, info), harvestInfraMarker(info.digest))
				continue
			}
			runLog.Info("run/harvest_retry", "repo", fullName, "pr", pr.Number, "issue", issueNum, "attempts", info.attempts)
		}
		// Some providers list only MR summaries; pin an authoritative head
		// before handing the candidate to a background process.
		if pr.HeadSHA == "" {
			current, err := client.GetPR(fullName, pr.Number)
			if err != nil || current.HeadSHA == "" {
				skip("head_unavailable", "pr", pr.Number)
				continue
			}
			pr.HeadSHA = current.HeadSHA
		}
		eligible = append(eligible, pr)
	}
	eligible, priorities, err := orderHarvest(client, fullName, eligible)
	if err != nil {
		skip("order_unreadable", "err", err.Error())
		return nil
	}
	deferred := 0
	if selected := selectHarvest(eligible, state, priorities); selected > 0 {
		deferred = state.Previous
	}
	var tasks []harvestTask
	for len(eligible) > 0 && len(tasks) < repoCfg.AcceptanceParallelLimit() {
		selected := selectHarvest(eligible, state, priorities)
		pr := eligible[selected]
		tasks = append(tasks, harvestTask{fullName, repoCfg, client, pr, runTimeout})
		state.Previous = pr.Number
		state.Heads[pr.Number] = pr.HeadSHA
		eligible = append(eligible[:selected], eligible[selected+1:]...)
	}
	for _, pr := range eligible {
		reason := "harvest_slot_busy"
		if pr.Number == deferred {
			reason = "deferred_fairness"
		}
		skip(reason, "pr", pr.Number)
	}
	if err := saveHarvestSchedule(fullName, state); err != nil {
		skip("schedule_write_error", "err", err.Error())
		return nil
	}
	return tasks
}

// harvestRun runs one acceptance and, on a pass, attempts the conditional merge.
func harvestRun(ctx context.Context, client vcs.Client, setCtx func(context.Context), fullName string, repoCfg config.Repo, pr vcs.PR, issueNum int, runTimeout time.Duration) *firedKey {
	prefix := fmt.Sprintf("[%s]", fullName)
	start := time.Now()
	runLog.Info("run/harvest_start", "repo", fullName, "pr", pr.Number, "issue", issueNum, "timeout", runTimeout.Round(time.Second).String())
	fmt.Fprintf(os.Stderr, "%s → acceptance harvest: PR #%d\n", prefix, pr.Number)

	runCtx, cancel := context.WithTimeout(ctx, runTimeout)
	setCtx(runCtx)
	record, err := runAcceptanceFn(runCtx, client, fullName, pr.Number)
	cancel()
	setCtx(ctx)
	elapsed := time.Since(start).Round(time.Second).String()

	attempt := autoMergeAttempt{ctx: ctx, client: client, repo: fullName, repoCfg: repoCfg, prNum: pr.Number, issueNum: issueNum, prefix: prefix, ciPoll: true, quiet: true}
	switch {
	case err == nil:
		if record.Contract.EvidenceSchema != 1 && len(record.Findings) > 0 {
			attempt.notify(harvestNonpassComment(record), harvestMarker(record))
		}
		merged := false
		if repoCfg.AutoMerge {
			merged = attemptAutoMerge(attempt)
		}
		runLog.Info("run/harvest_end", "repo", fullName, "pr", pr.Number, "issue", issueNum, "verdict", string(record.Verdict), "planner_isolation", record.Plan.PlannerIsolation, "status", "accepted", "merged", merged, "duration", elapsed)
	case errors.Is(err, errAcceptanceNonpass):
		if record.Contract.EvidenceSchema != 1 {
			attempt.notify(harvestNonpassComment(record), harvestMarker(record))
		}
		runLog.Info("run/harvest_end", "repo", fullName, "pr", pr.Number, "issue", issueNum, "verdict", string(record.Verdict), "planner_isolation", record.Plan.PlannerIsolation, "status", "nonpass", "merged", false, "duration", elapsed)
		fmt.Fprintf(os.Stderr, "%s ✗ acceptance %s for PR #%d\n", prefix, record.Verdict, pr.Number)
	default:
		runLog.Warn("run/harvest_end", "repo", fullName, "pr", pr.Number, "issue", issueNum, "status", "error", "merged", false, "duration", elapsed, "err", err.Error())
		fmt.Fprintf(os.Stderr, "%s ⚠ acceptance harvest PR #%d: %v\n", prefix, pr.Number, err)
		return &firedKey{Repo: fullName, IssueNumber: issueNum, Operator: "acceptance"}
	}
	return nil
}

// Infrastructure retry policy. An attempt that ended without a reviewer verdict
// (setup, provider, inspection, storage or cancellation error) says nothing
// about the PR, so it is retried after a cooldown, at most harvestMaxInfra
// consecutive attempts per head. A new head, changed requirements
// or binary starts over. The execution entry point enforces the same cap.
const (
	harvestMaxInfra      = 2
	harvestInfraCooldown = 15 * time.Minute
)

type harvestDecision int

const (
	// harvestVerdict: the latest record is a reviewer verdict; never retried.
	harvestVerdict harvestDecision = iota
	// harvestInfraRetry: latest is an infrastructure attempt that may be retried.
	harvestInfraRetry
	harvestInfraCoolingDown
	harvestInfraExhausted
)

type harvestInfraInfo struct {
	attempts int
	retryAt  time.Time
	digest   string
}

// harvestInfraDecision classifies an existing, non-passing, non-missing
// evidence state for the current contract. Errors (contract cannot be built,
// store unreadable, corrupt or ambiguous records) mean "do not retry".
func harvestInfraDecision(ctx context.Context, client vcs.Client, repo string, pr int, now time.Time) (harvestDecision, harvestInfraInfo, error) {
	var info harvestInfraInfo
	c, _, _, _, err := acceptanceContract(ctx, client, repo, pr)
	if err != nil {
		return harvestVerdict, info, err
	}
	info.digest = acceptanceInfraKey(c)
	store, err := acceptance.OpenStore(acceptanceStorePath(repo, pr))
	if err != nil {
		return harvestVerdict, info, err
	}
	defer store.Close()
	attempts, err := store.InfrastructureAttempts(c)
	if err != nil {
		return harvestVerdict, info, err
	}
	info.attempts = attempts.Count
	if !attempts.LatestIsInfra {
		return harvestVerdict, info, nil
	}
	if attempts.Count == 0 {
		return harvestInfraRetry, info, nil
	}
	if attempts.Count >= harvestMaxInfra {
		return harvestInfraExhausted, info, nil
	}
	info.retryAt = attempts.LastAt.Add(harvestInfraCooldown)
	if now.Before(info.retryAt) {
		return harvestInfraCoolingDown, info, nil
	}
	return harvestInfraRetry, info, nil
}

// harvestInfraMarker is the dedupe marker for the retries-exhausted comment.
func harvestInfraMarker(digest string) string {
	if len(digest) > 12 {
		digest = digest[:12]
	}
	return "<!-- clawflow:acceptance-infra=" + digest + " -->"
}

func harvestInfraComment(repo string, pr int, info harvestInfraInfo) string {
	return fmt.Sprintf("🤖 Independent acceptance for PR #%d could not run after %d attempts (setup or provider errors, no reviewer verdict). Automatic retries stopped. Run `clawflow acceptance run --repo %s --pr %d` after changing the issue body or updating ClawFlow, or push a new commit.\n\n%s",
		pr, info.attempts, repo, pr, harvestInfraMarker(info.digest))
}

// harvestMarker is the dedupe marker for a contract's non-pass comment.
func harvestMarker(r acceptance.Record) string {
	d := r.ContractDigest
	if len(d) > 12 {
		d = d[:12]
	}
	return "<!-- clawflow:acceptance=" + d + " -->"
}

// harvestNonpassComment renders nonpassing verdicts and passing advisories. The reviewer summary is model output, so it is bounded and stripped
// of HTML comment delimiters to keep it from forging markers.
func harvestNonpassComment(r acceptance.Record) string {
	summary := strings.NewReplacer("<!--", "", "-->", "").Replace(strings.TrimSpace(r.Report))
	if runes := []rune(summary); len(runes) > harvestSummaryLimit {
		summary = string(runes[:harvestSummaryLimit]) + "…"
	}
	for i, f := range r.Findings {
		if i >= acceptanceFindingEvidenceLimit {
			break
		}
		summary += "\n\n" + sanitizeReviewerText(f.Kind+": "+f.Summary+" "+f.Requirement+" "+f.Defect+" "+string(f.Evidence), acceptanceFindingEntryLimit)
	}
	next := ""
	if r.Verdict == acceptance.Blocked {
		next = "\n\nA human decision is needed; automatic repair does not run for a blocked verdict."
	}
	return fmt.Sprintf("🤖 Independent acceptance for PR #%d: %s.\n\n%s%s\n\n%s",
		r.Contract.PRNumber, r.Verdict, summary, next, harvestMarker(r))
}

// The retry/comment identity deliberately excludes moving base and policy state.
func acceptanceInfraKey(c acceptance.Contract) string {
	return acceptanceHash([]string{c.Repository, fmt.Sprint(c.PRNumber), c.HeadSHA, c.Requirements, acceptance.BinaryID()})
}
