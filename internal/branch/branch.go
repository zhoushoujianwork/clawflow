// Package branch provides read-only enumeration and merge-status analysis of
// local and remote-tracking git branches, plus helpers to delete them. It is
// intentionally side-effect-light: ListMerged only reads, and the delete
// helpers each touch a single branch so callers (CLI, future Pilot duty) can
// drive confirmation/preview around them.
//
// Merge detection is done entirely against the local clone via
// `git for-each-ref --merged=<commit>`, which uses ancestry. This is exact for
// normal merge commits. NOTE: squash-merged or rebase-merged branches do not
// share ancestry with the base tip, so they are NOT reported as merged here —
// detecting those requires cross-referencing the platform's merged-PR list and
// is left to a later increment.
package branch

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Branch describes a single branch and its merge/age status.
type Branch struct {
	Name       string    // short name without any "origin/" prefix, e.g. "fix/issue-226"
	Remote     bool      // true if this is a remote-tracking branch (origin/*)
	LastCommit time.Time // committer date of the branch tip (zero if unknown)
	Merged     bool      // merged into the base branch (by ancestry)
}

// Scope renders "remote" or "local" for display.
func (b Branch) Scope() string {
	if b.Remote {
		return "remote"
	}
	return "local"
}

// protectedNames are branch names that are never eligible for deletion,
// regardless of merge status.
var protectedNames = map[string]bool{
	"main":    true,
	"master":  true,
	"develop": true,
	"HEAD":    true,
}

// IsProtected reports whether a (short) branch name must never be deleted.
// The configured base branch is always protected.
func IsProtected(name, base string) bool {
	if name == "" || name == base || name == "origin" {
		return true
	}
	return protectedNames[name]
}

// refFormat asks `git for-each-ref` for "<short-name>\x00<committerdate-unix>".
// A NUL separator avoids ambiguity with branch names containing spaces/slashes.
const refFormat = "%(refname:short)%00%(committerdate:unix)"

// parseRefLines parses the NUL-delimited output of `git for-each-ref` using
// refFormat. Any "origin/" prefix is stripped from the name. It is exported
// indirectly via ListMerged but kept package-private and unit-tested directly.
func parseRefLines(out string, remote bool) []Branch {
	var branches []Branch
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 2)
		name := strings.TrimSpace(parts[0])
		if name == "" {
			continue
		}
		var ts time.Time
		if len(parts) == 2 {
			if unix, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64); err == nil && unix > 0 {
				ts = time.Unix(unix, 0)
			}
		}
		name = strings.TrimPrefix(name, "origin/")
		branches = append(branches, Branch{Name: name, Remote: remote, LastCommit: ts})
	}
	return branches
}

func gitOut(dir string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// headlessEnv returns the process environment plus the guards that keep git
// from blocking on interactive prompts when invoked without a TTY (background
// fetch, dashboard-triggered pull/push). GIT_TERMINAL_PROMPT=0 blocks HTTP
// credential prompts; the SSH BatchMode/ConnectTimeout options block key
// passphrase prompts and bound the TCP connect for git-over-SSH. Without these
// a background fetch can hang indefinitely on a credential prompt — the same
// class of failure as issue #216. (clone.cloneRepo already sets these; the
// background sync path must match.)
func headlessEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=10",
	)
}

// Fetch updates remote-tracking refs and prunes deleted ones so the
// remote-tracking view is accurate before analysis. Failure is returned to the
// caller, which may choose to proceed with cached refs.
func Fetch(localPath string) error {
	c := exec.Command("git", "fetch", "--prune", "origin")
	c.Dir = localPath
	c.Env = headlessEnv()
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch --prune origin: %w\n%s", err, out)
	}
	return nil
}

// SyncStatus describes the configured base branch's position relative to its
// remote-tracking counterpart (origin/<base>). Computed against the local
// clone; callers should run Fetch first so origin/<base> reflects the remote
// tip. All counts are zero and HasUpstream is false when origin/<base> is
// missing (e.g. a brand-new branch never pushed).
type SyncStatus struct {
	Branch      string `json:"branch"`       // the base branch name compared
	Ahead       int    `json:"ahead"`        // local commits not on origin → push needed
	Behind      int    `json:"behind"`       // origin commits not local → pull needed
	Dirty       bool   `json:"dirty"`        // worktree has uncommitted changes
	HasUpstream bool   `json:"has_upstream"` // origin/<base> ref exists
	Current     string `json:"current"`      // currently checked-out branch (may differ from base)
}

// GetSyncStatus computes the ahead/behind/dirty state of base relative to
// origin/<base> for the clone at localPath. It does NOT fetch — run Fetch
// first for an up-to-date comparison. base defaults to "main" when empty.
func GetSyncStatus(localPath, base string) (SyncStatus, error) {
	if base == "" {
		base = "main"
	}
	st := SyncStatus{Branch: base}

	// Currently checked-out branch (best-effort; detached HEAD yields "HEAD").
	if cur, err := gitOut(localPath, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		st.Current = strings.TrimSpace(cur)
	}

	// Dirty = any staged/unstaged/untracked change.
	if out, err := gitOut(localPath, "status", "--porcelain"); err == nil {
		st.Dirty = strings.TrimSpace(out) != ""
	}

	// Without origin/<base> there is nothing to compare against.
	if _, err := gitOut(localPath, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+base); err != nil {
		st.HasUpstream = false
		return st, nil
	}
	st.HasUpstream = true

	// `--left-right --count A...B` prints "<left>\t<right>": commits reachable
	// from A but not B, then from B but not A. With A=origin/base, B=base:
	// left = behind (remote ahead of us), right = ahead (we are ahead).
	out, err := gitOut(localPath, "rev-list", "--left-right", "--count", "origin/"+base+"..."+base)
	if err != nil {
		return st, err
	}
	fields := strings.Fields(out)
	if len(fields) == 2 {
		st.Behind, _ = strconv.Atoi(fields[0])
		st.Ahead, _ = strconv.Atoi(fields[1])
	}
	return st, nil
}

// runGitCombined runs a git command capturing combined stdout+stderr with the
// headless env, returning the trimmed output and any error. The output is
// surfaced to the user verbatim on failure so they can resolve it locally.
func runGitCombined(localPath string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = localPath
	c.Env = headlessEnv()
	out, err := c.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, text)
		}
		return text, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return text, nil
}

// Pull fast-forwards the base branch from origin. It refuses to run unless the
// clone is currently on base (a non-ff or wrong-branch pull could move an
// unrelated feature branch) and uses --ff-only so it never creates a merge
// commit or attempts conflict resolution — matching the issue's "show the
// error, let the user handle it locally" contract. Returns git's combined
// output for display.
func Pull(localPath, base string) (string, error) {
	if base == "" {
		base = "main"
	}
	cur, err := gitOut(localPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil && strings.TrimSpace(cur) != base {
		return "", fmt.Errorf("本地仓库当前在分支 %q，pull 仅在 %q 分支上执行；请先切换分支或在本地手动处理", strings.TrimSpace(cur), base)
	}
	return runGitCombined(localPath, "pull", "--ff-only", "origin", base)
}

// Push pushes the local base branch to origin/<base>. Pushing the ref
// explicitly (origin <base>) works regardless of the currently checked-out
// branch. A rejected push (e.g. local is behind) surfaces as an error with
// git's message for the user to resolve.
func Push(localPath, base string) (string, error) {
	if base == "" {
		base = "main"
	}
	return runGitCombined(localPath, "push", "origin", base)
}

// ListMerged returns local (and, when includeRemote is set, remote-tracking)
// branches that are merged into base. The base branch itself, HEAD, and
// protected branches are excluded. Results are sorted by LastCommit ascending
// (oldest first) so the most stale, safest-to-delete branches surface first.
func ListMerged(localPath, base string, includeRemote bool) ([]Branch, error) {
	if base == "" {
		base = "main"
	}

	var result []Branch

	localOut, err := gitOut(localPath, "for-each-ref", "--merged="+base, "--format="+refFormat, "refs/heads/")
	if err != nil {
		return nil, err
	}
	for _, b := range parseRefLines(localOut, false) {
		if IsProtected(b.Name, base) {
			continue
		}
		b.Merged = true
		result = append(result, b)
	}

	if includeRemote {
		remoteOut, err := gitOut(localPath, "for-each-ref", "--merged=origin/"+base, "--format="+refFormat, "refs/remotes/origin/")
		if err != nil {
			return nil, err
		}
		for _, b := range parseRefLines(remoteOut, true) {
			if IsProtected(b.Name, base) {
				continue
			}
			b.Merged = true
			result = append(result, b)
		}
	}

	sort.SliceStable(result, func(i, j int) bool {
		return result[i].LastCommit.Before(result[j].LastCommit)
	})
	return result, nil
}

// DeleteLocal removes a local branch. When force is false it uses `git branch
// -d` (refuses unmerged branches); force switches to `-D`. Deleting the
// currently checked-out branch or one held by a worktree fails with git's own
// error, which is returned verbatim.
func DeleteLocal(localPath, name string, force bool) error {
	flag := "-d"
	if force {
		flag = "-D"
	}
	c := exec.Command("git", "branch", flag, name)
	c.Dir = localPath
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("git branch %s %s: %w\n%s", flag, name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
