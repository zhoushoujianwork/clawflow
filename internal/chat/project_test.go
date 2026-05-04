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
	ctx := BuildProjectChatContext("my-proj", repos, "# My Project\n\nSome content.\n")

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

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
