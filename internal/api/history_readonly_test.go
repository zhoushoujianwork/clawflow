package api

import (
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/store"
)

func TestRedactRunErrorRemovesSecretsAndPaths(t *testing.T) {
	in := "push failed for https://oauth2:glpat-abcdef123456@gitlab.example/g/p.git\n" +
		"Authorization: Bearer abc.def.ghi\n" +
		"token=ghp_0123456789abcdef\n" +
		"worktree /Users/alice/Library/Application Support/ClawFlow/wt/issue-1 missing\n" +
		"commit 0123456789abcdef0123456789abcdef01234567\n" +
		"Invalid prompt: your prompt was flagged as potentially violating our usage policy"
	got := redactRunError(in)
	for _, leak := range []string{"glpat-", "oauth2:", "abc.def.ghi", "ghp_", "/Users/alice", "0123456789abcdef0123456789abcdef"} {
		if strings.Contains(got, leak) {
			t.Errorf("leaked %q in %q", leak, got)
		}
	}
	if !strings.Contains(got, "Invalid prompt: your prompt was flagged") {
		t.Errorf("the failure reason must remain readable: %q", got)
	}
}

func TestRedactRunErrorIsBoundedAndValidUTF8(t *testing.T) {
	got := redactRunError(strings.Repeat("失败", 2000))
	if len(got) > readOnlyErrorLimit+len("…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("not bounded: %d bytes", len(got))
	}
	if strings.ToValidUTF8(got, "") != got {
		t.Fatal("truncation split a rune")
	}
}

// Read-only details expose the failure reason and attempt metadata, and nothing
// else from the recorded payload: no issue body, summary, prompt or workdir.
func TestReadOnlyRunDetailsAreWhitelisted(t *testing.T) {
	root := t.TempDir()
	row := store.Run{
		RunDir: root + "/runs/grp__smoke/issue-1/2026-10-27T11-00-00Z", Repo: "grp/smoke", Operator: "implement",
		Status: "failed", StartedAt: time.Date(2026, 10, 27, 11, 0, 0, 0, time.UTC), Error: "boom token=glpat-secretvalue1234",
		Payload: `{"repo":"grp/smoke","operator":"implement","issue_number":1,"issue_title":"t","status":"failed",` +
			`"started_at":"2026-10-27T11:00:00Z","issue_body":"PRIVATE BODY","summary":"PRIVATE SUMMARY",` +
			`"error":"boom token=glpat-secretvalue1234","development_session":{"workdir":"/Users/alice/wt","role":"developer"},` +
			`"prompt":"PRIVATE PROMPT"}`,
	}
	out, err := readOnlyRunDetails([]store.Run{row}, root, "run")
	if err != nil || len(out) != 1 {
		t.Fatalf("details: %v %v", out, err)
	}
	m := out[0]
	if m["status"] != "failed" || m["issue_title"] != "t" {
		t.Fatalf("summary fields missing: %v", m)
	}
	msg, _ := m["error"].(string)
	if !strings.Contains(msg, "boom") || strings.Contains(msg, "glpat-") {
		t.Fatalf("error not redacted: %q", msg)
	}
	for k, v := range m {
		if strings.Contains(strings.ToLower(k), "body") || strings.Contains(k, "prompt") || k == "summary" || k == "development_session" {
			t.Errorf("unexpected key %q exposed", k)
		}
		if s, ok := v.(string); ok && (strings.Contains(s, "PRIVATE") || strings.Contains(s, "/Users/alice")) {
			t.Errorf("private text exposed under %q: %q", k, s)
		}
	}
}
