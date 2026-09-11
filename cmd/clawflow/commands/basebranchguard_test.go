package commands

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// newBaseBranchClone builds a bare remote with a single `main` branch plus a
// clone of it, and returns the clone path.
func newBaseBranchClone(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	clone := filepath.Join(root, "clone")

	git := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(root, "init", "--bare", "--initial-branch=main", remote)
	git(root, "init", "--initial-branch=main", work)
	git(work, "config", "user.email", "t@example.com")
	git(work, "config", "user.name", "t")
	git(work, "commit", "--allow-empty", "-m", "init")
	git(work, "remote", "add", "origin", remote)
	git(work, "push", "-u", "origin", "main")
	git(root, "clone", remote, clone)
	return clone
}

// The issue #315 case: base_branch "origin" is proven invalid, so dispatch
// must be suppressed instead of feeding every operator a guaranteed exit-128.
func TestCheckBaseBranch_ProvenInvalidBlocksDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // isolate the dedup file
	clone := newBaseBranchClone(t)

	v := checkBaseBranch(nil, "acme/repo", config.Repo{Enabled: true, LocalPath: clone, BaseBranch: "origin"})
	if !v.ProvenInvalid() {
		t.Fatalf("base_branch \"origin\" must be proven invalid: %+v", v)
	}
}

// #300's guarantee: an offline machine (or anything where ls-remote can't
// answer) is unproven, never blocked.
func TestCheckBaseBranch_UnprovenDoesNotBlock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir() // not a git repo — the probe cannot succeed

	v := checkBaseBranch(nil, "acme/repo", config.Repo{Enabled: true, LocalPath: dir, BaseBranch: "main"})
	if v.ProvenInvalid() {
		t.Errorf("unprovable base must not block dispatch: %+v", v)
	}
}

func TestCheckBaseBranch_ValidDoesNotBlock(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	clone := newBaseBranchClone(t)

	v := checkBaseBranch(nil, "acme/repo", config.Repo{Enabled: true, LocalPath: clone, BaseBranch: "main"})
	if v.ProvenInvalid() {
		t.Errorf("main resolves, must not block dispatch: %+v", v)
	}
}

// Repos with no local clone (or disabled) have nothing to validate against,
// so they must fall through untouched.
func TestCheckBaseBranch_NoLocalPathIsNeutral(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, r := range []config.Repo{
		{Enabled: true, LocalPath: "", BaseBranch: "origin"},
		{Enabled: false, LocalPath: "/nonexistent", BaseBranch: "origin"},
	} {
		if v := checkBaseBranch(nil, "acme/repo", r); v.ProvenInvalid() {
			t.Errorf("repo %+v should be neutral, got %+v", r, v)
		}
	}
}

// 99 identical WARN lines in one day (issue #315) came from the 2-minute
// auto-run cycle: every round is a fresh process, so dedup state has to
// survive process exit.
func TestBaseBranchNoticeDedup_SuppressesRepeatWithinInterval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	if !shouldLogBaseBranchNotice("acme/repo", "origin", now) {
		t.Fatal("first verdict must be logged")
	}
	if shouldLogBaseBranchNotice("acme/repo", "origin", now.Add(2*time.Minute)) {
		t.Error("same verdict 2 minutes later must be suppressed")
	}
	if shouldLogBaseBranchNotice("acme/repo", "origin", now.Add(30*time.Minute)) {
		t.Error("same verdict inside the interval must stay suppressed")
	}
	if !shouldLogBaseBranchNotice("acme/repo", "origin", now.Add(baseBranchNoticeInterval+time.Minute)) {
		t.Error("verdict must resurface once the interval elapses")
	}
}

func TestBaseBranchNoticeDedup_ChangedBaseLogsImmediately(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	if !shouldLogBaseBranchNotice("acme/repo", "origin", now) {
		t.Fatal("first verdict must be logged")
	}
	if !shouldLogBaseBranchNotice("acme/repo", "release", now.Add(time.Minute)) {
		t.Error("a different bad base is a state change and must be logged")
	}
}

func TestBaseBranchNoticeDedup_IsPerRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	if !shouldLogBaseBranchNotice("acme/one", "origin", now) {
		t.Fatal("first repo must be logged")
	}
	if !shouldLogBaseBranchNotice("acme/two", "origin", now) {
		t.Error("a second repo must not be deduped against the first")
	}
}

// Once the config is fixed the record is cleared, so a later regression is
// reported at once instead of waiting out a stale timestamp.
func TestBaseBranchNotice_ClearedOnRecovery(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()

	shouldLogBaseBranchNotice("acme/repo", "origin", now)
	clearBaseBranchNotice("acme/repo")
	if !shouldLogBaseBranchNotice("acme/repo", "origin", now.Add(time.Minute)) {
		t.Error("verdict after recovery must be logged immediately")
	}
}
