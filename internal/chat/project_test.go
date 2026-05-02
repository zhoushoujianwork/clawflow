package chat

import (
	"os"
	"path/filepath"
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

func TestProjectContextRoundTrip(t *testing.T) {
	// Use a temp dir as home to avoid touching the real ~/.clawflow
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "test-project"

	// Initially empty
	content, err := LoadProjectContext(name)
	if err != nil {
		t.Fatalf("LoadProjectContext: %v", err)
	}
	if content != "" {
		t.Fatalf("expected empty content, got %q", content)
	}

	// Save
	doc := "# Test Project\n\nHello world.\n"
	if err := SaveProjectContext(name, doc); err != nil {
		t.Fatalf("SaveProjectContext: %v", err)
	}

	// Load back
	got, err := LoadProjectContext(name)
	if err != nil {
		t.Fatalf("LoadProjectContext after save: %v", err)
	}
	if got != doc {
		t.Errorf("round-trip mismatch: got %q, want %q", got, doc)
	}

	// Verify file path
	p, _ := ProjectContextPath(name)
	wantPath := filepath.Join(tmpHome, ".clawflow", "projects", name, "context.md")
	if p != wantPath {
		t.Errorf("ProjectContextPath = %q, want %q", p, wantPath)
	}
}

func TestListProjects(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// No projects yet
	names, err := ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("expected 0 projects, got %d", len(names))
	}

	// Create two projects
	for _, n := range []string{"alpha", "beta"} {
		if err := SaveProjectContext(n, "# "+n+"\n"); err != nil {
			t.Fatalf("SaveProjectContext(%s): %v", n, err)
		}
	}

	// Create a dir without context.md — should not appear
	emptyDir := filepath.Join(tmpHome, ".clawflow", "projects", "empty")
	os.MkdirAll(emptyDir, 0o755)

	names, err = ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 projects, got %d: %v", len(names), names)
	}

	// Check both are present (order may vary by filesystem)
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["alpha"] || !found["beta"] {
		t.Errorf("expected alpha and beta, got %v", names)
	}
}

func TestBuildProjectChatContext(t *testing.T) {
	ctx := BuildProjectChatContext("my-proj", "# My Project\n\nSome content.\n")

	// Should contain the project name
	if got := ctx; !contains(got, "Project: my-proj") {
		t.Error("missing project name in context")
	}

	// Should contain the original content
	if !contains(ctx, "# My Project") {
		t.Error("missing original context.md content")
	}

	// Should contain the instruction about outputting context.md
	if !contains(ctx, "```context.md") {
		t.Error("missing instruction about context.md code block")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
