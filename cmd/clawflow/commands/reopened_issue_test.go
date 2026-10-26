package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func reopenEvents() []vcs.IssueEvent {
	return []vcs.IssueEvent{
		{ID: "github-1", Type: "labeled", Label: "agent-implemented", CreatedAt: "2026-01-01T00:00:00Z"},
		{ID: "github-2", Type: "closed", CreatedAt: "2026-01-02T00:00:00Z"},
		{ID: "github-3", Type: "reopened", CreatedAt: "2026-01-03T00:00:00Z", Actor: "alice"},
	}
}

type reopenFake struct {
	vcs.Client
	issue                   vcs.Issue
	events                  []vcs.IssueEvent
	open                    []vcs.PR
	eventErr, errorComments error
	noPreviousPR            bool
	removed                 []string
	issueCalls              int
	closeOnHistory          bool
}

func (f *reopenFake) GetIssue(string, int) (*vcs.Issue, error) {
	f.issueCalls++
	i := f.issue
	if f.closeOnHistory && f.issueCalls > 1 {
		i.State = "closed"
	}
	return &i, nil
}
func (f *reopenFake) ListIssueEvents(string, int) ([]vcs.IssueEvent, error) {
	return f.events, f.eventErr
}
func (f *reopenFake) ListOpenPRs(string) ([]vcs.PR, error) { return f.open, nil }
func (f *reopenFake) ListPRs(string, string) ([]vcs.PR, error) {
	if f.noPreviousPR {
		return nil, nil
	}
	return []vcs.PR{{Number: 10, Body: "Fixes #9\nPrevious implementation", State: "merged", URL: "https://example.test/pr/10"}}, nil
}
func (f *reopenFake) ListIssueCommentsDetail(string, int) ([]vcs.IssueComment, error) {
	return []vcs.IssueComment{{ID: 42, Author: "alice", CreatedAt: "2026-01-04T00:00:00Z", Body: strings.Repeat("latest follow-up ", 40)}}, f.errorComments
}
func (f *reopenFake) RemoveLabel(_ string, _ int, labels ...string) error {
	f.removed = append(f.removed, labels...)
	return nil
}

func newReopenFake() *reopenFake {
	return &reopenFake{issue: vcs.Issue{Number: 9, State: "open", Body: "current issue", Labels: []string{"agent-implemented"}}, events: reopenEvents()}
}
func reopenJob(t *testing.T, f *reopenFake) *runJob {
	return &runJob{repo: "o/r", sub: &operator.Subject{Number: 9, State: "open", Labels: []string{"agent-implemented"}}, op: implementationForTest(t), client: f}
}

func TestReopenPreparation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*reopenFake)
		want   bool
	}{
		{"eligible", func(*reopenFake) {}, true},
		{"no previous PR", func(f *reopenFake) { f.noPreviousPR = true }, true},
		{"retry after label removal", func(f *reopenFake) { f.issue.Labels = nil }, true},
		{"awaiting CI", func(f *reopenFake) { f.events = f.events[:1] }, false},
		{"consumed", func(f *reopenFake) {
			f.events = append(f.events, vcs.IssueEvent{ID: "github-4", Type: "labeled", Label: "agent-implemented", CreatedAt: "2026-01-05T00:00:00Z"})
		}, false},
		{"unsupported", func(f *reopenFake) { f.eventErr = vcs.ErrNotSupported }, false},
		{"comments failed", func(f *reopenFake) { f.errorComments = errors.New("page two") }, false},
		{"open PR after scan", func(f *reopenFake) { f.open = []vcs.PR{{Number: 11, State: "open", Body: "Fixes #9"}} }, false},
		{"closed midqueue", func(f *reopenFake) { f.issue.State = "closed" }, false},
		{"closed during history", func(f *reopenFake) { f.closeOnHistory = true }, false},
		{"became parent", func(f *reopenFake) { f.issue.SubIssuesTotal = 1 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReopenFake()
			tc.change(f)
			j := reopenJob(t, f)
			err := prepareImplementation(j)
			if (err == nil) != tc.want {
				t.Fatalf("prepare=%v", err)
			}
			if len(f.removed) != 0 {
				t.Fatal("preparation mutated labels")
			}
			if tc.want {
				if j.reopen == nil || (!f.noPreviousPR && !strings.Contains(j.reopen.prompt(9), "Previous implementation")) {
					t.Fatal("lost cycle/history")
				}
				if err := removeStaleImplementation(j); err != nil {
					t.Fatal(err)
				}
				if len(f.issue.Labels) > 0 && (len(f.removed) != 1 || f.removed[0] != "agent-implemented") {
					t.Fatalf("removed %v", f.removed)
				}
			}
		})
	}
}

func TestReopenPreservesTerminalExclusions(t *testing.T) {
	for _, label := range []string{"question", "tracking", "agent-running", "agent-failed", "agent-skipped"} {
		f := newReopenFake()
		f.issue.Labels = append(f.issue.Labels, label)
		if err := prepareImplementation(reopenJob(t, f)); err == nil {
			t.Fatalf("accepted %s", label)
		}
	}
}

func TestReopenScanCopyAndCycleChange(t *testing.T) {
	f := newReopenFake()
	j := reopenJob(t, f)
	copy := withoutImplemented(j.sub)
	if !j.sub.HasLabel("agent-implemented") || copy.HasLabel("agent-implemented") {
		t.Fatal("scan copy mutated original")
	}
	if !operator.Matches(copy, j.op) {
		t.Fatal("verified scan copy did not match")
	}
	j.reopen = &reopenContext{Event: vcs.IssueEvent{ID: "different-cycle"}}
	if err := prepareImplementation(j); err == nil {
		t.Fatal("changed cycle accepted")
	}
}

func TestReopenLockPreventsHistoryAndWrites(t *testing.T) {
	withRunLog(t)
	f := newReopenFake()
	j := reopenJob(t, f)
	if err := snapshot.AcquireLock(j.repo, j.sub.Number, "implement"); err != nil {
		t.Fatal(err)
	}
	defer snapshot.ReleaseLock(j.repo, j.sub.Number)
	if fired, _, failed := runOneOperator(context.Background(), j, time.Second); fired || failed {
		t.Fatal("fired or failed while locked")
	}
	if f.issueCalls != 0 || len(f.removed) > 0 {
		t.Fatal("locked issue was inspected or mutated")
	}
}

func TestReopenOrdinaryDuplicateGuardAndComments(t *testing.T) {
	f := newReopenFake()
	f.issue.Labels = nil
	f.events = nil
	f.open = []vcs.PR{{Number: 11, Body: "Fixes #9"}}
	if err := prepareImplementation(reopenJob(t, f)); err == nil {
		t.Fatal("ordinary duplicate accepted")
	}
	f.open = []vcs.PR{{Number: 11, Body: "Fixes #90", HeadBranch: "fix/issue-90"}}
	j := reopenJob(t, f)
	if err := prepareImplementation(j); err != nil {
		t.Fatal(err)
	}
	if j.reopen != nil {
		t.Fatal("ordinary issue gained cycle")
	}
	text := formattedComments(j.comments)[0]
	for _, part := range []string{"42", "alice", "2026-01-04", strings.Repeat("latest follow-up ", 40)} {
		if !strings.Contains(text, part) {
			t.Errorf("missing %q", part)
		}
	}
	f.errorComments = errors.New("comments unavailable")
	if err := prepareImplementation(reopenJob(t, f)); err == nil {
		t.Fatal("incomplete ordinary context accepted")
	}
}

func TestReopenWorktreeIsolationAndFreshFetch(t *testing.T) {
	local := newIssue329Repo(t)
	t.Setenv("HOME", t.TempDir())
	cfg := config.Repo{LocalPath: local, BaseBranch: "main"}
	r := &reopenContext{Event: reopenEvents()[2]}
	first, cleanup, err := setupReopenWorktree(cfg, "o/r", 9, time.Now(), "", r)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if out, err := exec.Command("git", "-C", first.Path, "checkout", "-b", r.branch(9)).CombinedOutput(); err != nil {
		t.Fatalf("branch: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(first.Path, "partial.txt"), []byte("cycle work"), 0600); err != nil {
		t.Fatal(err)
	}
	same, _, err := setupReopenWorktree(cfg, "o/r", 9, time.Now().Add(time.Second), "", r)
	if err != nil || !same.Resumed || same.Path != first.Path {
		t.Fatalf("same-cycle resume: %+v %v", same, err)
	}
	other := &reopenContext{Event: vcs.IssueEvent{ID: "github-999"}}
	next, nextCleanup, err := setupReopenWorktree(cfg, "o/r", 9, time.Now().Add(2*time.Second), "", other)
	if err != nil {
		t.Fatal(err)
	}
	defer nextCleanup()
	if next.Resumed || next.Path == first.Path {
		t.Fatal("new cycle resumed old work")
	}
	if _, err := os.Stat(filepath.Join(first.Path, "partial.txt")); err != nil {
		t.Fatal("old work lost")
	}
	if out, err := exec.Command("git", "-C", local, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git")).CombinedOutput(); err != nil {
		t.Fatalf("origin: %v %s", err, out)
	}
	if _, _, err := setupReopenWorktree(cfg, "o/r", 9, time.Now(), "", other); err == nil {
		t.Fatal("stale-base reopen permitted")
	}
}

func TestReopenLocalHistoryAndPrompt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	started := time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)
	dir := snapshot.RunDir("o/r", 9, started)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.WriteRunMeta(dir, snapshot.RunMeta{Repo: "o/r", IssueNumber: 9, Operator: "implement", Status: "success", StartedAt: started, PRUrl: "https://example.test/pr/10", Summary: "SECRET RAW OUTPUT", Error: "SECRET ERROR"}); err != nil {
		t.Fatal(err)
	}
	runs, err := loadLocalIssueRuns("o/r", 9)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%v err=%v", runs, err)
	}
	h, err := vcs.LoadIssueHistory(newReopenFake(), "o/r", 9)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(issueHistoryOutput{IssueHistory: h, LocalRuns: runs, LocalScope: "local-only"})
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, part := range []string{"local-only", "success", "2026-01-04", "alice", "Previous implementation"} {
		if !strings.Contains(text, part) {
			t.Errorf("missing %s", part)
		}
	}
	if strings.Contains(text, "SECRET") {
		t.Fatal("leaked raw metadata")
	}
	for _, asJSON := range []bool{false, true} {
		var output bytes.Buffer
		if err := renderIssueHistory(&output, issueHistoryOutput{IssueHistory: h, LocalRuns: runs, LocalScope: "local-only"}, asJSON); err != nil {
			t.Fatal(err)
		}
		for _, part := range []string{"alice", "42", "2026-01-04", strings.Repeat("latest follow-up ", 40), "success", "local-only"} {
			if !strings.Contains(output.String(), part) {
				t.Errorf("render json=%v missing %q", asJSON, part)
			}
		}
	}
	r := &reopenContext{Event: reopenEvents()[2], History: h}
	if !vcs.ManagedIssueBranch(r.branch(9), 9) || !strings.Contains(r.prompt(9), r.branch(9)) {
		t.Fatal("branch/prompt mismatch")
	}
	pr, issue := repairFixture()
	pr.HeadBranch = r.branch(9)
	if err := ciRepairCandidate("o/r", pr, issue); err != nil {
		t.Fatal(err)
	}
	if cmd, _, err := NewIssueCmd().Find([]string{"history", "9"}); err != nil || cmd.Name() != "history" {
		t.Fatal("history command not registered")
	}
}
