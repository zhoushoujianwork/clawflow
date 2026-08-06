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
	client  vcs.Client
	repo    string
	repoCfg config.Repo
	prNum   int
	// issueNum is the issue the PR closes; comments land there. 0 means
	// "unknown" and comments are posted on the PR itself.
	issueNum int
	prefix   string
	// ciPoll, when true, checks CI exactly once instead of blocking up to
	// repoCfg.CITimeout. The sweep sets it: it runs every pass, so a
	// still-pending check is simply re-examined next time rather than
	// eating the scan budget in 30s sleeps.
	ciPoll bool
	// quiet suppresses the "not mergeable" / "CI pending" comments. The
	// sweep runs every pass, so commenting on a still-dirty PR each time
	// would spam the issue; the inline path comments once and is done.
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

// checkCI resolves the PR's CI state. The inline path blocks up to the
// repo's ci_timeout (a fresh PR's checks have not started yet, and waiting
// is what makes same-run auto-merge work). The sweep sets ciPoll and takes a
// single reading: it will look again next pass, so blocking here would just
// spend scan budget on sleeps.
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

	if a.repoCfg.CIRequired {
		switch status := a.checkCI(); status {
		case vcs.CIStatusSuccess, vcs.CIStatusNone:
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
		func() error { return a.client.MergePR(a.repo, a.prNum) },
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

// sweepAutoMergePRs converges open PRs on an auto_merge repo that no inline
// auto-merge ever claimed. Before issue #299 auto-merge only fired in the
// instant an `implement` run emitted `agent-implemented`, with the PR number
// parsed out of that run's stdout — so a PR created by an earlier pass (or by
// a run that deliberately declined to open a duplicate PR) could never be
// merged by anything. This makes auto-merge an idempotent state check rather
// than a one-shot side effect.
//
// It is a no-op unless repoCfg.AutoMerge is set: repos without auto-merge
// spend zero API calls here.
func sweepAutoMergePRs(ctx context.Context, client vcs.Client, fullName string, repoCfg config.Repo) {
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
	for _, pr := range prs {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		if candidates >= sweepMaxCandidates || merged >= sweepMaxMergesPerPass {
			break
		}
		if isDraftPR(pr.Title) {
			skipped++
			continue
		}
		// A PR with no closing keyword isn't ClawFlow's to merge: it may be
		// a human's in-flight work. The `Closes #N` link is the signal that
		// an operator opened it to resolve a tracked issue.
		issueNum := extractClosesIssue(pr.Body)
		if issueNum == 0 {
			skipped++
			continue
		}
		candidates++
		// Silent skip on non-clean: the sweep sees the same dirty PR every
		// pass, so commenting here would spam the issue forever.
		status, mErr := client.GetPRMergeability(fullName, pr.Number)
		if mErr != nil {
			fmt.Fprintf(os.Stderr, "%s ⚠ auto-merge sweep: mergeability of PR #%d: %v\n", prefix, pr.Number, mErr)
			skipped++
			continue
		}
		if status != vcs.MergeStatusClean {
			debugf("%s · auto-merge sweep: PR #%d not clean (%s), leaving it", prefix, pr.Number, status)
			skipped++
			continue
		}
		if snapshot.IsLocked(fullName, issueNum) {
			debugf("%s · auto-merge sweep: issue #%d locked by another process, skipping PR #%d", prefix, issueNum, pr.Number)
			skipped++
			continue
		}
		if attemptAutoMerge(autoMergeAttempt{
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
		}
	}

	runLog.Info("run/automerge_sweep", "repo", fullName, "open_prs", len(prs), "candidates", candidates, "merged", merged, "skipped", skipped)
	if merged > 0 {
		fmt.Fprintf(os.Stderr, "%s ✓ auto-merge sweep: merged %d PR(s)\n", prefix, merged)
	}
}
