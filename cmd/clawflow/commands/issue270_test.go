package commands

import (
	"errors"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// fakeSubLister is a minimal subIssueLister for exercising isStaleLeafJob
// without standing up a full vcs.Client.
type fakeSubLister struct {
	subs []vcs.Issue
	err  error
}

func (f fakeSubLister) ListSubIssues(repo string, issueNumber int) ([]vcs.Issue, error) {
	return f.subs, f.err
}

func leafOp() *operator.Operator {
	return &operator.Operator{
		Name:    "evaluate-bug",
		Trigger: operator.Trigger{AppliesTo: operator.AppliesLeaf},
	}
}

// TestIsStaleLeafJob covers the issue #270 freshness recheck: a leaf-gated
// operator whose subject grew sub-issues between scan and execution must be
// reported stale (and thus skipped) so the parent defers to track-progress.
func TestIsStaleLeafJob(t *testing.T) {
	cases := []struct {
		name      string
		op        *operator.Operator
		client    fakeSubLister
		wantStale bool
		wantCount int
	}{
		{
			name:      "leaf op, sub-issues attached since poll -> stale",
			op:        leafOp(),
			client:    fakeSubLister{subs: []vcs.Issue{{Number: 140}, {Number: 141}}},
			wantStale: true,
			wantCount: 2,
		},
		{
			name:      "leaf op, still a leaf -> not stale",
			op:        leafOp(),
			client:    fakeSubLister{subs: nil},
			wantStale: false,
			wantCount: 0,
		},
		{
			name:      "non-leaf op (applies_to empty) is never rechecked",
			op:        &operator.Operator{Name: "track-progress", Trigger: operator.Trigger{AppliesTo: operator.AppliesParent}},
			client:    fakeSubLister{subs: []vcs.Issue{{Number: 140}}},
			wantStale: false,
			wantCount: 0,
		},
		{
			name:      "ListSubIssues error (e.g. GitLab ErrNotSupported) -> not stale",
			op:        leafOp(),
			client:    fakeSubLister{err: errors.New("not supported")},
			wantStale: false,
			wantCount: 0,
		},
		{
			name:      "nil op is safely not stale (defensive)",
			op:        nil,
			client:    fakeSubLister{subs: []vcs.Issue{{Number: 140}}},
			wantStale: false,
			wantCount: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stale, n := isStaleLeafJob(tc.client, tc.op, "owner/repo", 139)
			if stale != tc.wantStale || n != tc.wantCount {
				t.Errorf("isStaleLeafJob = (%v, %d), want (%v, %d)", stale, n, tc.wantStale, tc.wantCount)
			}
		})
	}
}

// TestOperatorPriority pins the explicit dispatch order (issue #270):
// decompose must outrank every evaluator, which in turn outrank implement,
// so a tracking issue is split before it is evaluated regardless of how the
// operators happen to sort alphabetically.
func TestOperatorPriority(t *testing.T) {
	if operatorPriority("decompose") >= operatorPriority("evaluate-bug") {
		t.Error("decompose must run before evaluate-bug")
	}
	if operatorPriority("decompose") >= operatorPriority("evaluate-feat") {
		t.Error("decompose must run before evaluate-feat")
	}
	if operatorPriority("evaluate-bug") >= operatorPriority("implement") {
		t.Error("evaluators must run before implement")
	}
	// A leaf issue matching both decompose and evaluate-bug must pick
	// decompose, mirroring the firstMatch selection in scanRepoOnce.
	ops := []string{"evaluate-bug", "decompose"}
	best := ops[0]
	for _, name := range ops[1:] {
		if operatorPriority(name) < operatorPriority(best) {
			best = name
		}
	}
	if best != "decompose" {
		t.Errorf("firstMatch among %v = %q, want decompose", ops, best)
	}
}
