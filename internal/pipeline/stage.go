package pipeline

import (
	"fmt"
	"strings"
	"time"
)

// Evidence is everything Derive needs about one item, as plain data so the
// stage logic is testable from fixtures with no network or filesystem.
type Evidence struct {
	IssueState string // "open" | "closed"
	Labels     []string

	Run *RunInfo
	PR  *PRInfo

	AcceptanceRequired bool
	Acceptance         *AcceptanceInfo

	AutoMerge  bool
	CIRequired bool

	// Decision is the latest queue decision about this very issue, if any.
	Decision *QueueDecision
	InQueue  bool

	// Now is the evaluation time; a zero value treats retry times as future.
	Now time.Time
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// Derive maps the evidence of one item to its delivery stage, a short human
// "why" and the evidence lines behind the verdict. The order of checks is the
// order of the pipeline read backwards: a merged PR wins over everything, a
// running reviewer wins over CI state, and so on down to "queued".
func Derive(e Evidence) (Stage, string, []string) {
	var ev []string
	add := func(format string, args ...any) { ev = append(ev, fmt.Sprintf(format, args...)) }

	if e.Run != nil {
		add("latest %s run: %s", e.Run.Operator, e.Run.Status)
	}
	if e.PR != nil {
		add("PR #%d is %s", e.PR.Number, e.PR.State)
	}

	if pr := e.PR; pr != nil {
		switch pr.State {
		case "merged":
			return StageMerged, fmt.Sprintf("PR #%d merged", pr.Number), ev
		case "open":
			return derivePROpen(e, ev)
		case "closed":
			return StageBlocked, fmt.Sprintf("PR #%d was closed without merging", pr.Number), ev
		}
	}

	// No PR: the issue is queued, being implemented, or the attempt ended.
	if e.Run != nil && e.Run.Live {
		why := fmt.Sprintf("%s is running", e.Run.Operator)
		if e.Run.Stage != "" {
			why += " (" + e.Run.Stage + ")"
		}
		return StageImplementing, why, ev
	}
	if e.IssueState == "closed" {
		return StageBlocked, "issue is closed and no merged PR was found", ev
	}
	if e.Run != nil {
		switch e.Run.Status {
		case "running", "finalizing":
			return StageFailed, fmt.Sprintf("%s run was recorded as %s but its runner is gone", e.Run.Operator, e.Run.Status), ev
		case "rate-limited":
			return StageBlocked, "provider rate limit hit; waiting for it to clear", ev
		case "cost-limit":
			return StageBlocked, "provider cost cap reached; recovers with the next billing window", ev
		case "auth-error":
			return StageBlocked, "provider authentication failed; fix the Offload profile", ev
		case "failed", "no-marker", "disallowed-outcome", "output-limit", "skipped-empty", "cancelled":
			return StageFailed, fmt.Sprintf("last %s run ended as %s", e.Run.Operator, e.Run.Status), ev
		}
	}
	if hasLabel(e.Labels, "agent-failed") {
		add("label agent-failed")
		return StageFailed, "the issue carries agent-failed", ev
	}
	if hasLabel(e.Labels, "agent-skipped") {
		add("label agent-skipped")
		return StageBlocked, "agent-skipped: the implementer needs more information", ev
	}
	if hasLabel(e.Labels, "agent-implemented") {
		add("label agent-implemented")
		return StageBlocked, "marked implemented but no pull request was found", ev
	}
	if d := e.Decision; d != nil {
		add("queue %s: %s", d.Action, d.Reason)
		switch d.Action {
		case "blocked":
			return StageBlocked, "queue blocked: " + d.Reason, ev
		case "pick":
			return StageQueued, "picked by the queue; waiting for the next run", ev
		default:
			return StageQueued, "waiting in the queue: " + d.Reason, ev
		}
	}
	if e.InQueue {
		return StageQueued, "waiting in the queue", ev
	}
	return StageQueued, "waiting for the next run", ev
}

func derivePROpen(e Evidence, ev []string) (Stage, string, []string) {
	pr := e.PR
	a := e.Acceptance
	add := func(format string, args ...any) { ev = append(ev, fmt.Sprintf(format, args...)) }

	if pr.Draft {
		return StageBlocked, fmt.Sprintf("PR #%d is a draft", pr.Number), ev
	}
	if a != nil && a.Running {
		add("acceptance review in progress")
		return StageAcceptanceRunning, "independent acceptance is reviewing the PR", ev
	}

	if pr.CI != "" {
		add("ci=%s", pr.CI)
	}
	switch pr.CI {
	case "failure":
		why := "CI failed on the current head"
		if e.Run != nil && e.Run.Operator == "repair-ci" {
			if e.Run.Live {
				why += "; repair-ci is running"
			} else {
				why += fmt.Sprintf("; last repair-ci run: %s", e.Run.Status)
			}
		}
		return StageCIFailed, why, ev
	case "pending":
		return StageCIPending, "CI is still running on the current head", ev
	case "no-checks":
		if e.CIRequired {
			return StageCIPending, "CI is required but no checks are reported yet", ev
		}
	case "unknown":
		if e.CIRequired {
			why := "CI status could not be read"
			if pr.CIError != "" {
				why += ": " + pr.CIError
			}
			return StageBlocked, why, ev
		}
	}

	if pr.Mergeable == "conflict" {
		add("mergeable=conflict")
		return StageBlocked, "PR has merge conflicts with the base branch", ev
	}

	if e.AcceptanceRequired {
		if a == nil {
			return StageAcceptancePending, "waiting for independent acceptance to start", ev
		}
		if a.Error != "" {
			add("acceptance: %s", a.Error)
			return StageBlocked, "acceptance contract unavailable: " + a.Error, ev
		}
		add("acceptance attempts=%d infrastructure=%d", a.Attempts, a.InfraAttempts)
		switch {
		case a.Attempts == 0:
			return StageAcceptancePending, "waiting for independent acceptance to start", ev
		case a.LatestIsInfra:
			if a.MaxInfraRetries > 0 && a.InfraAttempts >= a.MaxInfraRetries {
				return StageBlocked, fmt.Sprintf("acceptance could not run after %d infrastructure attempts; retries stopped", a.InfraAttempts), ev
			}
			why := fmt.Sprintf("acceptance hit an infrastructure error (attempt %d)", a.InfraAttempts)
			switch retry := ParseTime(a.NextRetryAt); {
			case retry.IsZero():
				why += "; rerun with clawflow acceptance run"
			case !e.Now.IsZero() && !retry.After(e.Now):
				why += "; retry is due on the next run"
			default:
				why += "; next retry at " + a.NextRetryAt
			}
			return StageInfraRetry, why, ev
		case a.Verdict == "passed" && a.Valid:
			add("acceptance verdict=passed")
			if e.AutoMerge {
				return StageAccepted, "accepted; waiting for auto-merge", ev
			}
			return StageAccepted, "accepted; waiting for a manual merge", ev
		case a.Verdict == "passed":
			return StageAcceptancePending, "the last passing record no longer matches the current head or requirements", ev
		case a.Verdict == "failed" || a.Verdict == "blocked":
			add("acceptance verdict=%s", a.Verdict)
			return StageRejected, "acceptance " + a.Verdict + "; a new commit or issue update is needed", ev
		}
		return StageAcceptancePending, "waiting for independent acceptance", ev
	}

	return StagePROpen, "PR is open", ev
}

// ParseTime parses an RFC3339 timestamp, returning the zero time on failure.
func ParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}

// Latest returns the most recent of the given RFC3339 timestamps formatted in
// UTC, or "" when none parses.
func Latest(stamps ...string) string {
	var best time.Time
	for _, s := range stamps {
		if t := ParseTime(s); t.After(best) {
			best = t
		}
	}
	if best.IsZero() {
		return ""
	}
	return best.UTC().Format(time.RFC3339)
}
