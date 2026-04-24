package operator

import (
	"strings"
	"testing"
)

func TestBuildPrompt_Issue(t *testing.T) {
	op := &Operator{Name: "evaluate-bug", Prompt: "analyze the bug and post a comment"}
	sub := &Subject{
		Number: 42,
		Title:  "app crashes on login",
		Body:   "repro: click login, app dies",
		Labels: []string{"bug", "p1"},
		IsPR:   false,
	}
	p := BuildPrompt(op, sub, "acme/webapp", nil)

	mustContain(t, p, "Repo: acme/webapp")
	mustContain(t, p, "Issue Number: #42")
	mustContain(t, p, "Issue Title: app crashes on login")
	mustContain(t, p, "[bug p1]")
	mustContain(t, p, "repro: click login, app dies")
	mustContain(t, p, "# Your Task (Operator: evaluate-bug)")
	mustContain(t, p, "analyze the bug and post a comment")

	mustNotContain(t, p, "Head Branch")
	mustNotContain(t, p, "Recent Comments")
	mustNotContain(t, p, "Pull Request")
}

func TestBuildPrompt_PR(t *testing.T) {
	op := &Operator{Name: "review-pr", Prompt: "review the diff"}
	sub := &Subject{
		Number:     7,
		Title:      "fix login crash",
		Body:       "Fixes #42",
		Labels:     []string{"ready-to-review"},
		IsPR:       true,
		HeadBranch: "fix/issue-42",
		URL:        "https://github.com/acme/webapp/pull/7",
	}
	p := BuildPrompt(op, sub, "acme/webapp", nil)

	mustContain(t, p, "Pull Request Number: #7")
	mustContain(t, p, "Pull Request Title: fix login crash")
	mustContain(t, p, "Head Branch: fix/issue-42")
	mustContain(t, p, "URL: https://github.com/acme/webapp/pull/7")
	mustNotContain(t, p, "Issue Number")
}

func TestBuildPrompt_EmptyBody(t *testing.T) {
	op := &Operator{Name: "x", Prompt: "p"}
	sub := &Subject{Number: 1, Title: "t", Body: "", Labels: nil}
	p := BuildPrompt(op, sub, "r", nil)
	mustContain(t, p, "_(empty)_")
}

func TestBuildPrompt_WhitespaceOnlyBody(t *testing.T) {
	op := &Operator{Name: "x", Prompt: "p"}
	sub := &Subject{Number: 1, Title: "t", Body: "   \n\t\n  "}
	p := BuildPrompt(op, sub, "r", nil)
	mustContain(t, p, "_(empty)_")
}

func TestBuildPrompt_Comments(t *testing.T) {
	op := &Operator{Name: "x", Prompt: "reply"}
	sub := &Subject{Number: 1, Title: "t", Body: "hi"}
	p := BuildPrompt(op, sub, "r", []string{"first reply", "second reply"})

	mustContain(t, p, "## Recent Comments")
	mustContain(t, p, "### Comment 1")
	mustContain(t, p, "first reply")
	mustContain(t, p, "### Comment 2")
	mustContain(t, p, "second reply")
}

func TestBuildPrompt_EmptyCommentsSliceOmitsSection(t *testing.T) {
	op := &Operator{Name: "x", Prompt: "p"}
	sub := &Subject{Number: 1, Title: "t", Body: "b"}
	p := BuildPrompt(op, sub, "r", []string{})
	mustNotContain(t, p, "Recent Comments")
}

func TestBuildPrompt_TaskSectionAppearsAfterContext(t *testing.T) {
	op := &Operator{Name: "op", Prompt: "MARKER"}
	sub := &Subject{Number: 1, Title: "body", Body: "CONTEXT_BODY"}
	p := BuildPrompt(op, sub, "r", nil)

	ctxIdx := strings.Index(p, "CONTEXT_BODY")
	taskIdx := strings.Index(p, "MARKER")
	if ctxIdx < 0 || taskIdx < 0 {
		t.Fatalf("markers missing; ctxIdx=%d taskIdx=%d", ctxIdx, taskIdx)
	}
	if taskIdx <= ctxIdx {
		t.Errorf("task section (%d) should come after context (%d)", taskIdx, ctxIdx)
	}
}

func mustContain(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Errorf("prompt missing %q\n--- full prompt ---\n%s", sub, s)
	}
}

func mustNotContain(t *testing.T, s, sub string) {
	t.Helper()
	if strings.Contains(s, sub) {
		t.Errorf("prompt unexpectedly contains %q\n--- full prompt ---\n%s", sub, s)
	}
}
