package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

// becameParentFake is a minimal vcs.Client for exercising runOneOperator's
// became-parent branch (issue #335): it returns fresh labels matching the
// job's trigger (so the post-poll re-match passes) and reports sub-issues on
// ListSubIssues (so isStaleLeafJob reports stale). Every method that branch
// must NOT reach — most importantly AddLabel — panics if called, so an
// accidental terminal-label write fails the test loudly instead of silently
// passing.
type becameParentFake struct {
	vcs.Client    // embeds a nil interface; any unimplemented method panics on call
	labels        []string
	subs          []vcs.Issue
	addLabelCalls []string
}

func (f *becameParentFake) GetIssueLabels(repo string, issueNumber int) ([]string, error) {
	return f.labels, nil
}

func (f *becameParentFake) ListSubIssues(repo string, issueNumber int) ([]vcs.Issue, error) {
	return f.subs, nil
}

func (f *becameParentFake) AddLabel(repo string, issueNumber int, labels ...string) error {
	f.addLabelCalls = append(f.addLabelCalls, labels...)
	return nil
}

// TestRunOneOperator_BecameParent_NoTerminalLabel is the issue #335
// regression test: a leaf operator that discovers its subject grew
// sub-issues between poll and execution must skip via isStaleLeafJob
// without writing agent-skipped (or any other label). Marking a
// structurally-skipped leaf job as agent-skipped used to make
// track-progress treat "became a parent mid-flight" as "requirement done",
// which is wrong while the issue is still open and its children haven't
// shipped.
func TestRunOneOperator_BecameParent_NoTerminalLabel(t *testing.T) {
	readLog := withRunLog(t) // sets HOME so snapshot.AcquireLock lands in a temp dir

	op := &operator.Operator{
		Name:    "evaluate-bug",
		Trigger: operator.Trigger{Target: "issue", LabelsRequired: []string{"bug"}, AppliesTo: operator.AppliesLeaf},
	}
	sub := &operator.Subject{Number: 42, Title: "some bug", Labels: []string{"bug"}, State: "open"}
	client := &becameParentFake{
		labels: []string{"bug"},
		subs:   []vcs.Issue{{Number: 43}, {Number: 44}},
	}
	job := &runJob{op: op, sub: sub, repo: "owner/repo", client: client}

	didFire, hitRateLimit := runOneOperator(context.Background(), job, 5*time.Second)
	if didFire || hitRateLimit {
		t.Errorf("runOneOperator() = (%v, %v), want (false, false)", didFire, hitRateLimit)
	}
	if len(client.addLabelCalls) != 0 {
		t.Errorf("became-parent skip must not write any label, got AddLabel(%v)", client.addLabelCalls)
	}
	log := readLog()
	if !strings.Contains(log, "run/skip_became_parent") {
		t.Errorf("expected run/skip_became_parent in run.log, got:\n%s", log)
	}
}
