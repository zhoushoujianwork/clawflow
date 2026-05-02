package project

import (
	"os"
	"path/filepath"
	"testing"
)

// withTempHome overrides HOME so tests write to a temp dir instead of
// the real ~/.clawflow/projects. Restores the original HOME on cleanup.
func withTempHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	orig := os.Getenv("HOME")
	t.Setenv("HOME", tmp)
	t.Cleanup(func() { os.Setenv("HOME", orig) })
	return tmp
}

func TestCreateAndGet(t *testing.T) {
	withTempHome(t)

	p, err := Create("test-proj")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.Name != "test-proj" {
		t.Errorf("Name = %q, want %q", p.Name, "test-proj")
	}
	if len(p.Repos) != 0 {
		t.Errorf("Repos = %v, want empty", p.Repos)
	}

	got, err := Get("test-proj")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "test-proj" {
		t.Errorf("Get Name = %q, want %q", got.Name, "test-proj")
	}

	// context.md should exist and be empty
	ctx, err := ReadContext("test-proj")
	if err != nil {
		t.Fatalf("ReadContext: %v", err)
	}
	if ctx != "" {
		t.Errorf("ReadContext = %q, want empty", ctx)
	}
}

func TestCreateDuplicate(t *testing.T) {
	withTempHome(t)

	if _, err := Create("dup"); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := Create("dup"); err == nil {
		t.Fatal("second Create should fail")
	}
}

func TestList(t *testing.T) {
	withTempHome(t)

	Create("beta")
	Create("alpha")

	projects, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("List len = %d, want 2", len(projects))
	}
	if projects[0].Name != "alpha" || projects[1].Name != "beta" {
		t.Errorf("List order = [%s, %s], want [alpha, beta]", projects[0].Name, projects[1].Name)
	}
}

func TestAddRemoveRepo(t *testing.T) {
	withTempHome(t)

	Create("proj")

	if err := AddRepo("proj", "owner/backend"); err != nil {
		t.Fatalf("AddRepo: %v", err)
	}
	if err := AddRepo("proj", "owner/frontend"); err != nil {
		t.Fatalf("AddRepo: %v", err)
	}

	p, _ := Get("proj")
	if len(p.Repos) != 2 {
		t.Fatalf("Repos len = %d, want 2", len(p.Repos))
	}

	// duplicate add should fail
	if err := AddRepo("proj", "owner/backend"); err == nil {
		t.Fatal("duplicate AddRepo should fail")
	}

	if err := RemoveRepo("proj", "owner/backend"); err != nil {
		t.Fatalf("RemoveRepo: %v", err)
	}
	p, _ = Get("proj")
	if len(p.Repos) != 1 || p.Repos[0] != "owner/frontend" {
		t.Errorf("after remove: Repos = %v", p.Repos)
	}

	// remove non-existent
	if err := RemoveRepo("proj", "owner/nope"); err == nil {
		t.Fatal("RemoveRepo non-existent should fail")
	}
}

func TestFindByRepo(t *testing.T) {
	withTempHome(t)

	Create("proj-a")
	AddRepo("proj-a", "owner/backend")
	Create("proj-b")
	AddRepo("proj-b", "owner/frontend")

	found, err := FindByRepo("owner/backend")
	if err != nil {
		t.Fatalf("FindByRepo: %v", err)
	}
	if found.Name != "proj-a" {
		t.Errorf("FindByRepo = %q, want proj-a", found.Name)
	}

	_, err = FindByRepo("owner/nope")
	if err == nil {
		t.Fatal("FindByRepo should fail for unknown repo")
	}
}

func TestOneProjectConstraint(t *testing.T) {
	withTempHome(t)

	Create("proj-a")
	AddRepo("proj-a", "owner/shared")
	Create("proj-b")

	err := AddRepo("proj-b", "owner/shared")
	if err == nil {
		t.Fatal("AddRepo should fail: repo already in another project")
	}
}

func TestDelete(t *testing.T) {
	withTempHome(t)

	Create("doomed")
	if err := Delete("doomed"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := Get("doomed"); err == nil {
		t.Fatal("Get after Delete should fail")
	}
}

func TestReadWriteContext(t *testing.T) {
	withTempHome(t)

	Create("ctx-proj")
	content := "# My Project\n\nThis is the overview."
	if err := WriteContext("ctx-proj", content); err != nil {
		t.Fatalf("WriteContext: %v", err)
	}
	got, err := ReadContext("ctx-proj")
	if err != nil {
		t.Fatalf("ReadContext: %v", err)
	}
	if got != content {
		t.Errorf("ReadContext = %q, want %q", got, content)
	}
}

func TestContextPathLocation(t *testing.T) {
	withTempHome(t)

	Create("loc-proj")
	p := ContextPath("loc-proj")
	if !filepath.IsAbs(p) {
		t.Errorf("ContextPath should be absolute, got %q", p)
	}
	if filepath.Base(p) != "context.md" {
		t.Errorf("ContextPath base = %q, want context.md", filepath.Base(p))
	}
}
