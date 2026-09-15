package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// newIssue329Repo builds a bare remote plus a local clone with a single
// commit on main, mirroring newBaseBranchClone's pattern but returning both
// the clone (used as repoCfg.LocalPath) and a helper to add worktrees.
func newIssue329Repo(t *testing.T) (localPath string) {
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
	git(clone, "fetch", "origin", "main")
	return clone
}

// TestDetectPartialWork_EmptyWorktree verifies a freshly detached worktree
// with no edits reports no partial work (issue #329's core symptom: claude
// exits before writing anything, leaving an empty shell).
func TestDetectPartialWork_EmptyWorktree(t *testing.T) {
	localPath := newIssue329Repo(t)
	wt := filepath.Join(localPath, "..", "wt-empty")
	if err := exec.Command("git", "-C", localPath, "worktree", "add", "--detach", wt, "origin/main").Run(); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	hasWork, _, _ := detectPartialWork(wt, "main")
	if hasWork {
		t.Errorf("expected no partial work in a fresh detached worktree")
	}
}

// TestDetectPartialWork_UntrackedFile verifies a worktree with an untracked
// file (i.e. claude wrote something before exiting) is correctly flagged as
// having partial work worth preserving.
func TestDetectPartialWork_UntrackedFile(t *testing.T) {
	localPath := newIssue329Repo(t)
	wt := filepath.Join(localPath, "..", "wt-untracked")
	if err := exec.Command("git", "-C", localPath, "worktree", "add", "--detach", wt, "origin/main").Run(); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt, "new.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	hasWork, stat, _ := detectPartialWork(wt, "main")
	if !hasWork {
		t.Errorf("expected partial work to be detected from an untracked file")
	}
	if stat == "" {
		t.Errorf("expected non-empty diff stat for detected partial work")
	}
}

// TestPruneEmptyIssueWorktrees verifies the reconcile-pass GC removes empty
// issue-* worktrees but leaves ones with partial work and ones currently
// locked by an in-flight run untouched (issue #329).
func TestPruneEmptyIssueWorktrees(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp) // pruneEmptyIssueWorktrees + snapshot.IsLocked use os.UserHomeDir

	localPath := newIssue329Repo(t)
	slug := "acme__repo"
	wtRoot := filepath.Join(tmp, ".clawflow", "worktrees", slug)
	if err := os.MkdirAll(wtRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	// Empty shell — should be pruned.
	emptyWt := filepath.Join(wtRoot, "issue-1-2026-01-01T00-00-00Z")
	if err := exec.Command("git", "-C", localPath, "worktree", "add", "--detach", emptyWt, "origin/main").Run(); err != nil {
		t.Fatalf("worktree add (empty): %v", err)
	}

	// Worktree with partial work — must survive.
	wipWt := filepath.Join(wtRoot, "issue-2-2026-01-01T00-00-00Z")
	if err := exec.Command("git", "-C", localPath, "worktree", "add", "--detach", wipWt, "origin/main").Run(); err != nil {
		t.Fatalf("worktree add (wip): %v", err)
	}
	if err := os.WriteFile(filepath.Join(wipWt, "wip.txt"), []byte("in progress"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Empty shell but locked by an in-flight run — must survive.
	lockedWt := filepath.Join(wtRoot, "issue-3-2026-01-01T00-00-00Z")
	if err := exec.Command("git", "-C", localPath, "worktree", "add", "--detach", lockedWt, "origin/main").Run(); err != nil {
		t.Fatalf("worktree add (locked): %v", err)
	}
	if err := snapshot.AcquireLock("acme/repo", 3, "implement"); err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	defer snapshot.ReleaseLock("acme/repo", 3)

	cfg := &config.Config{
		Repos: map[string]config.Repo{
			"acme/repo": {Enabled: true, LocalPath: localPath, BaseBranch: "main"},
		},
	}

	n := pruneEmptyIssueWorktrees(cfg)
	if n != 1 {
		t.Errorf("expected 1 worktree pruned, got %d", n)
	}
	if _, err := os.Stat(emptyWt); !os.IsNotExist(err) {
		t.Errorf("empty worktree was not pruned (stat err: %v)", err)
	}
	if _, err := os.Stat(wipWt); err != nil {
		t.Errorf("WIP worktree was incorrectly removed: %v", err)
	}
	if _, err := os.Stat(lockedWt); err != nil {
		t.Errorf("locked worktree was incorrectly removed: %v", err)
	}
}
