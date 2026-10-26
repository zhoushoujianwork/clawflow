package vcs

import (
	"errors"
	"strings"
	"testing"
)

func cycleEvents() []IssueEvent {
	return []IssueEvent{
		{ID: "label", Type: "labeled", Label: "agent-implemented", CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "close", Type: "closed", CreatedAt: "2026-01-02T00:00:00Z"},
		{ID: "reopen", Type: "reopened", CreatedAt: "2026-01-03T00:00:00Z"},
	}
}

func TestHistoryReopenCycle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []IssueEvent
		want   bool
	}{
		{"eligible", cycleEvents(), true},
		{"awaiting CI", cycleEvents()[:1], false},
		{"no close", []IssueEvent{cycleEvents()[0], cycleEvents()[2]}, false},
		{"consumed", append(cycleEvents(), IssueEvent{ID: "new-label", Type: "labeled", Label: "agent-implemented", CreatedAt: "2026-01-04T00:00:00Z"}), false},
		{"tied", []IssueEvent{cycleEvents()[0], cycleEvents()[1], {ID: "reopen", Type: "reopened", CreatedAt: "2026-01-02T00:00:00Z"}}, false},
		{"duplicate reopen time", append(cycleEvents(), IssueEvent{ID: "other-reopen", Type: "reopened", CreatedAt: "2026-01-03T00:00:00Z"}), false},
		{"invalid", append(cycleEvents(), IssueEvent{ID: "bad", Type: "closed", CreatedAt: "bad"}), false},
		{"missing identity", []IssueEvent{{Type: "reopened", CreatedAt: "2026-01-03T00:00:00Z"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := ReopenCycle(tc.events)
			if (err == nil) != tc.want {
				t.Fatalf("event=%+v err=%v", e, err)
			}
		})
	}
}

func TestHistoryExactPRReferences(t *testing.T) {
	for _, tc := range []struct {
		body, branch string
		want         bool
	}{
		{"Fixes #90", "fix/issue-90", false},
		{"Fixes #8; Resolves: #9", "", true},
		{"", "fix/issue-9", true},
		{"", "feat/issue-9", true},
		{"", "fix/issue-9-reopen-" + strings.Repeat("a", 24), true},
		{"", "fix/issue-9-reopen-bogus", false},
		{"Fixes owner/repo#9", "", false},
	} {
		if got := PRReferencesIssue(PR{Body: tc.body, HeadBranch: tc.branch}, 9); got != tc.want {
			t.Errorf("%+v: %v", tc, got)
		}
	}
}

type historyFake struct {
	Client
	err error
}

func (f historyFake) GetIssue(string, int) (*Issue, error) {
	return &Issue{Number: 9, State: "open", Body: "current body"}, nil
}
func (f historyFake) ListIssueCommentsDetail(string, int) ([]IssueComment, error) {
	return []IssueComment{{ID: 123, Author: "alice", CreatedAt: "2026-01-04T00:00:00Z", Body: strings.Repeat("followup", 100)}}, f.err
}
func (f historyFake) ListIssueEvents(string, int) ([]IssueEvent, error) { return cycleEvents(), nil }
func (f historyFake) ListPRs(string, string) ([]PR, error) {
	return []PR{{Number: 10, Body: "Fixes #9\nprior delivery", State: "merged", URL: "https://example.test/pr/10", MergedAt: "2026-01-02T00:00:00Z"}}, nil
}

func TestHistoryCompleteAndPartial(t *testing.T) {
	h, err := LoadIssueHistory(historyFake{}, "o/r", 9)
	if err != nil || !h.Complete {
		t.Fatal(err)
	}
	for _, text := range []string{"current body", "alice", "123", "2026-01-04", strings.Repeat("followup", 100), "prior delivery", "merged", "https://example.test/pr/10"} {
		if !strings.Contains(h.Text(), text) {
			t.Errorf("missing %q", text)
		}
	}
	h, err = LoadIssueHistory(historyFake{err: errors.New("second page failed")}, "o/r", 9)
	if err == nil || h.Complete || !strings.Contains(h.Text(), "INCOMPLETE") {
		t.Fatalf("%+v %v", h, err)
	}
}
