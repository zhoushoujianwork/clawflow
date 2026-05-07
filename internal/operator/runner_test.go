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
	closedIssues    []int
	errOnAdd        bool
	errOnRemove     bool
	errOnComment    bool
	errOnClose      bool
	addLabelCalls   int
	removeLabelCals int
	closeCalls      int
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

func (f *fakeVCS) CloseIssue(repo string, issueNumber int) error {
	f.closeCalls++
	if f.errOnClose {
		return errors.New("fake close-issue error")
	}
	f.closedIssues = append(f.closedIssues, issueNumber)
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

	output, _, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo:    "acme/webapp",
		Workdir: t.TempDir(),
		Timeout: time.Second,
		RunFunc: func(_ context.Context, prompt, _ string, _ time.Duration, _ io.Writer, _ string, sysp ...string) (string, error) {
			if !strings.Contains(prompt, "Issue Number: #42") {
				t.Errorf("fake claude did not receive issue context in user message; got: %q", prompt)
			}
			if len(sysp) == 0 || !strings.Contains(sysp[0], "do the thing") {
				t.Errorf("fake claude did not receive op prompt in system prompt; got: %v", sysp)
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

	// No outcomes / no triggers in this op, so no AddLabel or RemoveLabel
	// calls should fire. Lock-label add/remove was removed when the
	// `agent-running` label model was retired in favor of in-process locks.
	if v.addLabelCalls != 0 {
		t.Errorf("AddLabel called %d times, want 0 (no outcomes declared)", v.addLabelCalls)
	}
	if v.removeLabelCals != 0 {
		t.Errorf("RemoveLabel called %d times, want 0", v.removeLabelCals)
	}
	// No outcome marker in the output → runner skips the comment post to
	// prevent duplicate accumulation (the self-posting guard introduced in
	// fix/issue-53). A real operator must include a marker; this test op
	// intentionally omits one to verify the guard fires cleanly.
	if len(v.comments) != 0 {
		t.Fatalf("want 0 comments (no outcome marker → guard skips post), got %d", len(v.comments))
	}
}

// TestRun_ClaudeFails_NoIssuePollution locks the failure-path
// contract: when the underlying claude run errors, the runner must
// NOT post a comment to the issue and must NOT add any labels. The
// failure is recorded out-of-band (events.jsonl, dashboard run row),
// and the upstream circuit breaker — not this layer — decides when a
// run of failures warrants the `agent-failed` label. Keeping the
// issue thread quiet lets a human (or PM patrol) own recovery.
func TestRun_ClaudeFails_NoIssuePollution(t *testing.T) {
	op := &Operator{Name: "x", LockLabel: "running", Prompt: "p"}
	sub := &Subject{Number: 5, Labels: []string{"bug"}}
	v := newFakeVCS()

	claudeErr := errors.New("model refused")
	_, _, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer, string, ...string) (string, error) {
			return "", claudeErr
		},
	})
	if err == nil {
		t.Fatal("want error when claude fails, got nil")
	}

	if v.addLabelCalls != 0 || v.removeLabelCals != 0 {
		t.Errorf("add=%d remove=%d, want 0/0 (no labels touched on failure)", v.addLabelCalls, v.removeLabelCals)
	}
	if len(v.comments) != 0 {
		t.Fatalf("want 0 comments on failure (issue thread stays clean), got %d: %+v", len(v.comments), v.comments)
	}
}

func TestRun_EmptyClaudeOutput_NoComment(t *testing.T) {
	op := &Operator{Name: "x", LockLabel: "running", Prompt: "p"}
	sub := &Subject{Number: 1, Labels: []string{"bug"}}
	v := newFakeVCS()

	_, _, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer, string, ...string) (string, error) {
			return "   \n\t  ", nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(v.comments) != 0 {
		t.Errorf("empty/whitespace output should produce no comment; got %v", v.comments)
	}
	if v.addLabelCalls != 0 || v.removeLabelCals != 0 {
		t.Errorf("no labels should be touched on empty output: add=%d remove=%d", v.addLabelCalls, v.removeLabelCals)
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

	_, _, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo:        "r",
		EventWriter: sink,
		RunFunc: func(_ context.Context, _, _ string, _ time.Duration, events io.Writer, _ string, _ ...string) (string, error) {
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
	_, _, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer, string, ...string) (string, error) {
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
	_, _, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer, string, ...string) (string, error) {
			return body, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(v.comments) != 1 {
		t.Fatalf("want comment posted even on disallowed outcome, got %d", len(v.comments))
	}
	// AddLabel must NOT have been called — the disallowed outcome was
	// rejected and there's no longer any lock-label to add.
	if v.addLabelCalls != 0 {
		t.Errorf("AddLabel calls = %d, want 0 (disallowed outcome rejected)", v.addLabelCalls)
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
	_, _, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer, string, ...string) (string, error) {
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
	_, _, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer, string, ...string) (string, error) {
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

// TestRun_OutcomeMarker_None_NoPost verifies that when an operator produces
// output with no outcome marker, the runner does NOT post a comment. This
// prevents the infinite-loop / duplicate-comment accumulation that occurs when
// a model violates the output contract by self-posting via a VCS tool call and
// emitting only a short summary line (with no marker) to stdout.
func TestRun_OutcomeMarker_None_NoPost(t *testing.T) {
	op := &Operator{
		Name:      "legacy-skill",
		LockLabel: "lock",
		Outcomes:  []string{"agent-evaluated"},
	}
	sub := &Subject{Number: 4}
	v := newFakeVCS()

	body := "old-style operator output, no marker"
	_, _, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer, string, ...string) (string, error) {
			return body, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// No marker → runner skips comment post (self-posting guard).
	if len(v.comments) != 0 {
		t.Errorf("want 0 comments (no marker → guard skips post); got %v", v.comments)
	}
	// No marker → no outcome label.
	if v.addLabelCalls != 0 {
		t.Errorf("AddLabel calls = %d, want 0", v.addLabelCalls)
	}
}

func TestRun_OutcomeMarker_RemovesTriggerLabels(t *testing.T) {
	op := &Operator{
		Name:      "implement",
		LockLabel: "agent-running",
		Trigger: Trigger{
			LabelsRequired: []string{"ready-for-agent"},
		},
		Outcomes: []string{"agent-implemented", "agent-failed", "agent-skipped"},
	}
	sub := &Subject{Number: 10, Labels: []string{"ready-for-agent"}}
	v := newFakeVCS()

	body := "## ✅ ClawFlow fix complete\n\nPR opened\n\n<!-- clawflow:outcome=agent-implemented -->\n"
	_, _, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "r",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer, string, ...string) (string, error) {
			return body, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}

	// Outcome label should be added
	if !slices.Contains(v.labels[10], "agent-implemented") {
		t.Errorf("agent-implemented should be added; labels = %v", v.labels[10])
	}

	// Trigger label should be removed
	if slices.Contains(v.labels[10], "ready-for-agent") {
		t.Errorf("ready-for-agent trigger label should be removed; labels = %v", v.labels[10])
	}

	// Only the trigger label cleanup remains — lock label removal is gone.
	if v.removeLabelCals != 1 {
		t.Errorf("RemoveLabel called %d times, want 1 (trigger only)", v.removeLabelCals)
	}

	// Non-close outcome should NOT close the issue.
	if v.closeCalls != 0 {
		t.Errorf("CloseIssue called %d times, want 0 (outcome is not agent-closed)", v.closeCalls)
	}
}

func TestRun_OutcomeAgentClosed_ClosesIssue(t *testing.T) {
	op := &Operator{
		Name:      "track-progress",
		LockLabel: "agent-running",
		Trigger: Trigger{
			LabelsRequired: []string{"progress-check"},
		},
		Outcomes: []string{"agent-closed", "agent-watching"},
	}
	sub := &Subject{Number: 15, Labels: []string{"progress-check"}}
	v := newFakeVCS()

	body := "## 📊 Progress Check\n\n3/3 complete. Closing.\n\n<!-- clawflow:outcome=agent-closed -->\n"
	_, outcome, err := Run(context.Background(), op, sub, v, RunOptions{
		Repo: "acme/webapp",
		RunFunc: func(context.Context, string, string, time.Duration, io.Writer, string, ...string) (string, error) {
			return body, nil
		},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if outcome != "agent-closed" {
		t.Errorf("outcome = %q, want %q", outcome, "agent-closed")
	}

	// Issue should be closed.
	if v.closeCalls != 1 {
		t.Fatalf("CloseIssue called %d times, want 1", v.closeCalls)
	}
	if !slices.Contains(v.closedIssues, 15) {
		t.Errorf("issue #15 should be in closedIssues; got %v", v.closedIssues)
	}

	// Outcome label should still be added.
	if !slices.Contains(v.labels[15], "agent-closed") {
		t.Errorf("agent-closed label should be added; labels = %v", v.labels[15])
	}

	// Trigger label should be removed.
	if slices.Contains(v.labels[15], "progress-check") {
		t.Errorf("progress-check trigger label should be removed; labels = %v", v.labels[15])
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
