package api

import (
	"os/exec"
	"path/filepath"
	"strings"
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

// TestParseTabRefLines_SkipsRemoteHEAD guards issue #306: refs/remotes/origin/HEAD
// must never surface as a selectable branch. Its %(refname:short) form is the
// bare string "origin", which used to reach the dropdown and, once clicked, wrote
// base_branch: origin and broke every fetch for that repo.
func TestParseTabRefLines_SkipsRemoteHEAD(t *testing.T) {
	out := "refs/remotes/origin/HEAD\t1700000000\n" +
		"refs/remotes/origin/main\t1700000001\n" +
		"refs/remotes/origin/feature/x\t1700000002\n"
	got := parseTabRefLines(out, true)
	want := []string{"main", "feature/x"}
	if len(got) != len(want) {
		t.Fatalf("parseTabRefLines = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseTabRefLines = %v, want %v", got, want)
		}
	}
	for _, n := range got {
		if n == "origin" {
			t.Error("ghost \"origin\" entry leaked into branch list")
		}
	}
}

func TestParseTabRefLines_LocalStripsRefsHeads(t *testing.T) {
	out := "refs/heads/main\t1700000000\nrefs/heads/fix/issue-306\t1700000001\n"
	got := parseTabRefLines(out, false)
	if len(got) != 2 || got[0] != "main" || got[1] != "fix/issue-306" {
		t.Fatalf("parseTabRefLines = %v, want [main fix/issue-306]", got)
	}
}

// TestGitForEachRef_RemoteHEADNotListed runs the real git plumbing: a clone with
// origin/HEAD set must yield no "origin" entry.
func TestGitForEachRef_RemoteHEADNotListed(t *testing.T) {
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
	run(clone, "remote", "set-head", "origin", "main")

	out, err := gitForEachRef(clone, "refs/remotes/origin/")
	if err != nil {
		t.Fatalf("gitForEachRef: %v", err)
	}
	if !strings.Contains(out, "refs/remotes/origin/HEAD") {
		t.Skip("origin/HEAD not present in this git version's clone layout")
	}
	for _, name := range parseTabRefLines(out, true) {
		if name == "origin" {
			t.Fatalf("ghost \"origin\" entry present in %q", out)
		}
	}
}

func TestBaseBranchFromCfg_NilConfig(t *testing.T) {
	got := baseBranchFromCfg(nil, "any/repo")
	if got != "main" {
		t.Errorf("baseBranchFromCfg(nil) = %q, want \"main\"", got)
	}
}
