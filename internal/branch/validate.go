package branch

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// lsRemoteTimeout bounds the single network probe ValidateBase may make. A var
// so tests can shrink it.
var lsRemoteTimeout = 15 * time.Second

// BaseValidation reports whether a repo's configured base_branch actually
// exists on the remote. A misconfigured value (e.g. base_branch: "origin")
// makes every `git fetch origin <base>` fail with exit 128 forever, which
// silently blocks analysis operators round after round (issue #300).
type BaseValidation struct {
	Base string // the base branch name that was checked

	// LocalRefExists is true when refs/remotes/origin/<base> is present in
	// the local clone — the cheap, offline-safe signal.
	LocalRefExists bool

	// RemoteRefExists is true when `git ls-remote --heads origin <base>`
	// lists the branch. Only consulted when LocalRefExists is false, so a
	// clone that simply hasn't fetched yet isn't reported as misconfigured.
	RemoteRefExists bool

	// RemoteChecked is false when the ls-remote probe itself failed
	// (offline, missing credentials) — in that case "invalid" is unproven
	// and callers must not treat the result as a config error.
	RemoteChecked bool

	// RemoteDefault is the remote's default branch (from
	// refs/remotes/origin/HEAD), when resolvable — the suggested fix value.
	RemoteDefault string
}

// Valid reports whether the base branch resolves to a real ref. It is true
// when the local remote-tracking ref exists, when the remote lists the
// branch, or when the remote could not be probed at all (unproven → treated
// as valid so offline machines never block on this check).
func (v BaseValidation) Valid() bool {
	if v.LocalRefExists || v.RemoteRefExists {
		return true
	}
	return !v.RemoteChecked
}

// Hint returns a human-readable remediation string when the base branch is
// definitely wrong, or "" when it is valid/unproven.
func (v BaseValidation) Hint() string {
	if v.Valid() {
		return ""
	}
	msg := fmt.Sprintf("base_branch %q does not exist on origin", v.Base)
	if v.RemoteDefault != "" {
		msg += fmt.Sprintf("; the remote default branch is %q", v.RemoteDefault)
	}
	return msg + " — fix base_branch in ~/.clawflow/config/config.yaml or from the dashboard repo page"
}

// ValidateBase checks whether origin/<base> resolves for the clone at
// localPath. The local remote-tracking ref is checked first (pure local read,
// microseconds); only when it is missing do we pay for one `ls-remote` probe
// to tell "not fetched yet" apart from "remote has no such branch".
//
// localPath must be a git clone; an empty localPath yields a zero-value
// result with RemoteChecked=false, i.e. Valid()==true (nothing to check).
func ValidateBase(localPath, base string) BaseValidation {
	if base == "" {
		base = "main"
	}
	v := BaseValidation{Base: base}
	if localPath == "" {
		return v
	}
	v.RemoteDefault = RemoteDefaultBranch(localPath)

	if _, err := gitOut(localPath, "rev-parse", "--verify", "--quiet",
		"refs/remotes/origin/"+base); err == nil {
		v.LocalRefExists = true
		return v
	}

	// Bounded: this runs on the scan path and on every snapshot write, so a
	// black-holed remote must not wedge either. headlessEnv already stops git
	// from waiting on a credential prompt; the deadline covers the rest.
	ctx, cancel := context.WithTimeout(context.Background(), lsRemoteTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, "git", "ls-remote", "--heads", "origin", base)
	c.Dir = localPath
	c.Env = headlessEnv()
	out, err := c.Output()
	if err != nil {
		// Offline / no credentials / timed out / not a git dir — unproven,
		// not invalid.
		return v
	}
	v.RemoteChecked = true
	v.RemoteRefExists = strings.Contains(string(out), "refs/heads/"+base)
	return v
}

// RemoteDefaultBranch resolves the remote's default branch from the local
// clone's refs/remotes/origin/HEAD symref (written at clone time, refreshed by
// `git remote set-head`). Returns "" when it cannot be resolved — callers use
// it only to enrich a warning message, never as a decision input.
func RemoteDefaultBranch(localPath string) string {
	out, err := gitOut(localPath, "symbolic-ref", "--short", "--quiet",
		"refs/remotes/origin/HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(out), "origin/")
}
