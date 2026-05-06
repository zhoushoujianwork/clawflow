package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateAndGet(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

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

	// context.md should exist
	ctxPath := ContextPath("test-proj")
	if _, err := os.Stat(ctxPath); err != nil {
		t.Errorf("context.md not created: %v", err)
	}

	got, err := Get("test-proj")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "test-proj" {
		t.Errorf("Get Name = %q, want %q", got.Name, "test-proj")
	}
}

func TestCreateDuplicate(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if _, err := Create("dup"); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := Create("dup"); err == nil {
		t.Fatal("second Create should fail")
	}
}

func TestAddRemoveRepo(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if _, err := Create("proj"); err != nil {
		t.Fatalf("Create: %v", err)
	}
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
	if len(p.Repos) != 1 {
		t.Fatalf("Repos len = %d after remove, want 1", len(p.Repos))
	}
}

func TestOneProjectConstraint(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	Create("alpha")
	Create("beta")
	AddRepo("alpha", "owner/shared")

	if err := AddRepo("beta", "owner/shared"); err == nil {
		t.Fatal("should reject repo already in another project")
	}
}

func TestFindProjectByRepo(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	Create("myproj")
	AddRepo("myproj", "owner/api")

	found, err := FindProjectByRepo("owner/api")
	if err != nil {
		t.Fatalf("FindProjectByRepo: %v", err)
	}
	if found == nil || found.Name != "myproj" {
		t.Fatalf("expected myproj, got %v", found)
	}

	found, err = FindProjectByRepo("owner/unknown")
	if err != nil {
		t.Fatalf("FindProjectByRepo unknown: %v", err)
	}
	if found != nil {
		t.Fatalf("expected nil for unknown repo, got %v", found)
	}
}

func TestList(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	Create("zebra")
	Create("alpha")

	projects, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("List len = %d, want 2", len(projects))
	}
	if projects[0].Name != "alpha" {
		t.Errorf("first project = %q, want alpha", projects[0].Name)
	}
}

func TestDelete(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	Create("doomed")
	if err := Delete("doomed"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	dir := filepath.Join(ProjectsRoot(), "doomed")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("project dir should be gone, stat err = %v", err)
	}
}

func TestReadWriteDeployment(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	Create("deploy-test")

	// deployment.md does not exist yet — should return "" without error
	content, err := ReadDeployment("deploy-test")
	if err != nil {
		t.Fatalf("ReadDeployment on missing file: %v", err)
	}
	if content != "" {
		t.Errorf("ReadDeployment on missing file = %q, want empty", content)
	}

	// write and read back
	body := "## Logs\n\nssh prod 'journalctl -u app -n 200'\n"
	if err := WriteDeployment("deploy-test", body); err != nil {
		t.Fatalf("WriteDeployment: %v", err)
	}
	got, err := ReadDeployment("deploy-test")
	if err != nil {
		t.Fatalf("ReadDeployment after write: %v", err)
	}
	if got != body {
		t.Errorf("ReadDeployment = %q, want %q", got, body)
	}
}

func TestReadWriteContext(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	Create("ctx-test")
	if err := WriteContext("ctx-test", "# My Project\nOverview here."); err != nil {
		t.Fatalf("WriteContext: %v", err)
	}
	content, err := ReadContext("ctx-test")
	if err != nil {
		t.Fatalf("ReadContext: %v", err)
	}
	if content != "# My Project\nOverview here." {
		t.Errorf("ReadContext = %q", content)
	}
}
