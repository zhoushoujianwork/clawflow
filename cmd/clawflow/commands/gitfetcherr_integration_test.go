package commands

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFetchRefNotFound_RealGit reproduces issue #300 end-to-end against a real
// local clone: `git fetch origin origin` when base_branch is misconfigured as
// "origin". It asserts the pipeline (runGitWithRetryOutput → classify →
// describe) turns git's actual stderr into a config-error message.
func TestFetchRefNotFound_RealGit(t *testing.T) {
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

	out, err := runGitWithRetryOutput(func() *exec.Cmd {
		return exec.Command("git", "-C", clone, "fetch", "origin", "origin")
	})
	if err == nil {
		t.Fatal("fetching a nonexistent ref should fail")
	}
	if classifyGitFetchError(out) != gitFetchRefNotFound {
		t.Fatalf("real git output not classified as ref-not-found: %q", out)
	}

	msg := describeGitFetchFailure("origin", clone, out, "main", err).Error()
	if !strings.Contains(msg, "base_branch is likely misconfigured") {
		t.Errorf("message should name base_branch: %s", msg)
	}
	if strings.Contains(msg, "without network access") {
		t.Errorf("must not blame the network: %s", msg)
	}

	// Sanity: the correct branch fetches fine over the same path/credentials.
	if _, err := runGitWithRetryOutput(func() *exec.Cmd {
		return exec.Command("git", "-C", clone, "fetch", "origin", "main")
	}); err != nil {
		t.Fatalf("fetching origin/main should succeed: %v", err)
	}
}
