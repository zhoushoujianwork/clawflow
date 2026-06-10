package api

import (
	"os/exec"
	"testing"
)

// initTestRepo creates a minimal git repo with one commit so for-each-ref works.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmds := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	}
	for _, args := range cmds {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", args, out)
		}
	}
	return dir
}

func TestHeadBranch(t *testing.T) {
	dir := initTestRepo(t)
	got := headBranch(dir)
	if got != "main" {
		t.Errorf("headBranch = %q, want %q", got, "main")
	}
}

func TestGitForEachRef_LocalBranches(t *testing.T) {
	dir := initTestRepo(t)
	out, err := gitForEachRef(dir, "refs/heads/")
	if err != nil {
		t.Fatalf("gitForEachRef: %v", err)
	}
	if out == "" {
		t.Fatal("expected non-empty output for local refs")
	}
}

func TestGitForEachRef_RemoteBranches_Empty(t *testing.T) {
	dir := initTestRepo(t)
	// No remotes configured — should return empty output without error.
	_, err := gitForEachRef(dir, "refs/remotes/origin/")
	if err != nil {
		t.Fatalf("gitForEachRef remote: %v", err)
	}
}

func TestHeadBranch_AfterCheckout(t *testing.T) {
	dir := initTestRepo(t)
	c := exec.Command("git", "checkout", "-b", "feature/test")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("checkout: %s", out)
	}
	got := headBranch(dir)
	if got != "feature/test" {
		t.Errorf("headBranch = %q, want %q", got, "feature/test")
	}
}

func TestBaseBranchFromCfg_NilConfig(t *testing.T) {
	got := baseBranchFromCfg(nil, "any/repo")
	if got != "main" {
		t.Errorf("baseBranchFromCfg(nil) = %q, want \"main\"", got)
	}
}
