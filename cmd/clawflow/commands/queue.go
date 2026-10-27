package commands

import (
	"errors"
	"fmt"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// Queue actions reported by decideQueue.
const (
	queueActionPick    = "pick"
	queueActionWait    = "wait"
	queueActionBlocked = "blocked"
)

// queueInput is everything decideQueue needs, expressed as plain data and
// callbacks so the decision is unit-testable with fakes.
type queueInput struct {
	Items []vcs.Issue // direct sub-issues of the queue issue, in provider order
	// Locked reports whether the item holds a live local execution lock.
	Locked func(number int) bool
	// OpenPRs lists open PRs. It is only called when an open item carries
	// agent-implemented, so repos without delivered work cost no extra request.
	OpenPRs func() ([]vcs.PR, error)
	// Eligible reports whether implement would be dispatched for the item right
	// now. A non-empty reason explains why not.
	Eligible func(number int) (ok bool, reason string)
}

// queueDecision is the outcome of one queue evaluation.
type queueDecision struct {
	Action string // queueActionPick | queueActionWait | queueActionBlocked
	Issue  int    // the item the decision is about; 0 when the queue is empty
	Reason string
}

// decideQueue picks the single queue item that may be dispatched for
// implement. At most one item is in flight: while any open item is locked or
// delivered (agent-implemented with an open PR closing it), nothing else is
// dispatched. Otherwise only the head (first open item) is considered; if it
// is not eligible the queue is blocked rather than skipping ahead, so the
// maintainer's ordering stays authoritative.
//
// It fails closed: an unreadable PR list yields a blocked decision.
func decideQueue(in queueInput) queueDecision {
	var open []vcs.Issue
	for _, it := range in.Items {
		if it.State == "open" {
			open = append(open, it)
		}
	}
	if len(open) == 0 {
		return queueDecision{Action: queueActionWait, Reason: "queue_empty"}
	}

	var prs []vcs.PR
	var prsLoaded bool
	for _, it := range open {
		if in.Locked != nil && in.Locked(it.Number) {
			return queueDecision{Action: queueActionWait, Issue: it.Number, Reason: "in_flight_locked"}
		}
		if !hasLabel(it.Labels, "agent-implemented") {
			continue
		}
		if !prsLoaded {
			var err error
			prs, err = in.OpenPRs()
			if err != nil {
				return queueDecision{Action: queueActionBlocked, Issue: it.Number, Reason: "open_prs_unreadable: " + err.Error()}
			}
			prsLoaded = true
		}
		for _, pr := range prs {
			if vcs.PRReferencesIssue(pr, it.Number) {
				return queueDecision{Action: queueActionWait, Issue: it.Number, Reason: fmt.Sprintf("in_flight_open_pr #%d", pr.Number)}
			}
		}
	}

	head := open[0]
	if ok, reason := in.Eligible(head.Number); !ok {
		return queueDecision{Action: queueActionBlocked, Issue: head.Number, Reason: reason}
	}
	return queueDecision{Action: queueActionPick, Issue: head.Number, Reason: "head_eligible"}
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// queueSubIssueLister is the slice of vcs.Client the queue read needs.
type queueSubIssueLister interface {
	ListSubIssues(repo string, issueNumber int) ([]vcs.Issue, error)
}

// readQueueItems lists the queue issue's direct sub-issues. Any failure,
// including a platform without sub-issues, is returned so the caller fails
// closed.
func readQueueItems(client queueSubIssueLister, repo string, queueIssue int) ([]vcs.Issue, error) {
	items, err := client.ListSubIssues(repo, queueIssue)
	if err != nil {
		if errors.Is(err, vcs.ErrNotSupported) {
			return nil, fmt.Errorf("sub-issues not supported on this platform: %w", err)
		}
		return nil, err
	}
	return items, nil
}

// applyImplementQueue removes every implement job that the repository's work
// queue does not allow this pass and logs the decision. Jobs of other
// operators are returned untouched. It is the single call site of the queue
// policy in the scan. A non-nil itemsErr fails closed: no implement job
// survives.
func applyImplementQueue(repo string, queueIssue int, jobs []*runJob, in queueInput, itemsErr error) []*runJob {
	allow := 0
	if itemsErr != nil {
		runLog.Warn("run/queue_blocked", "repo", repo, "queue", queueIssue, "reason", "sub_issues_unreadable", "err", itemsErr.Error())
	} else {
		d := decideQueue(in)
		switch d.Action {
		case queueActionPick:
			allow = d.Issue
			runLog.Info("run/queue_pick", "repo", repo, "queue", queueIssue, "issue", d.Issue, "reason", d.Reason)
		case queueActionWait:
			runLog.Info("run/queue_wait", "repo", repo, "queue", queueIssue, "issue", d.Issue, "reason", d.Reason)
		default:
			runLog.Info("run/queue_blocked", "repo", repo, "queue", queueIssue, "issue", d.Issue, "reason", d.Reason)
		}
	}
	kept := make([]*runJob, 0, len(jobs))
	for _, j := range jobs {
		if j.op != nil && j.op.Name == "implement" && (j.sub == nil || j.sub.Number != allow) {
			continue
		}
		kept = append(kept, j)
	}
	return kept
}
