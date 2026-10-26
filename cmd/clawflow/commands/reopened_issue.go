package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type reopenContext struct {
	Event   vcs.IssueEvent
	History *vcs.IssueHistory
}

func (r *reopenContext) token() string {
	sum := sha256.Sum256([]byte(r.Event.ID))
	return hex.EncodeToString(sum[:12])
}
func (r *reopenContext) branch(number int) string {
	return fmt.Sprintf("fix/issue-%d-reopen-%s", number, r.token())
}
func (r *reopenContext) prompt(number int) string {
	return fmt.Sprintf("\n## Reopened issue cycle\nReopen event: %s at %s. Address the latest follow-up; do not reimplement resolved work.\nRequired implementation branch: `%s` (use this instead of the default branch in the workflow). Keep all previous-cycle branches unchanged.\n%s", r.Event.ID, r.Event.CreatedAt, r.branch(number), r.History.Text())
}

func implementationSubjectAllowed(sub *operator.Subject) bool {
	if sub.IsPR || sub.State != "open" || sub.SubTotal > 0 {
		return false
	}
	for _, label := range []string{"question", "tracking", "agent-running", "agent-failed", "agent-skipped"} {
		if sub.HasLabel(label) {
			return false
		}
	}
	return true
}

func withoutImplemented(sub *operator.Subject) *operator.Subject {
	copy := *sub
	copy.Labels = nil
	for _, l := range sub.Labels {
		if l != "agent-implemented" {
			copy.Labels = append(copy.Labels, l)
		}
	}
	return &copy
}

// discoverReopen never changes remote labels or the registry's shared operator.
// A missing capability is acceptable for an ordinary, never-delivered issue.
func discoverReopen(client vcs.Client, repo string, sub *operator.Subject) (*reopenContext, error) {
	lister, ok := client.(vcs.IssueEventLister)
	if !ok {
		if sub.HasLabel("agent-implemented") {
			return nil, fmt.Errorf("reopen history: %w", vcs.ErrNotSupported)
		}
		return nil, nil
	}
	events, err := lister.ListIssueEvents(repo, sub.Number)
	if err != nil {
		return nil, fmt.Errorf("reopen history unavailable: %w", err)
	}
	event, cycleErr := vcs.ReopenCycle(events)
	if cycleErr != nil {
		if sub.HasLabel("agent-implemented") {
			return nil, cycleErr
		}
		// If there has been a reopen since implementation but the evidence is
		// ambiguous, do not silently treat a retry as first-time work.
		var implemented, reopened bool
		for _, e := range events {
			implemented = implemented || (e.Type == "labeled" && e.Label == "agent-implemented")
			reopened = reopened || e.Type == "reopened"
		}
		if implemented && reopened {
			return nil, cycleErr
		}
		return nil, nil
	}
	h, err := vcs.LoadIssueHistory(client, repo, sub.Number)
	if err != nil {
		return nil, err
	}
	verified, err := vcs.ReopenCycle(h.Events)
	if err != nil || verified.ID != event.ID {
		return nil, fmt.Errorf("reopen cycle changed during history read")
	}
	for _, pr := range h.PRs {
		if pr.State == "open" || pr.State == "opened" {
			return nil, fmt.Errorf("linked PR/MR #%d is still open", pr.Number)
		}
	}
	return &reopenContext{Event: event, History: h}, nil
}

// prepareImplementation is called only after the cross-process lock is held.
// It prepares complete context before any stale implementation label removal.
func prepareImplementation(j *runJob) error {
	issue, err := j.client.GetIssue(j.repo, j.sub.Number)
	if err != nil {
		return err
	}
	if issue == nil {
		return fmt.Errorf("issue no longer exists")
	}
	fresh := &operator.Subject{Number: issue.Number, Title: issue.Title, Body: issue.Body, Labels: issue.Labels, State: issue.State, SubTotal: issue.SubIssuesTotal, URL: issue.URL}
	if !implementationSubjectAllowed(fresh) {
		return fmt.Errorf("issue is no longer an eligible open leaf")
	}
	r, err := discoverReopen(j.client, j.repo, fresh)
	if err != nil {
		return err
	}
	if j.reopen != nil && (r == nil || r.Event.ID != j.reopen.Event.ID) {
		return fmt.Errorf("reopen cycle changed since scan")
	}
	prs, err := j.client.ListOpenPRs(j.repo)
	if err != nil {
		return fmt.Errorf("open PR guard: %w", err)
	}
	for _, pr := range prs {
		if vcs.PRReferencesIssue(pr, issue.Number) {
			return fmt.Errorf("linked PR/MR #%d is still open", pr.Number)
		}
	}
	if r != nil {
		if r.History.Issue == nil || r.History.Issue.State != "open" {
			return fmt.Errorf("issue closed during history read")
		}
		latest := r.History.Issue
		fresh.Title, fresh.Body, fresh.Labels, fresh.SubTotal = latest.Title, latest.Body, latest.Labels, latest.SubIssuesTotal
		if !implementationSubjectAllowed(fresh) {
			return fmt.Errorf("issue changed during history read")
		}
		j.comments = r.History.Comments
	} else {
		j.comments, err = j.client.ListIssueCommentsDetail(j.repo, issue.Number)
		if err != nil {
			return fmt.Errorf("implementation comments: %w", err)
		}
	}
	matchSub := fresh
	if r != nil {
		matchSub = withoutImplemented(fresh)
	}
	if op := effectiveOperator(j.op, matchSub); op == nil {
		return fmt.Errorf("implementation no longer eligible")
	} else if ok, reason := operator.MatchesWithReason(matchSub, op); !ok {
		return fmt.Errorf("implementation excluded: %s", reason)
	}
	j.sub, j.reopen = fresh, r
	return nil
}

func formattedComments(comments []vcs.IssueComment) []string {
	out := make([]string, len(comments))
	for i, c := range comments {
		out[i] = fmt.Sprintf("ID: %d; author: %s; created: %s\n%s", c.ID, c.Author, c.CreatedAt, c.Body)
	}
	return out
}

func removeStaleImplementation(j *runJob) error {
	if j.reopen == nil || !j.sub.HasLabel("agent-implemented") {
		return nil
	}
	if err := j.client.RemoveLabel(j.repo, j.sub.Number, "agent-implemented"); err != nil {
		return fmt.Errorf("remove stale implementation label: %w", err)
	}
	j.sub = withoutImplemented(j.sub)
	return nil
}

// Keep cycle paths inside a separate parent; ordinary worktree discovery must
// never see prior reopened-cycle worktrees as resumable ordinary work.
func cycleWorktreeRepo(repo string, r *reopenContext) string {
	return strings.TrimSuffix(repo, "/") + "/reopen-" + r.token()
}
