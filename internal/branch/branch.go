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

// Fetch updates remote-tracking refs and prunes deleted ones so the
// remote-tracking view is accurate before analysis. Failure is returned to the
// caller, which may choose to proceed with cached refs.
func Fetch(localPath string) error {
	c := exec.Command("git", "fetch", "--prune", "origin")
	c.Dir = localPath
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch --prune origin: %w\n%s", err, out)
	}
	return nil
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
