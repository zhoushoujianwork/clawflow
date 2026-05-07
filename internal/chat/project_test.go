package chat

import (
	"testing"
)

func TestExtractFencedBlock(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple block",
			input: "Some text\n```context.md\n# My Project\n\nOverview here.\n```\nMore text",
			want:  "# My Project\n\nOverview here.",
		},
		{
			name:  "multiple blocks returns last",
			input: "```context.md\n# V1\n```\nSome discussion\n```context.md\n# V2 Final\n\nUpdated.\n```\n",
			want:  "# V2 Final\n\nUpdated.",
		},
		{
			name:  "no block",
			input: "Just some regular text\nwith no code blocks",
			want:  "",
		},
		{
			name:  "wrong info string",
			input: "```markdown\n# Not context.md\n```",
			want:  "",
		},
		{
			name:  "block with space before info string",
			input: "``` context.md\n# Spaced\n```",
			want:  "# Spaced",
		},
		{
			name:  "empty block",
			input: "```context.md\n```",
			want:  "",
		},
		{
			name:  "multiline content",
			input: "```context.md\n# Project\n\n## Overview\n\nThis is a project.\n\n## Architecture\n\nMicroservices.\n```",
			want:  "# Project\n\n## Overview\n\nThis is a project.\n\n## Architecture\n\nMicroservices.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFencedBlock(tt.input, "context.md")
			if got != tt.want {
				t.Errorf("extractFencedBlock() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractLastContextMD(t *testing.T) {
	tests := []struct {
		name   string
		stream string
		want   string
	}{
		{
			name: "content_block with text",
			stream: `{"type":"content_block_start","content_block":{"type":"text","text":"Here is the updated document:\n\n` + "```context.md\\n# Updated\\n```" + `"}}
`,
			want: "# Updated",
		},
		{
			name: "message with content array",
			stream: `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"` + "```context.md\\n# From Array\\n```" + `"}]}}
`,
			want: "# From Array",
		},
		{
			name: "assistant role with inline content",
			stream: `{"role":"assistant","content":"` + "```context.md\\n# Inline\\n```" + `"}
`,
			want: "# Inline",
		},
		{
			name:   "no context.md block in output",
			stream: `{"type":"content_block_start","content_block":{"type":"text","text":"Just a regular response."}}` + "\n",
			want:   "",
		},
		{
			name:   "empty stream",
			stream: "",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractLastContextMD(tt.stream)
			if got != tt.want {
				t.Errorf("ExtractLastContextMD() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildProjectChatContext(t *testing.T) {
	repos := []ProjectChatRepo{
		{Name: "owner/repo-a", LocalPath: "/tmp/repo-a"},
		{Name: "owner/repo-b", LocalPath: ""},
	}
	ctx := BuildProjectChatContext("my-proj", repos, "# My Project\n\nSome content.\n", "")

	if !containsStr(ctx, "Project: my-proj") {
		t.Error("missing project name in context")
	}
	if !containsStr(ctx, "# My Project") {
		t.Error("missing original context.md content")
	}
	if !containsStr(ctx, "context.md") {
		t.Error("missing instruction about context.md code block")
	}
	if !containsStr(ctx, "owner/repo-a") || !containsStr(ctx, "/tmp/repo-a") {
		t.Error("missing member repo with local_path")
	}
	if !containsStr(ctx, "owner/repo-b") || !containsStr(ctx, "no local_path") {
		t.Error("missing member repo without local_path or its caveat")
	}
	if !containsStr(ctx, "clawflow issue") {
		t.Error("missing clawflow CLI cheatsheet")
	}
	if !containsStr(ctx, "Confirm before mutations") {
		t.Error("missing mutation-confirmation rule")
	}
}

func TestBuildPilotContext_WithDeployment(t *testing.T) {
	repos := []PilotRepoDigest{
		{Name: "owner/api", OpenIssues: []PilotIssueRow{{Number: 1, Title: "crash on startup", Labels: []string{"bug"}}}},
	}
	deploymentMD := "## Logs\n\n```bash\nssh prod 'journalctl -u myapp -n 200'\n```"

	prompt := BuildPilotContext("my-proj", "# Context\n\nSome overview.", "", deploymentMD, nil, repos)

	if !containsStr(prompt, "Deployment & runtime health") {
		t.Error("missing 'Deployment & runtime health' section")
	}
	if !containsStr(prompt, "journalctl") {
		t.Error("missing deployment.md content in prompt")
	}
	if !containsStr(prompt, "repeated errors") {
		t.Error("missing log analysis guidance")
	}
	if !containsStr(prompt, "AT MOST 2 new issues") {
		t.Error("missing issue filing budget cap")
	}
	if !containsStr(prompt, "Production-breaking errors") {
		t.Error("missing filing budget prioritization")
	}
	if !containsStr(prompt, "start by inspecting runtime logs") {
		t.Error("missing log-first triage instruction")
	}
	if !containsStr(prompt, "my-proj") {
		t.Error("missing project name")
	}
}

func TestBuildPilotContext_NoDeployment(t *testing.T) {
	repos := []PilotRepoDigest{}

	prompt := BuildPilotContext("empty-proj", "", "", "", nil, repos)

	if !containsStr(prompt, "Deployment & runtime health") {
		t.Error("missing 'Deployment & runtime health' section even when empty")
	}
	if !containsStr(prompt, "deployment.md not found") {
		t.Error("missing fallback message when deployment.md is absent")
	}
	if !containsStr(prompt, "AT MOST 2 new issues") {
		t.Error("missing issue filing budget even without deployment.md")
	}
	// goals.md fallback when empty
	if !containsStr(prompt, "no explicit user goals") {
		t.Error("missing goals.md fallback message when empty")
	}
	// recent wakes fallback when empty
	if !containsStr(prompt, "first Pilot run") {
		t.Error("missing recent-wakes fallback when none on file")
	}
}

func TestBuildPilotContext_GoalsAndRecent(t *testing.T) {
	repos := []PilotRepoDigest{}
	goals := "## Priorities\n- ship v1 by EOM\n- no flaky tests"
	recent := []PilotWakeSummary{
		{StartedAt: "2026-05-06T12:00:00Z", Status: "success", Result: "PILOT-RESULT: 2 actions — labeled api#7, closed api#3"},
		{StartedAt: "2026-05-05T12:00:00Z", Status: "success", Result: "PILOT-RESULT: no-action — backlog coherent"},
	}

	prompt := BuildPilotContext("my-proj", "", goals, "", recent, repos)

	if !containsStr(prompt, "User goals (goals.md") {
		t.Error("missing User goals section header")
	}
	if !containsStr(prompt, "ship v1 by EOM") {
		t.Error("missing goals.md content in prompt")
	}
	if !containsStr(prompt, "Recent wake history") {
		t.Error("missing Recent wake history section")
	}
	if !containsStr(prompt, "labeled api#7") {
		t.Error("missing recent wake result line")
	}
	if !containsStr(prompt, "Don't repeat what you already did") {
		t.Error("missing recent-wakes anti-duplication guidance")
	}
	// context.md update protocol always present
	if !containsStr(prompt, "Updating your own memory") {
		t.Error("missing context.md update protocol section")
	}
	if !containsStr(prompt, "```context.md") {
		t.Error("missing fenced context.md block in update protocol")
	}
}

func TestBuildPilotContext_StandardPlays(t *testing.T) {
	repos := []PilotRepoDigest{}
	prompt := BuildPilotContext("p", "", "", "", nil, repos)

	// Section header present
	if !containsStr(prompt, "Standard plays") {
		t.Error("missing Standard plays section header")
	}

	// Play 1 — branch cleanup
	if !containsStr(prompt, "Stale-branch cleanup") {
		t.Error("missing Play 1 (stale branch cleanup) header")
	}
	if !containsStr(prompt, "git push origin --delete") {
		t.Error("missing branch deletion command")
	}
	if !containsStr(prompt, "merged") || !containsStr(prompt, "default/base branch") {
		t.Error("missing branch-cleanup safety bounds (merged-only, skip default)")
	}

	// Play 2 — conflict resolution
	if !containsStr(prompt, "Merge-conflict resolution") {
		t.Error("missing Play 2 (conflict resolution) header")
	}
	if !containsStr(prompt, "git rebase") {
		t.Error("missing rebase instruction")
	}
	if !containsStr(prompt, "--force-with-lease") {
		t.Error("missing force-with-lease guard")
	}
	if !containsStr(prompt, "ONE conflict resolution per wake") {
		t.Error("missing per-wake conflict resolution cap")
	}
	if !containsStr(prompt, "git rebase --abort") {
		t.Error("missing abort-on-failure escape hatch")
	}

	// Play 3 — log patrol
	if !containsStr(prompt, "Runtime log patrol") {
		t.Error("missing Play 3 (log patrol) header")
	}
	if !containsStr(prompt, "PATROL:") {
		t.Error("missing PATROL summary line format")
	}

	// Hard rules updated to allow narrow Edit/push exceptions
	if !containsStr(prompt, "Edit") || !containsStr(prompt, "ONLY inside an") {
		t.Error("hard rules missing the active-play scope on Edit/Write")
	}
	if !containsStr(prompt, "push --force-with-lease") {
		t.Error("hard rules missing force-with-lease exception line")
	}
	if !containsStr(prompt, "push --delete") {
		t.Error("hard rules missing branch-delete exception line")
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
