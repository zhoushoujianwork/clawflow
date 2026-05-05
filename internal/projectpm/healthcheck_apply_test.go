package projectpm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/project"
)

// TestApplyProjectChange_Unchanged covers the regression that surfaced
// as a confusing red "failed" badge on the dashboard: when the proposed
// content is byte-identical to what's already on disk, apply should
// short-circuit to "unchanged" instead of writing + (for repo paths)
// hitting "git commit: nothing to commit". Project-level paths exercise
// the same read-then-compare logic without needing a git fixture.
func TestApplyProjectChange_Unchanged(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	const name = "test-proj"
	dir := filepath.Join(tmp, ".clawflow", "projects", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const body = "# already here\n"
	if err := os.WriteFile(filepath.Join(dir, "context.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &project.Project{Name: name}
	out := applyProjectChange(p, ProposedChange{
		Target:          "project",
		Path:            "context.md",
		Action:          "update",
		ProposedContent: body,
	})
	if out.Status != "unchanged" {
		t.Fatalf("status = %q, want unchanged (error=%q)", out.Status, out.Error)
	}
	if out.Error != "" {
		t.Fatalf("unexpected error: %q", out.Error)
	}
}

// TestApplyProjectChange_Written confirms the happy path: when the
// proposed content differs, apply writes it and reports "written".
func TestApplyProjectChange_Written(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	const name = "test-proj"
	dir := filepath.Join(tmp, ".clawflow", "projects", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "context.md"), []byte("# old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &project.Project{Name: name}
	out := applyProjectChange(p, ProposedChange{
		Target:          "project",
		Path:            "context.md",
		Action:          "update",
		ProposedContent: "# new\n",
	})
	if out.Status != "written" {
		t.Fatalf("status = %q, want written (error=%q)", out.Status, out.Error)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "context.md"))
	if string(got) != "# new\n" {
		t.Fatalf("file content = %q, want %q", got, "# new\n")
	}
}
