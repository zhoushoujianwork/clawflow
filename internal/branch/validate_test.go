package branch

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// newClonePair builds a bare "remote" with a single `main` commit plus a clone
// of it, and returns the clone path. Mirrors the setup style of
// TestListMergedIntegration.
func newClonePair(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	clone := filepath.Join(root, "clone")

	run := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run(root, "init", "--bare", "--initial-branch=main", remote)
	run(root, "init", "--initial-branch=main", work)
	run(work, "config", "user.email", "t@example.com")
	run(work, "config", "user.name", "t")
	run(work, "commit", "--allow-empty", "-m", "init")
	run(work, "remote", "add", "origin", remote)
	run(work, "push", "-u", "origin", "main")
	run(root, "clone", remote, clone)
	return clone
}

func TestValidateBase_ExistingBranch(t *testing.T) {
	clone := newClonePair(t)
	v := ValidateBase(clone, "main")
	if !v.LocalRefExists {
		t.Fatalf("expected origin/main to resolve locally: %+v", v)
	}
	if !v.Valid() {
		t.Errorf("main should be valid: %+v", v)
	}
	if v.Hint() != "" {
		t.Errorf("valid base should produce no hint, got %q", v.Hint())
	}
}

// The issue #300 case: base_branch was literally "origin".
func TestValidateBase_MisconfiguredBranch(t *testing.T) {
	clone := newClonePair(t)
	v := ValidateBase(clone, "origin")
	if v.LocalRefExists || v.RemoteRefExists {
		t.Fatalf("origin should not resolve as a branch: %+v", v)
	}
	if !v.RemoteChecked {
		t.Fatalf("local remote should have been probed: %+v", v)
	}
	if v.Valid() {
		t.Errorf("base_branch \"origin\" must be reported invalid: %+v", v)
	}
	if hint := v.Hint(); hint == "" {
		t.Error("invalid base should produce a remediation hint")
	}
}

func TestValidateBase_EmptyLocalPathIsUnproven(t *testing.T) {
	v := ValidateBase("", "whatever")
	if !v.Valid() {
		t.Errorf("no local clone means nothing to validate: %+v", v)
	}
}

func TestValidateBase_UnreachableRemoteIsUnproven(t *testing.T) {
	dir := t.TempDir() // not a git repo at all
	v := ValidateBase(dir, "main")
	if v.RemoteChecked {
		t.Fatalf("probe should have failed: %+v", v)
	}
	if !v.Valid() {
		t.Errorf("unprovable base must not be flagged invalid: %+v", v)
	}
}

func TestValidateBase_DefaultsToMain(t *testing.T) {
	clone := newClonePair(t)
	if v := ValidateBase(clone, ""); v.Base != "main" || !v.Valid() {
		t.Errorf("empty base should default to main: %+v", v)
	}
}

func TestRemoteDefaultBranch(t *testing.T) {
	clone := newClonePair(t)
	if got := RemoteDefaultBranch(clone); got != "main" {
		t.Errorf("RemoteDefaultBranch = %q, want main", got)
	}
	if got := RemoteDefaultBranch(t.TempDir()); got != "" {
		t.Errorf("non-git dir should yield empty, got %q", got)
	}
}
