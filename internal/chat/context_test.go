package chat

import (
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// All issue-level and repo-level chat builders must surface ClawFlow's own
// automation model + CLI cheatsheet so the AI doesn't ask "where do I file
// an issue?" or fail to recognize clawflow's label vocabulary. See #192.

func TestBuildIssueContext_HasSelfAwareness(t *testing.T) {
	issue := vcs.Issue{Number: 42, Title: "x", State: "open", Labels: []string{"bug"}, Body: "details"}
	out := BuildIssueContext("o/r", issue, nil)
	assertSelfAwareness(t, "BuildIssueContext", out)

	// Single-issue scope cheatsheet must NOT teach `issue create`. The
	// existing hard-constraint paragraph mentions it as forbidden — but
	// the actionable invocation form (with --title) must be absent.
	if strings.Contains(out, "issue create --repo") || strings.Contains(out, "issue create --title") {
		t.Error("BuildIssueContext (edit mode) leaked actionable `issue create` form into single-issue scope")
	}
}

func TestBuildIssueModeContext_HasSelfAwareness(t *testing.T) {
	issue := vcs.Issue{Number: 42, Title: "x", State: "open", Labels: []string{"feat"}, Body: "details"}
	out := BuildIssueModeContext("o/r", issue, nil)
	assertSelfAwareness(t, "BuildIssueModeContext", out)

	if strings.Contains(out, "issue create --repo") || strings.Contains(out, "issue create --title") {
		t.Error("BuildIssueModeContext leaked actionable `issue create` form into single-issue scope")
	}
}

func TestBuildRepoContext_HasSelfAwareness(t *testing.T) {
	out := BuildRepoContext("o/r", "github", "main", nil)
	assertSelfAwareness(t, "BuildRepoContext", out)

	// Repo scope is allowed to create issues — the actionable form should
	// be present in the cheatsheet.
	if !strings.Contains(out, "issue create --repo") {
		t.Error("BuildRepoContext should advertise the actionable `issue create --repo ...` form (ScopeRepo)")
	}
}

// LanguageRule must be the very last instruction across all chat builders so
// it isn't crowded out by anything we appended.
func TestChatBuilders_LanguageRuleIsLast(t *testing.T) {
	issue := vcs.Issue{Number: 1, Title: "t", State: "open"}
	cases := map[string]string{
		"BuildIssueContext":     BuildIssueContext("o/r", issue, nil),
		"BuildIssueModeContext": BuildIssueModeContext("o/r", issue, nil),
	}
	for name, prompt := range cases {
		langIdx := strings.Index(prompt, "Match the user's input language")
		autoIdx := strings.Index(prompt, "ClawFlow automation model")
		cliIdx := strings.Index(prompt, "clawflow CLI cheatsheet")
		if langIdx < 0 || autoIdx < 0 || cliIdx < 0 {
			t.Fatalf("%s: missing one of the expected sections (lang=%d auto=%d cli=%d)", name, langIdx, autoIdx, cliIdx)
		}
		if langIdx < autoIdx || langIdx < cliIdx {
			t.Errorf("%s: LanguageRule must come after both automation model and CLI cheatsheet", name)
		}
	}
}

func assertSelfAwareness(t *testing.T, builder, prompt string) {
	t.Helper()
	for _, want := range []string{
		"ClawFlow automation model", // AutomationModel header
		"ready-for-agent",           // label vocabulary
		"clawflow CLI cheatsheet",   // CLICheatsheet header
		"clawflow issue",            // at least one clawflow command
		"clawflow feedback",         // self-feedback escape hatch
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("%s: missing self-awareness fragment %q", builder, want)
		}
	}
}
