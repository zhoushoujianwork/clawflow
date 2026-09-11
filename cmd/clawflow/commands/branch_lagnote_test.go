package commands

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBaseLagNote covers the "silent zero" fix from issue #302: when local
// <base> lags origin/<base>, the cleanup commands must say so; when the clone
// is in sync (or has no upstream) the note must stay empty.
func TestBaseLagNote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	clone := filepath.Join(root, "clone")

	git := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(c.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	git(root, "init", "--bare", "--initial-branch=main", remote)
	git(root, "clone", remote, clone)
	git(clone, "checkout", "-b", "main")
	git(clone, "commit", "--allow-empty", "-m", "init")
	git(clone, "push", "-u", "origin", "main")

	// In sync → no note.
	if note := baseLagNote(clone, "main"); note != "" {
		t.Errorf("in-sync clone should produce no note, got %q", note)
	}

	// Push a second commit, then rewind local main so it lags by one.
	git(clone, "commit", "--allow-empty", "-m", "second")
	git(clone, "push", "origin", "main")
	git(clone, "reset", "--hard", "HEAD~1")

	note := baseLagNote(clone, "main")
	if note == "" {
		t.Fatal("lagging clone should produce a note")
	}
	for _, want := range []string{"behind origin/main", "1 commit(s)"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q missing %q", note, want)
		}
	}

	// No upstream ref for the queried base → no note (offline/fresh clone).
	if note := baseLagNote(clone, "does-not-exist"); note != "" {
		t.Errorf("missing upstream should produce no note, got %q", note)
	}
}
