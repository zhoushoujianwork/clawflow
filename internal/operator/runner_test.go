package operator

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeVCS records label and comment operations so tests can assert on them
// without spinning up a real GitHub/GitLab client.
type fakeVCS struct {
	labels          map[int][]string // per-issue label list (append on add, remove on remove)
	comments        []postedComment
	errOnAdd        bool
	errOnRemove     bool
	errOnComment    bool
	addLabelCalls   int
	removeLabelCals int
}

type postedComment struct {
	issueNumber int
	body        string
}

func newFakeVCS() *fakeVCS {
	return &fakeVCS{labels: map[int][]string{}}
}

func (f *fakeVCS) AddLabel(repo string, issueNumber int, labels ...string) error {
	f.addLabelCalls++
	if f.errOnAdd {
		return errors.New("fake add-label error")
	}
	f.labels[issueNumber] = append(f.labels[issueNumber], labels...)
	return nil
}

func (f *fakeVCS) RemoveLabel(repo string, issueNumber int, labels ...string) error {
	f.removeLabelCals++
	if f.errOnRemove {
		return errors.New("fake remove-label error")
	}
	current := f.labels[issueNumber]
	for _, rm := range labels {
		for i, l := range current {
			if l == rm {
				current = append(current[:i], current[i+1:]...)
				break
			}
		}
	}
	f.labels[issueNumber] = current
	return nil
}

func (f *fakeVCS) PostIssueComment(repo string, issueNumber int, body string) error {
	if f.errOnComment {
		return errors.New("fake post-comment error")
	}
	f.comments = append(f.comments, postedComment{issueNumber, body})
	return nil
}

func TestRun_HappyPath(t *testing.T) {
	op := &Operator{
		Name:      "test-op",
		LockLabel: "agent-running",
		Prompt:    "do the thing",
	}
	sub := &Subject{Number: 42, Labels: []string{"bug"}}
	v := newFakeVCS()

	err := Run(context.Background(), op, sub, v, RunOptions{
		Repo:    "acme/webapp",
		Workdir: t.TempDir(),
		Timeout: time.Second,
		RunFunc: func(_ context.Context, prompt, _ string, _ time.Duration) (string, error) {
			// Sanity: prompt should carry the operator body and context.
			if !strings.Contains(prompt, "do the thing") {
				t.Errorf("fake claude did not receive op prompt; got: %q", prompt)
			}
			if !strings.Contains(prompt, "Issue Number: #42") {
				t.Errorf("fake claude did not receive issue context; got: %q", prompt)
			}
			return "evaluation posted", nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Lock added once and removed once.
	if v.addLabelCalls != 1 {
		t.Errorf("AddLabel called %d times, want 1", v.addLabelCalls)
	}
	if v.removeLabelCals != 1 {
		t.Errorf("RemoveLabel called %d times, want 1", v.removeLabelCals)
	}
	// Final label set should NOT contain the lock.
	if slices.Contains(v.labels[42], "agent-running") {
		t.Errorf("lock label still present after run: %v", v.labels[42])
	}
	// Result comment posted.
	if len(v.comments) != 1 {
		t.Fatalf("want 1 comment, got %d", len(v.comments))
	}
	if v.comments[0].body != "evaluation posted" {
		t.Errorf("comment body = %q", v.comments[0].body)
	}
}

func TestRun_AlreadyLocked_NoOp(t *testing.T) {
	op := &Operator{Name: "x", LockLabel: "running", Prompt: "p"}
	sub := &Subject{Number: 1, Labels: []string{"bug", "running"}}
	v := newFakeVCS()
	called := false

	err := Run(context.Background(), op, sub, v, RunOptions{
		Repo:    "r",
		RunFunc: func(context.Context, string, string, time.Duration) (string, error) { called = true; return "", nil },
	})
	if err != nil {
		t.Fatalf("want nil error (skip), got %v", err)
	}
	if called {
		t.Error("claude should NOT be invoked when lock already held")
	}
	if v.addLabelCalls != 0 {
		t.Error("AddLabel should not be called when already locked")
	}
	if len(v.comments) != 0 {
		t.Error("no comment should be posted when skipping")
	}
}

func TestRun_ClaudeFails_PostsFailureComment(t *testing.T) {
	op := &Operator{Name: "x", LockLabel: "running", Prompt: "p"}
	sub := &Subject{Number: 5, Labels: []string{"bug"}}
	v := newFakeVCS()

	claudeErr := errors.New("model refused")
	err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration) (string, error) {
			return "", claudeErr
		},
	})
	if err == nil {
		t.Fatal("want error when claude fails, got nil")
	}

	// Lock should be added AND removed.
	if v.addLabelCalls != 1 || v.removeLabelCals != 1 {
		t.Errorf("add=%d remove=%d, want 1/1", v.addLabelCalls, v.removeLabelCals)
	}
	// Failure comment should mention the op name.
	if len(v.comments) != 1 {
		t.Fatalf("want 1 failure comment, got %d", len(v.comments))
	}
	body := v.comments[0].body
	if !strings.Contains(body, "Operator `x` failed") {
		t.Errorf("failure comment missing op name marker: %q", body)
	}
	if !strings.Contains(body, "model refused") {
		t.Errorf("failure comment should include the claude error: %q", body)
	}
}

func TestRun_AddLabelFails_StopsEarly(t *testing.T) {
	op := &Operator{Name: "x", LockLabel: "running", Prompt: "p"}
	sub := &Subject{Number: 1}
	v := newFakeVCS()
	v.errOnAdd = true

	claudeCalled := false
	err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration) (string, error) {
			claudeCalled = true
			return "result", nil
		},
	})
	if err == nil {
		t.Fatal("want error when AddLabel fails")
	}
	if claudeCalled {
		t.Error("claude should not run when lock acquisition fails")
	}
	if len(v.comments) != 0 {
		t.Error("no comment should be posted when lock acquisition fails")
	}
}

func TestRun_EmptyClaudeOutput_NoComment(t *testing.T) {
	op := &Operator{Name: "x", LockLabel: "running", Prompt: "p"}
	sub := &Subject{Number: 1, Labels: []string{"bug"}}
	v := newFakeVCS()

	err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration) (string, error) {
			return "   \n\t  ", nil // whitespace only
		},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(v.comments) != 0 {
		t.Errorf("empty/whitespace output should produce no comment; got %v", v.comments)
	}
	// But the lock still cycled on/off.
	if v.addLabelCalls != 1 || v.removeLabelCals != 1 {
		t.Errorf("lock not cycled: add=%d remove=%d", v.addLabelCalls, v.removeLabelCals)
	}
}

func TestRun_DefaultRunFuncIsNil(t *testing.T) {
	// Zero-value RunOptions must leave RunFunc nil; Run() resolves nil to the
	// real RunClaude at call time. Guards against accidental default that
	// would mask missing DI.
	var opts RunOptions
	if opts.RunFunc != nil {
		t.Error("default RunOptions.RunFunc should be nil (resolved at Run time)")
	}
}
