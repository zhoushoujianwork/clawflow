package prompts

import (
	"strings"
	"testing"
)

func TestCLICheatsheet_FullScope_HasSubIssueCommands(t *testing.T) {
	out := CLICheatsheet(ScopeFull)
	for _, want := range []string{"add-sub", "list-sub"} {
		if !strings.Contains(out, want) {
			t.Errorf("ScopeFull cheatsheet missing %q", want)
		}
	}
}

func TestCLICheatsheet_RepoScope_NoSubIssueCommands(t *testing.T) {
	out := CLICheatsheet(ScopeRepo)
	for _, absent := range []string{"add-sub", "list-sub"} {
		if strings.Contains(out, absent) {
			t.Errorf("ScopeRepo cheatsheet should not contain %q", absent)
		}
	}
}

func TestCLICheatsheet_SingleIssueScope_NoSubIssueCommands(t *testing.T) {
	out := CLICheatsheet(ScopeSingleIssue)
	for _, absent := range []string{"add-sub", "list-sub"} {
		if strings.Contains(out, absent) {
			t.Errorf("ScopeSingleIssue cheatsheet should not contain %q", absent)
		}
	}
}

func TestCLICheatsheet_LabelsFlag_Consistent(t *testing.T) {
	// issue create has NO --labels flag — all scopes that include create
	// must say so, and none should show "[--label bug]" (the old wrong form).
	for _, scope := range []Scope{ScopeFull, ScopeRepo} {
		out := CLICheatsheet(scope)
		if strings.Contains(out, "[--label bug]") {
			t.Errorf("scope %d: cheatsheet shows old incorrect [--label bug] flag", scope)
		}
		if !strings.Contains(out, "no `--labels` flag") {
			t.Errorf("scope %d: cheatsheet missing 'no --labels flag' clarification", scope)
		}
	}
}

func TestCLICheatsheet_SingleIssueScope_NoCreate(t *testing.T) {
	out := CLICheatsheet(ScopeSingleIssue)
	if strings.Contains(out, "issue create") {
		t.Error("ScopeSingleIssue cheatsheet should not include 'issue create'")
	}
}

func TestCLICheatsheet_AllScopes_HaveBasicCommands(t *testing.T) {
	for _, scope := range []Scope{ScopeFull, ScopeRepo, ScopeSingleIssue} {
		out := CLICheatsheet(scope)
		for _, want := range []string{"issue list", "issue view", "label add", "label remove"} {
			if !strings.Contains(out, want) {
				t.Errorf("scope %d: cheatsheet missing %q", scope, want)
			}
		}
	}
}

func TestLanguageRule_Content(t *testing.T) {
	out := LanguageRule()
	if !strings.Contains(out, "Match the user's input language") {
		t.Error("LanguageRule missing language-mirroring instruction")
	}
	if !strings.Contains(out, "Chinese") {
		t.Error("LanguageRule missing Chinese default fallback")
	}
}

func TestBehaviorRules_Content(t *testing.T) {
	out := BehaviorRules()
	if !strings.Contains(out, "Confirm before mutations") {
		t.Error("BehaviorRules missing confirm-before-mutations rule")
	}
	if !strings.Contains(out, "Cross-repo by default") {
		t.Error("BehaviorRules missing cross-repo rule")
	}
	if !strings.Contains(out, "Stay grounded") {
		t.Error("BehaviorRules missing stay-grounded rule")
	}
}
