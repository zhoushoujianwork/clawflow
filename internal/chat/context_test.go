package chat

import (
	"github.com/zhoushoujianwork/clawflow/internal/chat/prompts"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// All issue-level and repo-level chat builders must surface ClawFlow's own
// automation model + CLI cheatsheet so the AI doesn't ask "where do I file
// an issue?" or fail to recognize clawflow's label vocabulary. See #192.

func TestBuildIssueContext_HasSelfAwareness(t *testing.T) {
	issue := vcs.Issue{Number: 42, Title: "x", State: "open", Labels: []string{"bug"}, Body: "details"}
	out := BuildIssueContext("o/r", issue, nil, "")
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
	out := BuildIssueModeContext("o/r", issue, nil, "")
	assertSelfAwareness(t, "BuildIssueModeContext", out)

	if strings.Contains(out, "issue create --repo") || strings.Contains(out, "issue create --title") {
		t.Error("BuildIssueModeContext leaked actionable `issue create` form into single-issue scope")
	}
}

func TestBuildRepoContext_HasSelfAwareness(t *testing.T) {
	out := BuildRepoContext("o/r", "github", "main", nil, "")
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
		"BuildIssueContext":     BuildIssueContext("o/r", issue, nil, ""),
		"BuildIssueModeContext": BuildIssueModeContext("o/r", issue, nil, ""),
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

// TestChatBuilders_LanguageOverride verifies that the language parameter is
// threaded into each builder's output.
func TestChatBuilders_LanguageOverride(t *testing.T) {
	issue := vcs.Issue{Number: 1, Title: "t", State: "open"}

	for _, lang := range []string{"zh", "en"} {
		outIssue := BuildIssueContext("o/r", issue, nil, lang)
		outIssueMode := BuildIssueModeContext("o/r", issue, nil, lang)
		outRepo := BuildRepoContext("o/r", "github", "main", nil, lang)
		for name, out := range map[string]string{
			"BuildIssueContext[" + lang + "]":     outIssue,
			"BuildIssueModeContext[" + lang + "]": outIssueMode,
			"BuildRepoContext[" + lang + "]":      outRepo,
		} {
			switch lang {
			case "zh":
				if !strings.Contains(out, "简体中文") {
					t.Errorf("%s: expected Chinese directive, not found", name)
				}
			case "en":
				if !strings.Contains(out, "Always respond in **English**") {
					t.Errorf("%s: expected English directive, not found", name)
				}
			}
		}
	}
}

func assertSelfAwareness(t *testing.T, builder, prompt string) {
	t.Helper()
	for _, want := range []string{
		"ClawFlow automation model", // AutomationModel header
		"agent-implemented",         // lifecycle vocabulary
		"clawflow CLI cheatsheet",   // CLICheatsheet header
		"clawflow issue",            // at least one clawflow command
		"clawflow feedback",         // self-feedback escape hatch
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("%s: missing self-awareness fragment %q", builder, want)
		}
	}
}

func TestChatBuilders_ScopeChangesGoToBody(t *testing.T) {
	issue := vcs.Issue{Number: 42, Title: "x", State: "open", Body: "details"}
	for name, out := range map[string]string{
		"edit":  BuildIssueContext("o/r", issue, nil, ""),
		"issue": BuildIssueModeContext("o/r", issue, nil, ""),
	} {
		if !strings.Contains(out, "clawflow issue edit --repo o/r --issue 42") {
			t.Errorf("%s: missing issue edit command", name)
		}
	}
	out := BuildIssueModeContext("o/r", issue, nil, "")
	if !strings.Contains(out, "update the issue body instead") {
		t.Error("issue mode must tell scope changes to go into the body")
	}
}

func TestChatBuilders_CommentsAreContextNotRequirements(t *testing.T) {
	issue := vcs.Issue{Number: 42, Title: "x", State: "open", Body: "details"}
	for name, out := range map[string]string{
		"edit":  BuildIssueContext("o/r", issue, nil, ""),
		"issue": BuildIssueModeContext("o/r", issue, nil, ""),
	} {
		if strings.Contains(out, "comments are not read as requirements") {
			t.Errorf("%s: stale claim that comments are not read", name)
		}
		flat := strings.Join(strings.Fields(out), " ")
		if !strings.Contains(flat, "not a requirement source") || !strings.Contains(flat, "scope change MUST be written into the body") {
			t.Errorf("%s: missing comment/body requirement rule", name)
		}
	}
}

func TestCLICheatsheet_IssueCommentAndCloseUseIssueFlag(t *testing.T) {
	for _, scope := range []prompts.Scope{prompts.ScopeFull, prompts.ScopeSingleIssue, prompts.ScopeRepo} {
		out := prompts.CLICheatsheet(scope)
		for _, cmd := range []string{"issue comment", "issue close"} {
			if !strings.Contains(out, cmd+" --repo <owner/name> --issue <number>") {
				t.Errorf("scope %v: %s must use --issue <number>", scope, cmd)
			}
		}
		for _, bad := range []string{"issue comment --repo <owner/name> <number>", "issue close --repo <owner/name> <number>"} {
			if strings.Contains(out, bad) {
				t.Errorf("scope %v: positional %q", scope, bad)
			}
		}
		for _, cmd := range []string{"pr view", "pr comment"} {
			if !strings.Contains(out, cmd+" --repo <owner/name> --pr <number>") {
				t.Errorf("scope %v: %s must use --pr <number>", scope, cmd)
			}
		}
	}
}
