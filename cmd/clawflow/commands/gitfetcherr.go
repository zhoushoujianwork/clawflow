package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/branch"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
)

// gitFetchErrKind classifies why a `git fetch` failed. git exits 128 for a
// missing remote ref, a rejected credential and an unreachable host alike, so
// the exit status carries no signal — the classification has to come from the
// stderr text (issue #300).
type gitFetchErrKind int

const (
	gitFetchUnknown gitFetchErrKind = iota
	// gitFetchRefNotFound: the remote answered, but has no such branch.
	// Deterministic configuration error — retrying never helps.
	gitFetchRefNotFound
	// gitFetchAuth: reachable remote, rejected or missing credentials.
	gitFetchAuth
	// gitFetchNetwork: the remote could not be reached at all.
	gitFetchNetwork
)

// classifyGitFetchError maps git's combined output to a gitFetchErrKind.
// Ref-not-found is checked first: a wrong base_branch is the one cause the
// operator can name precisely, and its markers never co-occur with the others.
func classifyGitFetchError(output string) gitFetchErrKind {
	lower := strings.ToLower(output)
	contains := func(pats ...string) bool {
		for _, p := range pats {
			if strings.Contains(lower, p) {
				return true
			}
		}
		return false
	}

	switch {
	case contains(
		"couldn't find remote ref",
		"couldn't find remote branch",
		"unknown revision or path not in the working tree",
		"no such ref was fetched",
		"matched no remote refs",
	):
		return gitFetchRefNotFound
	case contains(
		"authentication failed",
		"permission denied",
		"could not read username",
		"could not read password",
		"invalid username or password",
		"terminal prompts disabled",
		"access denied",
	):
		return gitFetchAuth
	case contains(
		"could not resolve host",
		"could not resolve hostname",
		"connection timed out",
		"connection refused",
		"connection reset by peer",
		"network is unreachable",
		"does not appear to be a git repository",
		"failed to connect to",
		"no network response",
	):
		return gitFetchNetwork
	}
	return gitFetchUnknown
}

// describeGitFetchFailure renders a fetch failure with a cause-specific
// explanation instead of the old blanket "without network access" wording,
// which sent issue #300's investigation after SSH deploy keys for three weeks
// while the real problem was base_branch: "origin".
//
// remoteDefault, when non-empty, is offered as the likely correct value.
func describeGitFetchFailure(base, localPath, output string, remoteDefault string, err error) error {
	switch classifyGitFetchError(output) {
	case gitFetchRefNotFound:
		msg := fmt.Sprintf(
			"git fetch origin/%s: %v — the remote has no ref %q, "+
				"so base_branch is likely misconfigured for this repo",
			base, err, base)
		if remoteDefault != "" && remoteDefault != base {
			msg += fmt.Sprintf(" (the remote default branch is %q)", remoteDefault)
		}
		return fmt.Errorf("%s; fix base_branch in your clawflow config, "+
			"then re-run — this is a configuration error, not a network problem", msg)
	case gitFetchAuth:
		return fmt.Errorf(
			"git fetch origin/%s: %w — the remote rejected our credentials "+
				"(check the SSH key / token available to %s)", base, err, localPath)
	case gitFetchNetwork:
		return fmt.Errorf(
			"git fetch origin/%s: %w — the remote is unreachable, "+
				"cannot refresh analysis code without network access", base, err)
	}
	return fmt.Errorf("git fetch origin/%s: %w — analysis blocked to avoid "+
		"stale-code evaluation (see stderr above for git's own message)", base, err)
}

// warnInvalidBaseBranch validates a repo's configured base_branch against its
// local clone once per scan and warns when the remote has no such branch.
//
// Cost is one local ref read in the healthy case; the ls-remote probe only
// runs when the remote-tracking ref is missing. Warn-only by design: an
// offline machine or a freshly cloned repo must not stop the scan.
func warnInvalidBaseBranch(lg *clog.Logger, fullName string, repoCfg config.Repo) {
	if !repoCfg.Enabled || repoCfg.LocalPath == "" {
		return
	}
	v := branch.ValidateBase(repoCfg.LocalPath, repoCfg.BaseBranch)
	if v.Valid() {
		return
	}
	fmt.Fprintf(os.Stderr, "  ⚠ %s: %s\n", fullName, v.Hint())
	lg.Warn("run/base_branch_invalid",
		"repo", fullName,
		"base", v.Base,
		"remote_default", v.RemoteDefault,
	)
}
