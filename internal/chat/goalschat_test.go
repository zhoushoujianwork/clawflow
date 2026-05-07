package chat

import "testing"

func TestBuildGoalsChatContext(t *testing.T) {
	repos := []ProjectChatRepo{
		{Name: "owner/api", LocalPath: "/tmp/api"},
		{Name: "owner/web", LocalPath: ""},
	}
	current := "# Existing\n\n- ship v1"
	got := BuildGoalsChatContext("my-proj", repos, current)

	mustContain := []struct {
		needle string
		why    string
	}{
		{"my-proj", "missing project name"},
		{"owner/api", "missing repo with local clone"},
		{"/tmp/api", "missing local path for cloned repo"},
		{"owner/web", "missing repo without local clone"},
		{"no local_path", "missing caveat for repo lacking local clone"},
		{"# Existing", "missing current goals.md content"},
		{"```goals.md", "missing fenced-block protocol instruction"},
		{"FULL final content", "missing instruction to emit full document, not diff"},
		{"Match the user's input language", "missing language-mirroring instruction"},
		{"default to\nChinese", "missing Chinese default fallback"},
		{"conversational", "missing conversational tone hint"},
	}
	for _, m := range mustContain {
		if !containsStr(got, m.needle) {
			t.Errorf("%s: missing %q", m.why, m.needle)
		}
	}

	// No-write tools must be visible in the prompt.
	if !containsStr(got, "Read / Grep / Glob") {
		t.Error("missing read-only tool list")
	}
	if !containsStr(got, "do NOT have Edit") && !containsStr(got, "NOT have Edit") {
		t.Error("missing explicit Edit/Write/Bash exclusion")
	}
}

func TestBuildGoalsChatContext_EmptyCurrent(t *testing.T) {
	got := BuildGoalsChatContext("p", nil, "")
	if !containsStr(got, "fresh draft") {
		t.Error("missing fresh-draft hint when current goals.md is empty")
	}
	if !containsStr(got, "(none") {
		t.Error("missing fallback for projects with no member repos")
	}
}
