package project

import (
	"os"
	"path/filepath"
	"strings"
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

func TestCreateCreatesDeploymentMd(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if _, err := Create("deploy-test"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(DeploymentPath("deploy-test")); err != nil {
		t.Errorf("deployment.md not created: %v", err)
	}
}

func TestReadWriteDeployment(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	Create("dep-rw")
	content, err := ReadDeployment("dep-rw")
	if err != nil {
		t.Fatalf("ReadDeployment on empty file: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty, got %q", content)
	}

	want := "# Deployment\n\n## Environments\n\nproduction: VPS"
	if err := WriteDeployment("dep-rw", want); err != nil {
		t.Fatalf("WriteDeployment: %v", err)
	}
	got, err := ReadDeployment("dep-rw")
	if err != nil {
		t.Fatalf("ReadDeployment: %v", err)
	}
	if got != want {
		t.Errorf("ReadDeployment = %q, want %q", got, want)
	}
}

func TestReadDeploymentMissingFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// No project created — file doesn't exist; should return "" not error.
	content, err := ReadDeployment("nonexistent")
	if err != nil {
		t.Fatalf("ReadDeployment on missing file: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty string, got %q", content)
	}
}

func TestHeaderForRepoWithDeployment(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	Create("hdr-proj")
	AddRepo("hdr-proj", "owner/repo")

	// Only deployment.md populated — header should still be emitted.
	WriteDeployment("hdr-proj", "# Deployment\n\nproduction: VPS")
	header := HeaderForRepo("owner/repo")
	if header == "" {
		t.Fatal("expected non-empty header when deployment.md is set")
	}
	if !strings.Contains(header, "## Deployment environment (deployment.md)") {
		t.Errorf("header missing deployment section:\n%s", header)
	}
	if !strings.Contains(header, "production: VPS") {
		t.Errorf("header missing deployment content:\n%s", header)
	}
}

func TestHeaderForRepoDeploymentAbsent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	Create("hdr-no-deploy")
	AddRepo("hdr-no-deploy", "owner/repo2")

	// Only context.md populated; deployment.md empty → no deployment section.
	WriteContext("hdr-no-deploy", "# Overview")
	header := HeaderForRepo("owner/repo2")
	if strings.Contains(header, "deployment") {
		t.Errorf("header should not contain deployment section when deployment.md is empty:\n%s", header)
	}
	if !strings.Contains(header, "## Project overview (context.md)") {
		t.Errorf("header missing context section:\n%s", header)
	}
}
