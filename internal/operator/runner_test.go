package operator

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeVCS records label and comment operations so tests can assert on them
// without spinning up a real GitHub/GitLab client.
type fakeVCS struct {
	labels          map[int][]string
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

	output, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo:    "acme/webapp",
		Workdir: t.TempDir(),
		Timeout: time.Second,
		RunFunc: func(_ context.Context, prompt, _ string, _ time.Duration, _ io.Writer) (string, error) {
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
	if output != "evaluation posted" {
		t.Errorf("Run returned output %q, want %q", output, "evaluation posted")
	}

	if v.addLabelCalls != 1 {
		t.Errorf("AddLabel called %d times, want 1", v.addLabelCalls)
	}
	if v.removeLabelCals != 1 {
		t.Errorf("RemoveLabel called %d times, want 1", v.removeLabelCals)
	}
	if slices.Contains(v.labels[42], "agent-running") {
		t.Errorf("lock label still present after run: %v", v.labels[42])
	}
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

	out, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer) (string, error) {
			called = true
			return "", nil
		},
	})
	if err != nil {
		t.Fatalf("want nil error (skip), got %v", err)
	}
	if out != "" {
		t.Errorf("skip should return empty output; got %q", out)
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
	_, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer) (string, error) {
			return "", claudeErr
		},
	})
	if err == nil {
		t.Fatal("want error when claude fails, got nil")
	}

	if v.addLabelCalls != 1 || v.removeLabelCals != 1 {
		t.Errorf("add=%d remove=%d, want 1/1", v.addLabelCalls, v.removeLabelCals)
	}
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
	_, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer) (string, error) {
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

	_, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer) (string, error) {
			return "   \n\t  ", nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(v.comments) != 0 {
		t.Errorf("empty/whitespace output should produce no comment; got %v", v.comments)
	}
	if v.addLabelCalls != 1 || v.removeLabelCals != 1 {
		t.Errorf("lock not cycled: add=%d remove=%d", v.addLabelCalls, v.removeLabelCals)
	}
}

func TestRun_EventWriterReceivesRunFuncInput(t *testing.T) {
	// The RunFunc is invoked with whatever io.Writer the caller put in
	// RunOptions.EventWriter. Confirm the wiring so the dashboard's
	// events.jsonl sink actually gets passed through.
	op := &Operator{Name: "x", LockLabel: "l", Prompt: "p"}
	sub := &Subject{Number: 1, Labels: []string{"bug"}}
	v := newFakeVCS()
	var captured io.Writer
	sink := &bytes.Buffer{}

	_, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo:        "r",
		EventWriter: sink,
		RunFunc: func(_ context.Context, _, _ string, _ time.Duration, events io.Writer) (string, error) {
			captured = events
			return "ok", nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if captured != sink {
		t.Errorf("RunFunc received %v, want the EventWriter sink", captured)
	}
}

func TestRun_DefaultRunFuncIsNil(t *testing.T) {
	var opts RunOptions
	if opts.RunFunc != nil {
		t.Error("default RunOptions.RunFunc should be nil (resolved at Run time)")
	}
}

// --- outcome marker tests ---

func TestRun_OutcomeMarker_StripsAndAddsLabel(t *testing.T) {
	op := &Operator{
		Name:      "evaluate-bug",
		LockLabel: "agent-running",
		Outcomes:  []string{"agent-evaluated", "agent-skipped"},
	}
	sub := &Subject{Number: 7, Labels: []string{"bug"}}
	v := newFakeVCS()

	body := "## Eval\n\nRepro: 8/10\n\n<!-- clawflow:outcome=agent-evaluated -->\n"
	_, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer) (string, error) {
			return body, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(v.comments) != 1 {
		t.Fatalf("want 1 comment, got %d", len(v.comments))
	}
	if strings.Contains(v.comments[0].body, "clawflow:outcome") {
		t.Errorf("marker should be stripped from posted comment; got %q", v.comments[0].body)
	}
	if !slices.Contains(v.labels[7], "agent-evaluated") {
		t.Errorf("agent-evaluated should be added; labels = %v", v.labels[7])
	}
}

func TestRun_OutcomeMarker_NotInWhitelist_SkipsLabel(t *testing.T) {
	op := &Operator{
		Name:      "evaluate-bug",
		LockLabel: "lock",
		Outcomes:  []string{"agent-evaluated", "agent-skipped"},
	}
	sub := &Subject{Number: 1}
	v := newFakeVCS()

	body := "## Eval\n\nstuff\n\n<!-- clawflow:outcome=type:bug -->\n"
	_, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer) (string, error) {
			return body, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(v.comments) != 1 {
		t.Fatalf("want comment posted even on disallowed outcome, got %d", len(v.comments))
	}
	// AddLabel was called once for the lock label only — the disallowed
	// outcome must not have triggered another add.
	if v.addLabelCalls != 1 {
		t.Errorf("AddLabel calls = %d, want 1 (lock only)", v.addLabelCalls)
	}
	if slices.Contains(v.labels[1], "type:bug") {
		t.Errorf("disallowed outcome label was applied: %v", v.labels[1])
	}
}

func TestRun_OutcomeMarker_LastWins(t *testing.T) {
	op := &Operator{
		Name:      "x",
		LockLabel: "lock",
		Outcomes:  []string{"agent-evaluated", "agent-skipped"},
	}
	sub := &Subject{Number: 2}
	v := newFakeVCS()

	body := "draft 1\n<!-- clawflow:outcome=agent-skipped -->\nfinal\n<!-- clawflow:outcome=agent-evaluated -->\n"
	_, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer) (string, error) {
			return body, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !slices.Contains(v.labels[2], "agent-evaluated") {
		t.Errorf("last marker wins; labels = %v, want agent-evaluated", v.labels[2])
	}
	if slices.Contains(v.labels[2], "agent-skipped") {
		t.Errorf("earlier marker should be ignored; labels = %v", v.labels[2])
	}
}

func TestRun_OutcomeMarker_NoOutcomesSet_AcceptsAny(t *testing.T) {
	op := &Operator{
		Name:      "x",
		LockLabel: "lock",
		// No Outcomes set — runner accepts whatever the operator emits.
	}
	sub := &Subject{Number: 3}
	v := newFakeVCS()

	body := "answer\n<!-- clawflow:outcome=ready-for-agent -->\n"
	_, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer) (string, error) {
			return body, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !slices.Contains(v.labels[3], "ready-for-agent") {
		t.Errorf("empty whitelist should accept any label; got %v", v.labels[3])
	}
}

func TestRun_OutcomeMarker_None_BackCompat(t *testing.T) {
	op := &Operator{
		Name:      "legacy-skill",
		LockLabel: "lock",
		Outcomes:  []string{"agent-evaluated"},
	}
	sub := &Subject{Number: 4}
	v := newFakeVCS()

	body := "old-style operator output, no marker"
	_, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer) (string, error) {
			return body, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(v.comments) != 1 || v.comments[0].body != body {
		t.Errorf("comment should be posted unchanged when no marker present; got %v", v.comments)
	}
	// Only the lock label, no outcome label added.
	if v.addLabelCalls != 1 {
		t.Errorf("AddLabel calls = %d, want 1", v.addLabelCalls)
	}
}

func TestParseOutcome_Direct(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantLbl  string
		wantBody string
	}{
		{"none", "just text", "", "just text"},
		{"single", "body\n<!-- clawflow:outcome=agent-evaluated -->\n", "agent-evaluated", "body"},
		{"label with hyphens and dots", "x\n<!-- clawflow:outcome=v1.2-rc -->\n", "v1.2-rc", "x"},
		{"trailing whitespace tolerant", "x\n<!-- clawflow:outcome=agent-skipped --> \n", "agent-skipped", "x"},
		{"multiple — last wins", "<!-- clawflow:outcome=a -->\nfoo\n<!-- clawflow:outcome=b -->\n", "b", "foo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotLbl, gotBody := parseOutcome(c.in)
			if gotLbl != c.wantLbl {
				t.Errorf("label = %q, want %q", gotLbl, c.wantLbl)
			}
			if gotBody != c.wantBody {
				t.Errorf("body = %q, want %q", gotBody, c.wantBody)
			}
		})
	}
}
