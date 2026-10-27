package commands

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// closesIssueRE extracts the issue number a PR body claims to close.
// Matches GitHub/GitLab closing keywords case-insensitively, with or
// without the `owner/repo` prefix omitted (cross-repo refs are ignored on
// purpose — the sweep only converges PRs against their own repo's issue).
var closesIssueRE = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s+#(\d+)\b`)

// extractClosesIssue returns the first issue number referenced by a closing
// keyword in body, or 0 when there is none.
func extractClosesIssue(body string) int {
	m := closesIssueRE.FindStringSubmatch(body)
	if len(m) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// isDraftPR reports whether a PR title marks it as work-in-progress. vcs.PR
// carries no draft flag, so the title convention is all we have.
func isDraftPR(title string) bool {
	t := strings.ToUpper(strings.TrimSpace(title))
	for _, p := range []string{"WIP:", "WIP ", "[WIP]", "DRAFT:", "[DRAFT]"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// autoMergeAttempt bundles everything one auto-merge attempt needs. It is
// shared by two callers: the inline low-latency path in runPostAutomation
// (right after implement produced agent-implemented) and the per-scan sweep
// (sweepAutoMergePRs), so the two can't drift apart. See issue #299.
type autoMergeAttempt struct {
	ctx     context.Context
	client  vcs.Client
	repo    string
	repoCfg config.Repo
	prNum   int
	// issueNum is the issue the PR closes; comments land there. 0 means
	// "unknown" and comments are posted on the PR itself.
	issueNum int
	prefix   string
	// ciPoll, when true, checks CI exactly once instead of blocking up to
	// repoCfg.CITimeout. Runner callers set it: they run every pass, so a
	// still-pending check is simply re-examined next time rather than
	// holding the run lock in 30s sleeps.
	ciPoll bool
	// quiet suppresses the "not mergeable" / "CI pending" comments. Runner
	// callers see the same PR every pass, so commenting each time would spam
	// the issue; CI failures are handled by repair-ci.
	quiet bool
}

// notify posts body on the attempt's issue (or the PR when the issue is
// unknown), skipping the write when an earlier comment already carries the
// same PR marker. Best-effort: comment failures are logged, never fatal.
func (a autoMergeAttempt) notify(body, marker string) {
	if a.issueNum == 0 {
		if err := a.client.PostPRComment(a.repo, a.prNum, body); err != nil {
			fmt.Fprintf(os.Stderr, "%s ⚠ auto-merge comment: %v\n", a.prefix, err)
		}
		return
	}
	if marker != "" {
		if existing, err := a.client.ListIssueComments(a.repo, a.issueNum); err == nil {
			for _, c := range existing {
				if strings.Contains(c, marker) {
					fmt.Fprintf(os.Stderr, "%s · auto-merge: comment %q already present, staying silent\n", a.prefix, marker)
					return
				}
			}
		}
	}
	if err := a.client.PostIssueComment(a.repo, a.issueNum, body); err != nil {
		fmt.Fprintf(os.Stderr, "%s ⚠ auto-merge comment: %v\n", a.prefix, err)
	}
}

// checkCI resolves the PR's CI state. Without ciPoll it blocks up to the
// repo's ci_timeout. Every runner caller (inline, sweep, harvest) sets ciPoll
// and takes a single reading: the next pass looks again, so blocking would
// hold the global run lock on sleeps.
func (a autoMergeAttempt) checkCI() vcs.CIStatus {
	if a.ciPoll {
		status, err := a.client.GetCIStatus(a.repo, a.prNum)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s ⚠ CI status check: %v\n", a.prefix, err)
			return vcs.CIStatusPending
		}
		return status
	}
	timeout := a.repoCfg.CITimeout
	if timeout <= 0 {
		timeout = 600
	}
	return waitForCI(a.client, a.repo, a.prNum, timeout, a.prefix)
}

// attemptAutoMerge runs the full auto-merge sequence for one PR: optional CI
// gate, mergeability check, merge with transient-race retry, then head-branch
// cleanup. Returns true only when the merge landed.
func attemptAutoMerge(a autoMergeAttempt) bool {
	fmt.Fprintf(os.Stderr, "%s → auto-merge: PR #%d\n", a.prefix, a.prNum)
	if a.ctx == nil {
		a.ctx = context.Background()
	}
	if a.ctx.Err() != nil {
		return false
	}
	required, err := acceptanceEnrollment(a.repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s merge blocked: %v\n", a.prefix, err)
		return false
	}
	required = required || a.repoCfg.AcceptanceRequired
	merge := func() error { return a.client.MergePR(a.repo, a.prNum) }
	if required {
		unlock, err := acceptanceLock(a.repo, a.prNum)
		if err != nil {
			return false
		}
		defer unlock()
		accepted, _, err := currentAcceptance(a.ctx, a.client, a.repo, a.prNum)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s acceptance blocked: %v; run clawflow acceptance run --repo %s --pr %d\n", a.prefix, err, a.repo, a.prNum)
			return false
		}
		merge = func() error { return conditionalAcceptedMerge(a.ctx, a.client, a.repo, a.prNum, accepted) }
	}

	if a.repoCfg.CIRequired {
		switch status := a.checkCI(); status {
		case vcs.CIStatusSuccess:
			// proceed to merge
		case vcs.CIStatusFailure:
			fmt.Fprintf(os.Stderr, "%s ✗ CI failed, skipping auto-merge\n", a.prefix)
			if !a.quiet {
				a.notify("🤖 CI failed on PR #"+strconv.Itoa(a.prNum)+". Skipping auto-merge.",
					"CI failed on PR #"+strconv.Itoa(a.prNum))
			}
			return false
		default:
			fmt.Fprintf(os.Stderr, "%s ✗ CI still pending, skipping auto-merge\n", a.prefix)
			if !a.quiet {
				a.notify("🤖 CI did not pass within timeout for PR #"+strconv.Itoa(a.prNum)+". Skipping auto-merge.",
					"CI did not pass within timeout for PR #"+strconv.Itoa(a.prNum))
			}
			return false
		}
	}

	mergeStatus, err := a.client.GetPRMergeability(a.repo, a.prNum)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s ⚠ auto-merge: mergeability check failed: %v\n", a.prefix, err)
		return false
	}
	if mergeStatus != vcs.MergeStatusClean {
		fmt.Fprintf(os.Stderr, "%s ✗ PR not mergeable (%s)\n", a.prefix, mergeStatus)
		if !a.quiet {
			a.notify("🤖 PR #"+strconv.Itoa(a.prNum)+" is not mergeable (status: "+string(mergeStatus)+"). Skipping auto-merge.",
				"PR #"+strconv.Itoa(a.prNum)+" is not mergeable")
		}
		return false
	}

	if err := mergeWithRetry(a.prefix,
		merge,
		func() (vcs.MergeStatus, error) { return a.client.GetPRMergeability(a.repo, a.prNum) },
		time.Sleep,
	); err != nil {
		// Keep this comment even in quiet mode, but dedup on the PR
		// number so a repeatedly failing sweep doesn't stack identical
		// notices every pass. The marker `🤖 Auto-merge failed for PR #N`
		// is also Pilot Play 4's trigger contract — don't change its shape.
		fmt.Fprintf(os.Stderr, "%s ⚠ auto-merge failed: %v\n", a.prefix, err)
		a.notify("🤖 Auto-merge failed for PR #"+strconv.Itoa(a.prNum)+": "+err.Error(),
			"🤖 Auto-merge failed for PR #"+strconv.Itoa(a.prNum))
		return false
	}
	// No success comment: the PR's merged state is already visible in the
	// GitHub/GitLab UI.
	fmt.Fprintf(os.Stderr, "%s ✓ auto-merged PR #%d\n", a.prefix, a.prNum)

	// Clean up the remote head branch. Non-fatal: the merge already landed
	// and a stale branch is housekeeping, not a failure.
	if pr, err := a.client.GetPR(a.repo, a.prNum); err != nil {
		fmt.Fprintf(os.Stderr, "%s ⚠ branch cleanup: lookup PR failed: %v\n", a.prefix, err)
	} else if head := pr.HeadBranch; head != "" && head != a.repoCfg.BaseBranch {
		if err := a.client.DeleteBranch(a.repo, head); err != nil {
			fmt.Fprintf(os.Stderr, "%s ⚠ branch cleanup: delete %s failed: %v\n", a.prefix, head, err)
		} else {
			fmt.Fprintf(os.Stderr, "%s ✓ deleted branch %s\n", a.prefix, head)
		}
	}
	return true
}

// sweepMaxMergesPerPass caps how many PRs one scan pass may merge per repo.
// One-at-a-time keeps the blast radius small and matches Pilot Play 4's
// "Max ONE per wake" rule; the next pass picks up the rest.
const sweepMaxMergesPerPass = 1

// sweepMaxCandidates caps how many open PRs get a GetPRMergeability call in
// one pass. Mergeability is an N+1 request per candidate, so a repo with a
// large open-PR backlog must not turn the sweep into an API storm.
const sweepMaxCandidates = 10

// prSkip describes why a PR was filtered out of auto-merge convergence. The
// sweep and the acceptance harvest share it so both report the same reasons.
type prSkip struct {
	reason string
	warn   bool
	// fields are the log key/values that follow "repo" and "pr".
	fields []any
}

// log writes the skip under event, keeping the field order the sweep has
// always used: repo, pr, [issue], reason, [detail].
func (s *prSkip) log(event, repo string, pr int) {
	fields := append([]any{"repo", repo, "pr", pr}, s.fields...)
	if s.warn {
		runLog.Warn(event, fields...)
		return
	}
	runLog.Info(event, fields...)
}

// autoMergeCandidate applies the PR filters shared by the sweep and the
// acceptance harvest: not a draft, a closing keyword, mergeability clean and
// the closed issue not locally locked. It returns the issue the PR closes,
// whether the PR counted against the per-pass candidate cap (it passed the
// keyword filter), and a non-nil skip when the PR is filtered out.
//
// Non-clean PRs are skipped silently (no comment): the same dirty PR is seen
// every pass, so commenting would spam the issue forever. The reason still
// lands in run.log (see #334) so patrol can tell a stuck candidate from
// "nothing to do".
func autoMergeCandidate(client vcs.Client, fullName string, pr vcs.PR) (issueNum int, candidate bool, skip *prSkip) {
	if isDraftPR(pr.Title) {
		return 0, false, &prSkip{reason: "draft", fields: []any{"reason", "draft"}}
	}
	// A PR with no closing keyword isn't ClawFlow's to merge: it may be
	// a human's in-flight work. The `Closes #N` link is the signal that
	// an operator opened it to resolve a tracked issue.
	issueNum = extractClosesIssue(pr.Body)
	if issueNum == 0 {
		return 0, false, &prSkip{reason: "no_closing_keyword", fields: []any{"reason", "no_closing_keyword"}}
	}
	status, mErr := client.GetPRMergeability(fullName, pr.Number)
	if mErr != nil {
		fmt.Fprintf(os.Stderr, "[%s] ⚠ auto-merge sweep: mergeability of PR #%d: %v\n", fullName, pr.Number, mErr)
		return issueNum, true, &prSkip{reason: "mergeability_error", warn: true, fields: []any{"issue", issueNum, "reason", "mergeability_error", "err", mErr.Error()}}
	}
	if status != vcs.MergeStatusClean {
		return issueNum, true, &prSkip{reason: "not_clean", warn: true, fields: []any{"issue", issueNum, "reason", "not_clean", "status", string(status)}}
	}
	if snapshot.IsLocked(fullName, issueNum) {
		return issueNum, true, &prSkip{reason: "locked", fields: []any{"issue", issueNum, "reason", "locked"}}
	}
	return issueNum, true, nil
}

// sweepAutoMergePRs converges open PRs on an auto_merge repo that no inline
// auto-merge ever claimed. Before issue #299 auto-merge only fired in the
// instant an `implement` run emitted `agent-implemented`, with the PR number
// parsed out of that run's stdout — so a PR created by an earlier pass (or by
// a run that deliberately declined to open a duplicate PR) could never be
// merged by anything. This makes auto-merge an idempotent state check rather
// than a one-shot side effect.
//
// It is a no-op unless repoCfg.AutoMerge is set: repos without auto-merge
// spend zero API calls here. A non-zero onlyIssue restricts the sweep to PRs
// whose closing keyword names that issue (targeted runs); 0 considers all.
func sweepAutoMergePRs(ctx context.Context, client vcs.Client, fullName string, repoCfg config.Repo, onlyIssue int) {
	if !repoCfg.AutoMerge {
		return
	}
	prefix := fmt.Sprintf("[%s]", fullName)
	prs, err := client.ListOpenPRs(fullName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s ⚠ auto-merge sweep: list open PRs: %v\n", prefix, err)
		runLog.Warn("run/automerge_sweep", "repo", fullName, "err", err.Error())
		return
	}

	var candidates, merged, skipped int
	skipReasons := map[string]int{}
	for _, pr := range prs {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		if candidates >= sweepMaxCandidates || merged >= sweepMaxMergesPerPass {
			break
		}
		if onlyIssue != 0 && extractClosesIssue(pr.Body) != onlyIssue {
			continue
		}
		issueNum, isCandidate, skip := autoMergeCandidate(client, fullName, pr)
		if isCandidate {
			candidates++
		}
		if skip != nil {
			skipped++
			skipReasons[skip.reason]++
			skip.log("run/automerge_skip", fullName, pr.Number)
			continue
		}
		if attemptAutoMerge(autoMergeAttempt{
			ctx:      ctx,
			client:   client,
			repo:     fullName,
			repoCfg:  repoCfg,
			prNum:    pr.Number,
			issueNum: issueNum,
			prefix:   prefix,
			ciPoll:   true,
			quiet:    true,
		}) {
			merged++
		} else {
			skipped++
			skipReasons["merge_failed"]++
			runLog.Warn("run/automerge_skip", "repo", fullName, "pr", pr.Number, "issue", issueNum, "reason", "merge_failed")
		}
	}

	fields := []any{"repo", fullName, "open_prs", len(prs), "candidates", candidates, "merged", merged, "skipped", skipped}
	for _, reason := range []string{"draft", "no_closing_keyword", "mergeability_error", "not_clean", "locked", "merge_failed"} {
		if n := skipReasons[reason]; n > 0 {
			fields = append(fields, "skip_"+reason, n)
		}
	}
	runLog.Info("run/automerge_sweep", fields...)
	if merged > 0 {
		fmt.Fprintf(os.Stderr, "%s ✓ auto-merge sweep: merged %d PR(s)\n", prefix, merged)
	}
}
