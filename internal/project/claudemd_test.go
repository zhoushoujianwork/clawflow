package project

import (
	"os"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func TestRenderClaudeMD_NoRepos(t *testing.T) {
	p := &Project{Name: "empty-proj"}
	got := renderClaudeMD(p, nil)

	if !strings.Contains(got, "# Project: empty-proj") {
		t.Error("missing project title")
	}
	if !strings.Contains(got, "no repos in this project yet") {
		t.Error("missing empty-repos fallback")
	}
	if !strings.Contains(got, "context.md") || !strings.Contains(got, "goals.md") {
		t.Error("missing working-files section")
	}
	if !strings.Contains(got, "managed by clawflow") {
		t.Error("missing managed marker")
	}
}

func TestRenderClaudeMD_WithLocalPath(t *testing.T) {
	p := &Project{Name: "proj", Repos: []string{"owner/api", "owner/web", "owner/no-clone"}}
	cfg := &config.Config{
		Repos: map[string]config.Repo{
			"owner/api":      {LocalPath: "/tmp/api"},
			"owner/web":      {LocalPath: "/tmp/web"},
			"owner/no-clone": {LocalPath: ""},
		},
	}
	got := renderClaudeMD(p, cfg)

	if !strings.Contains(got, "owner/api") || !strings.Contains(got, "/tmp/api") {
		t.Error("missing repo with local path")
	}
	if !strings.Contains(got, "owner/web") || !strings.Contains(got, "/tmp/web") {
		t.Error("missing second repo with local path")
	}
	if !strings.Contains(got, "owner/no-clone") || !strings.Contains(got, "no local clone") {
		t.Error("missing repo without local clone, expected fallback note")
	}
}

func TestRefreshClaudeMD_WritesFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if _, err := Create("proj"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Create() already calls RefreshClaudeMD; verify the file exists.
	path := ClaudeMDPath("proj")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("CLAUDE.md not written: %v", err)
	}
	if !strings.Contains(string(data), "# Project: proj") {
		t.Errorf("CLAUDE.md missing project title; got:\n%s", data)
	}

	// Calling Refresh again with no changes should be a no-op (idempotent).
	statBefore, _ := os.Stat(path)
	if err := RefreshClaudeMD("proj"); err != nil {
		t.Fatalf("RefreshClaudeMD: %v", err)
	}
	statAfter, _ := os.Stat(path)
	if statBefore.ModTime() != statAfter.ModTime() {
		t.Errorf("RefreshClaudeMD rewrote file with unchanged content (mtime changed)")
	}
}
