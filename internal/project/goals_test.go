package project

import (
	"os/exec"
	"strings"
	"testing"
)

func TestReadWriteGoals_Roundtrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if _, err := Create("goals-test"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Empty file is created on Create.
	got, err := ReadGoals("goals-test")
	if err != nil {
		t.Fatalf("ReadGoals on empty: %v", err)
	}
	if got != "" {
		t.Errorf("ReadGoals on fresh project = %q, want empty", got)
	}

	body := "# Goals\n\n- ship v1\n- no flaky tests\n"
	if err := WriteGoals("goals-test", body); err != nil {
		t.Fatalf("WriteGoals: %v", err)
	}

	got, err = ReadGoals("goals-test")
	if err != nil {
		t.Fatalf("ReadGoals after write: %v", err)
	}
	if got != body {
		t.Errorf("ReadGoals = %q, want %q", got, body)
	}
}

func TestWriteGoals_AutoCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH — skipping commit verification")
	}

	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if _, err := Create("goals-commit"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := WriteGoals("goals-commit", "# Goals\n\nfirst draft"); err != nil {
		t.Fatalf("WriteGoals: %v", err)
	}

	out, err := runGitOutput(ProjectDir("goals-commit"), "log", "--oneline")
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if !strings.Contains(out, "update goals.md") {
		t.Errorf("git log missing 'update goals.md' commit:\n%s", out)
	}
}

func TestReadGoalsMissingProject(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	got, err := ReadGoals("never-created")
	if err != nil {
		t.Fatalf("ReadGoals on missing project: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}
