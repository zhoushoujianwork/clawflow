package branch

import (
	"os"
	"os/exec"
	"path/filepath"
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
