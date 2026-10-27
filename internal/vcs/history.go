package vcs

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// IssueEventLister is optional so custom clients can explicitly lack history.
type IssueEventLister interface {
	ListIssueEvents(repo string, number int) ([]IssueEvent, error)
}

// IssueEvent normalizes lifecycle and label events without provider transcripts.
type IssueEvent struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
	Actor     string `json:"actor,omitempty"`
	Label     string `json:"label,omitempty"`
}

type IssueHistory struct {
	Issue    *Issue         `json:"issue"`
	Comments []IssueComment `json:"comments"`
	Events   []IssueEvent   `json:"events"`
	PRs      []PR           `json:"pull_requests"`
	Complete bool           `json:"complete"`
	Errors   []string       `json:"errors,omitempty"`
}

var closingReferences = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s+#(\d+)\b`)
var cycleSuffix = regexp.MustCompile(`^[a-f0-9]{24}$`)

// ManagedIssueBranch accepts only our exact ordinary or cycle branch names.
func ManagedIssueBranch(branch string, number int) bool {
	for _, kind := range []string{"fix", "feat"} {
		base := fmt.Sprintf("%s/issue-%d", kind, number)
		if branch == base {
			return true
		}
		if kind == "fix" && strings.HasPrefix(branch, base+"-reopen-") && cycleSuffix.MatchString(strings.TrimPrefix(branch, base+"-reopen-")) {
			return true
		}
	}
	return false
}

func PRReferencesIssue(pr PR, number int) bool {
	if ManagedIssueBranch(pr.HeadBranch, number) {
		return true
	}
	for _, m := range closingReferences.FindAllStringSubmatch(pr.Body, -1) {
		n, _ := strconv.Atoi(m[1])
		if n == number {
			return true
		}
	}
	return false
}

// LoadIssueHistory never represents an unavailable section as complete history.
// The partial result is useful to CLI readers; execution must require nil error.
func LoadIssueHistory(client Client, repo string, number int) (*IssueHistory, error) {
	h := &IssueHistory{Comments: []IssueComment{}, Events: []IssueEvent{}, PRs: []PR{}}
	record := func(section string, err error) {
		if err != nil {
			h.Errors = append(h.Errors, section+": "+err.Error())
		}
	}
	var err error
	h.Issue, err = client.GetIssue(repo, number)
	record("issue", err)
	if err == nil && h.Issue == nil {
		record("issue", fmt.Errorf("#%d not found", number))
	}
	h.Comments, err = client.ListIssueCommentsDetail(repo, number)
	record("comments", err)
	if lister, ok := client.(IssueEventLister); ok {
		h.Events, err = lister.ListIssueEvents(repo, number)
		record("events", err)
	} else {
		record("events", ErrNotSupported)
	}
	prs, err := client.ListPRs(repo, "all")
	record("pull requests", err)
	for _, pr := range prs {
		if PRReferencesIssue(pr, number) {
			h.PRs = append(h.PRs, pr)
		}
	}
	sort.SliceStable(h.Comments, func(i, j int) bool { return h.Comments[i].CreatedAt < h.Comments[j].CreatedAt })
	sort.SliceStable(h.Events, func(i, j int) bool { return h.Events[i].CreatedAt < h.Events[j].CreatedAt })
	h.Complete = len(h.Errors) == 0
	if !h.Complete {
		return h, fmt.Errorf("incomplete issue history: %s", strings.Join(h.Errors, "; "))
	}
	return h, nil
}

// ErrNoUnconsumedReopen reports the ordinary state in which an implemented
// issue has no pending reopen (never reopened, or the reopen was already
// consumed by a later implementation). It is not a history failure.
var ErrNoUnconsumedReopen = errors.New("no unconsumed reopen after implementation and close")

// ReopenCycle requires strict timestamp ordering; ties deliberately fail closed.
// The preceding label remains discoverable after its removal for cycle retries.
func ReopenCycle(events []IssueEvent) (IssueEvent, error) {
	var reopen, implemented, closed IssueEvent
	var rt, it, ct time.Time
	for _, e := range events {
		if e.Type != "reopened" && e.Type != "closed" && !(e.Type == "labeled" && e.Label == "agent-implemented") {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, e.CreatedAt)
		if err != nil || e.ID == "" {
			return IssueEvent{}, fmt.Errorf("invalid lifecycle event identity/time")
		}
		switch {
		case e.Type == "reopened":
			if t.Equal(rt) && reopen.ID != e.ID {
				return IssueEvent{}, fmt.Errorf("ambiguous reopen event timestamp")
			}
			if !t.Before(rt) {
				rt, reopen = t, e
			}
		case e.Type == "closed":
			if !t.Before(ct) {
				ct, closed = t, e
			}
		default:
			if !t.Before(it) {
				it, implemented = t, e
			}
		}
	}
	if reopen.ID == "" || implemented.ID == "" || closed.ID == "" || rt.Before(it) || rt.Before(ct) {
		return IssueEvent{}, ErrNoUnconsumedReopen
	}
	if !rt.After(it) || !rt.After(ct) {
		return IssueEvent{}, fmt.Errorf("ambiguous lifecycle ordering: reopen timestamp ties implementation or close")
	}
	return reopen, nil
}

// Text is the shared human/AI representation. Bodies are never truncated.
func (h *IssueHistory) Text() string {
	var b strings.Builder
	b.WriteString("Issue history: discussion, lifecycle and linked delivery summaries (not PR review threads).\n")
	if h.Issue != nil {
		fmt.Fprintf(&b, "#%d %s [%s]\n%s\n\n", h.Issue.Number, h.Issue.Title, h.Issue.State, h.Issue.Body)
	}
	for _, c := range h.Comments {
		fmt.Fprintf(&b, "Comment %d by %s at %s:\n%s\n\n", c.ID, c.Author, c.CreatedAt, c.Body)
	}
	for _, e := range h.Events {
		fmt.Fprintf(&b, "Event %s: %s %s by %s at %s\n", e.ID, e.Type, e.Label, e.Actor, e.CreatedAt)
	}
	for _, pr := range h.PRs {
		fmt.Fprintf(&b, "\nPR/MR #%d %s [%s] %s\nBranch: %s; merged: %s; closed: %s\n%s\n", pr.Number, pr.Title, pr.State, pr.URL, pr.HeadBranch, pr.MergedAt, pr.ClosedAt, pr.Body)
	}
	for _, e := range h.Errors {
		fmt.Fprintf(&b, "INCOMPLETE: %s\n", e)
	}
	return b.String()
}
