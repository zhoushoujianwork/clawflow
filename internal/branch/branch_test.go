package branch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRefLines(t *testing.T) {
	cases := []struct {
		name     string
		out      string
		remote   bool
		wantLen  int
		wantName string // expected name of first parsed branch
		wantTS   bool   // whether first branch has a non-zero timestamp
	}{
		{
			name:     "local with timestamp",
			out:      "fix/issue-226\x001700000000\n",
			remote:   false,
			wantLen:  1,
			wantName: "fix/issue-226",
			wantTS:   true,
		},
		{
			name:     "remote prefix stripped",
			out:      "origin/fix/issue-221\x001699999999\n",
			remote:   true,
			wantLen:  1,
			wantName: "fix/issue-221",
			wantTS:   true,
		},
		{
			name:     "missing timestamp ok",
			out:      "feature-x\x00\n",
			remote:   false,
			wantLen:  1,
			wantName: "feature-x",
			wantTS:   false,
		},
		{
			name:     "blank and whitespace lines skipped",
			out:      "\n   \nfix/a\x001\n\n",
			remote:   false,
			wantLen:  1,
			wantName: "fix/a",
			wantTS:   true,
		},
		{
			name:    "empty output",
			out:     "",
			remote:  false,
			wantLen: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRefLines(tc.out, tc.remote)
			if len(got) != tc.wantLen {
				t.Fatalf("got %d branches, want %d (%v)", len(got), tc.wantLen, got)
			}
			if tc.wantLen == 0 {
				return
			}
			if tc.wantName != "" && got[0].Name != tc.wantName {
				t.Errorf("name = %q, want %q", got[0].Name, tc.wantName)
			}
			if got[0].Remote != tc.remote {
				t.Errorf("remote = %v, want %v", got[0].Remote, tc.remote)
			}
			if hasTS := !got[0].LastCommit.IsZero(); hasTS != tc.wantTS {
				t.Errorf("hasTimestamp = %v, want %v", hasTS, tc.wantTS)
			}
		})
	}
}

func TestIsProtected(t *testing.T) {
	cases := []struct {
		name string
		base string
		want bool
	}{
		{"main", "main", true},
		{"master", "main", true},
		{"develop", "main", true},
		{"HEAD", "main", true},
		{"", "main", true},
		{"origin", "main", true},
		{"main", "develop", true},    // base differs but main still protected
		{"develop", "develop", true}, // base itself protected
		{"fix/issue-226", "main", false},
		{"feature-x", "main", false},
	}
	for _, tc := range cases {
		if got := IsProtected(tc.name, tc.base); got != tc.want {
			t.Errorf("IsProtected(%q, %q) = %v, want %v", tc.name, tc.base, got, tc.want)
		}
	}
}

// TestListMergedIntegration builds a throwaway git repo, merges one branch and
// leaves another unmerged, and asserts ListMerged reports only the merged one
// (and never the base branch). git is available in CI per project SOP.
func TestListMergedIntegration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()

	run := func(args ...string) {
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
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	run("init", "-b", "main")
	write("a.txt", "1")
	run("add", ".")
	run("commit", "-m", "init")

	// merged-branch: commit then merge back into main (creates ancestry).
	run("checkout", "-b", "merged-branch")
	write("b.txt", "2")
	run("add", ".")
	run("commit", "-m", "feature")
	run("checkout", "main")
	run("merge", "--no-ff", "-m", "merge feature", "merged-branch")

	// unmerged-branch: a commit that never lands on main.
	run("checkout", "-b", "unmerged-branch")
	write("c.txt", "3")
	run("add", ".")
	run("commit", "-m", "wip")
	run("checkout", "main")

	got, err := ListMerged(dir, "main", false)
	if err != nil {
		t.Fatalf("ListMerged: %v", err)
	}

	names := map[string]bool{}
	for _, b := range got {
		names[b.Name] = true
		if b.Remote {
			t.Errorf("unexpected remote branch %q for local-only list", b.Name)
		}
		if b.LastCommit.IsZero() {
			t.Errorf("branch %q missing LastCommit", b.Name)
		}
	}
	if !names["merged-branch"] {
		t.Errorf("expected merged-branch to be reported, got %v", names)
	}
	if names["unmerged-branch"] {
		t.Errorf("unmerged-branch should not be reported")
	}
	if names["main"] {
		t.Errorf("base branch main must never be reported")
	}
}

// gitRunner returns a helper that runs git in dir with a deterministic identity.
func gitRunner(t *testing.T, dir string) func(args ...string) {
	t.Helper()
	return func(args ...string) {
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
}

// TestListMergedLaggingLocalBase reproduces issue #302: the branch was merged
// upstream (it is an ancestor of origin/main) but the local main has not been
// pulled yet, so `--merged=main` cannot see it. ListMerged must judge against
// origin/main and still report the branch.
func TestListMergedLaggingLocalBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	clone := filepath.Join(root, "clone")

	bare := gitRunner(t, root)
	bare("init", "--bare", "-b", "main", origin)

	run := gitRunner(t, clone)
	bare("clone", origin, clone)

	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(clone, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	run("checkout", "-b", "main")
	write("a.txt", "1")
	run("add", ".")
	run("commit", "-m", "init")
	run("push", "-u", "origin", "main")

	// merged-upstream: merged into main and pushed, so origin/main contains it.
	run("checkout", "-b", "merged-upstream")
	write("b.txt", "2")
	run("add", ".")
	run("commit", "-m", "feature")
	run("checkout", "main")
	run("merge", "--no-ff", "-m", "merge feature", "merged-upstream")
	run("push", "origin", "main")

	// Rewind local main so it lags origin/main by the merge commit — exactly
	// the state ClawFlow clones drift into (auto_merge merges server-side).
	run("reset", "--hard", "HEAD~1")

	// Sanity: the local base genuinely cannot see the branch anymore.
	if out, err := gitOut(clone, "for-each-ref", "--merged=main", "--format=%(refname:short)", "refs/heads/"); err != nil {
		t.Fatalf("for-each-ref: %v", err)
	} else if strings.Contains(out, "merged-upstream") {
		t.Fatalf("fixture invalid: local main still contains merged-upstream:\n%s", out)
	}

	if ref := MergeBaseRef(clone, "main"); ref != "origin/main" {
		t.Errorf("MergeBaseRef = %q, want origin/main", ref)
	}

	got, err := ListMerged(clone, "main", false)
	if err != nil {
		t.Fatalf("ListMerged: %v", err)
	}
	found := false
	for _, b := range got {
		if b.Name == "merged-upstream" {
			found = true
		}
		if b.Name == "main" {
			t.Errorf("base branch main must never be reported")
		}
	}
	if !found {
		t.Errorf("expected merged-upstream to be reported (merged into origin/main), got %v", got)
	}

	// The lag must be reportable so the CLI can print its note.
	st, err := GetSyncStatus(clone, "main")
	if err != nil {
		t.Fatalf("GetSyncStatus: %v", err)
	}
	if !st.HasUpstream || st.Behind == 0 {
		t.Errorf("expected local main behind origin/main, got %+v", st)
	}
}

// TestMergeBaseRefFallback locks the offline path: with no origin/<base> ref,
// merge status must fall back to the local base instead of erroring.
func TestMergeBaseRefFallback(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	run := gitRunner(t, dir)
	run("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("1"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", ".")
	run("commit", "-m", "init")

	if ref := MergeBaseRef(dir, "main"); ref != "main" {
		t.Errorf("MergeBaseRef = %q, want main (no origin/main present)", ref)
	}
	if _, err := ListMerged(dir, "main", false); err != nil {
		t.Errorf("ListMerged should succeed without origin/main: %v", err)
	}
}
