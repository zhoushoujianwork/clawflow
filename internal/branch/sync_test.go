package branch

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitEnv returns a deterministic git identity so commits succeed in CI.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = gitEnv()
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// makeRemoteAndClone builds a bare "origin" remote seeded with one commit on
// `main`, plus a working clone of it. Returns (clonePath, remotePath).
func makeRemoteAndClone(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	seed := filepath.Join(base, "seed")
	clonePath := filepath.Join(base, "clone")

	mustGit(t, base, "init", "--bare", "-b", "main", remote)

	// Seed the remote with an initial commit via a throwaway clone.
	mustGit(t, base, "clone", remote, seed)
	writeFile(t, seed, "a.txt", "1\n")
	mustGit(t, seed, "add", ".")
	mustGit(t, seed, "commit", "-m", "init")
	mustGit(t, seed, "push", "origin", "main")

	// The clone under test.
	mustGit(t, base, "clone", remote, clonePath)
	return clonePath, remote
}

func TestGetSyncStatus_Clean(t *testing.T) {
	clonePath, _ := makeRemoteAndClone(t)
	st, err := GetSyncStatus(clonePath, "main")
	if err != nil {
		t.Fatalf("GetSyncStatus: %v", err)
	}
	if !st.HasUpstream {
		t.Errorf("HasUpstream = false, want true")
	}
	if st.Ahead != 0 || st.Behind != 0 {
		t.Errorf("ahead=%d behind=%d, want 0/0", st.Ahead, st.Behind)
	}
	if st.Dirty {
		t.Errorf("Dirty = true, want false")
	}
	if st.Branch != "main" || st.Current != "main" {
		t.Errorf("branch=%q current=%q, want main/main", st.Branch, st.Current)
	}
}

func TestGetSyncStatus_AheadAndDirty(t *testing.T) {
	clonePath, _ := makeRemoteAndClone(t)
	// Local commit not pushed → ahead 1.
	writeFile(t, clonePath, "b.txt", "2\n")
	mustGit(t, clonePath, "add", ".")
	mustGit(t, clonePath, "commit", "-m", "local work")
	// Uncommitted change → dirty.
	writeFile(t, clonePath, "a.txt", "changed\n")

	st, err := GetSyncStatus(clonePath, "main")
	if err != nil {
		t.Fatalf("GetSyncStatus: %v", err)
	}
	if st.Ahead != 1 {
		t.Errorf("ahead = %d, want 1", st.Ahead)
	}
	if st.Behind != 0 {
		t.Errorf("behind = %d, want 0", st.Behind)
	}
	if !st.Dirty {
		t.Errorf("Dirty = false, want true")
	}
}

func TestGetSyncStatus_Behind(t *testing.T) {
	clonePath, remote := makeRemoteAndClone(t)
	// Push a new commit to the remote via an independent clone so our clone
	// under test falls behind by one.
	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, filepath.Dir(other), "clone", remote, other)
	writeFile(t, other, "c.txt", "3\n")
	mustGit(t, other, "add", ".")
	mustGit(t, other, "commit", "-m", "remote work")
	mustGit(t, other, "push", "origin", "main")

	if err := Fetch(clonePath); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	st, err := GetSyncStatus(clonePath, "main")
	if err != nil {
		t.Fatalf("GetSyncStatus: %v", err)
	}
	if st.Behind != 1 {
		t.Errorf("behind = %d, want 1", st.Behind)
	}
	if st.Ahead != 0 {
		t.Errorf("ahead = %d, want 0", st.Ahead)
	}
}

func TestGetSyncStatus_NoUpstream(t *testing.T) {
	clonePath, _ := makeRemoteAndClone(t)
	st, err := GetSyncStatus(clonePath, "nonexistent-branch")
	if err != nil {
		t.Fatalf("GetSyncStatus: %v", err)
	}
	if st.HasUpstream {
		t.Errorf("HasUpstream = true, want false for missing origin branch")
	}
	if st.Ahead != 0 || st.Behind != 0 {
		t.Errorf("ahead=%d behind=%d, want 0/0 when no upstream", st.Ahead, st.Behind)
	}
}

func TestPull_FastForward(t *testing.T) {
	clonePath, remote := makeRemoteAndClone(t)
	other := filepath.Join(t.TempDir(), "other")
	mustGit(t, filepath.Dir(other), "clone", remote, other)
	writeFile(t, other, "c.txt", "3\n")
	mustGit(t, other, "add", ".")
	mustGit(t, other, "commit", "-m", "remote work")
	mustGit(t, other, "push", "origin", "main")

	if _, err := Pull(clonePath, "main"); err != nil {
		t.Fatalf("Pull: %v", err)
	}
	st, err := GetSyncStatus(clonePath, "main")
	if err != nil {
		t.Fatalf("GetSyncStatus: %v", err)
	}
	if st.Behind != 0 {
		t.Errorf("after pull behind = %d, want 0", st.Behind)
	}
	if _, err := os.Stat(filepath.Join(clonePath, "c.txt")); err != nil {
		t.Errorf("pulled file c.txt missing: %v", err)
	}
}

func TestPull_WrongBranchRefused(t *testing.T) {
	clonePath, _ := makeRemoteAndClone(t)
	mustGit(t, clonePath, "checkout", "-b", "feature")
	if _, err := Pull(clonePath, "main"); err == nil {
		t.Errorf("Pull on non-base branch should be refused, got nil error")
	}
}

func TestPush_AheadSucceeds(t *testing.T) {
	clonePath, remote := makeRemoteAndClone(t)
	writeFile(t, clonePath, "b.txt", "2\n")
	mustGit(t, clonePath, "add", ".")
	mustGit(t, clonePath, "commit", "-m", "local work")

	if _, err := Push(clonePath, "main"); err != nil {
		t.Fatalf("Push: %v", err)
	}
	// Verify the remote advanced by checking ahead is back to 0 after fetch.
	if err := Fetch(clonePath); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	st, err := GetSyncStatus(clonePath, "main")
	if err != nil {
		t.Fatalf("GetSyncStatus: %v", err)
	}
	if st.Ahead != 0 {
		t.Errorf("after push ahead = %d, want 0", st.Ahead)
	}
	_ = remote
}
