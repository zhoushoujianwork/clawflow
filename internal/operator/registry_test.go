package operator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func makeSkill(name, target, lock string, required, excluded []string) string {
	var b strings.Builder
	b.WriteString("---\nname: ")
	b.WriteString(name)
	b.WriteString("\noperator:\n  trigger:\n    target: ")
	b.WriteString(target)
	b.WriteString("\n    labels_required: [")
	b.WriteString(strings.Join(required, ","))
	b.WriteString("]\n    labels_excluded: [")
	b.WriteString(strings.Join(excluded, ","))
	b.WriteString("]\n  lock_label: ")
	b.WriteString(lock)
	b.WriteString("\n---\n\nprompt for ")
	b.WriteString(name)
	return b.String()
}

func TestRegistry_LoadEmbedded(t *testing.T) {
	sys := fstest.MapFS{
		"skills/alpha/SKILL.md": {Data: []byte(makeSkill("alpha", "issue", "running", []string{"bug"}, nil))},
		"skills/beta/SKILL.md":  {Data: []byte(makeSkill("beta", "pr", "reviewing", nil, nil))},
		"skills/README.md":      {Data: []byte("# not a skill; should be ignored")},
	}
	reg := NewRegistry()
	if err := reg.LoadEmbedded(sys, "skills"); err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	if _, ok := reg.Get("alpha"); !ok {
		t.Error("alpha should be registered")
	}
	if _, ok := reg.Get("beta"); !ok {
		t.Error("beta should be registered")
	}
	if len(reg.All()) != 2 {
		t.Errorf("expected 2 ops, got %d", len(reg.All()))
	}
	// Source should indicate embed origin
	alpha, _ := reg.Get("alpha")
	if !strings.HasPrefix(alpha.Source, "embed:") {
		t.Errorf("alpha.Source should start with 'embed:', got %q", alpha.Source)
	}
}

func TestRegistry_AllSortedByName(t *testing.T) {
	sys := fstest.MapFS{
		"skills/charlie/SKILL.md": {Data: []byte(makeSkill("charlie", "issue", "l", nil, nil))},
		"skills/alpha/SKILL.md":   {Data: []byte(makeSkill("alpha", "issue", "l", nil, nil))},
		"skills/bravo/SKILL.md":   {Data: []byte(makeSkill("bravo", "issue", "l", nil, nil))},
	}
	reg := NewRegistry()
	if err := reg.LoadEmbedded(sys, "skills"); err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	ops := reg.All()
	if len(ops) != 3 {
		t.Fatalf("want 3 ops got %d", len(ops))
	}
	names := []string{ops[0].Name, ops[1].Name, ops[2].Name}
	if names[0] != "alpha" || names[1] != "bravo" || names[2] != "charlie" {
		t.Errorf("All() not sorted: %v", names)
	}
}

func TestRegistry_UserOverridesEmbedded(t *testing.T) {
	sys := fstest.MapFS{
		"skills/evaluate-bug/SKILL.md": {Data: []byte(makeSkill("evaluate-bug", "issue", "running", []string{"bug"}, nil))},
	}
	reg := NewRegistry()
	if err := reg.LoadEmbedded(sys, "skills"); err != nil {
		t.Fatal(err)
	}

	// Create a user override with different trigger requirements.
	userDir := t.TempDir()
	opDir := filepath.Join(userDir, "evaluate-bug")
	if err := os.MkdirAll(opDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userSkill := makeSkill("evaluate-bug", "issue", "running", []string{"bug", "urgent"}, nil)
	if err := os.WriteFile(filepath.Join(opDir, "SKILL.md"), []byte(userSkill), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := reg.LoadUserDir(userDir); err != nil {
		t.Fatal(err)
	}

	got, ok := reg.Get("evaluate-bug")
	if !ok {
		t.Fatal("evaluate-bug missing")
	}
	if len(got.Trigger.LabelsRequired) != 2 || got.Trigger.LabelsRequired[0] != "bug" || got.Trigger.LabelsRequired[1] != "urgent" {
		t.Errorf("user version did not override embedded; required=%v", got.Trigger.LabelsRequired)
	}
	if strings.HasPrefix(got.Source, "embed:") {
		t.Errorf("Source should be file path after user override, got %q", got.Source)
	}
}

func TestRegistry_LoadUserDir_Missing(t *testing.T) {
	reg := NewRegistry()
	err := reg.LoadUserDir(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Errorf("missing user dir should be a no-op, got: %v", err)
	}
}

func TestRegistry_LoadUserDir_InvalidSkill(t *testing.T) {
	reg := NewRegistry()
	userDir := t.TempDir()
	opDir := filepath.Join(userDir, "broken")
	_ = os.MkdirAll(opDir, 0o755)
	if err := os.WriteFile(filepath.Join(opDir, "SKILL.md"), []byte("not yaml, no frontmatter"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := reg.LoadUserDir(userDir)
	if err == nil {
		t.Error("invalid user SKILL.md should return an error")
	}
}

func TestRegistry_Get(t *testing.T) {
	reg := NewRegistry()
	if _, ok := reg.Get("nonexistent"); ok {
		t.Error("Get on empty registry should return ok=false")
	}
}
