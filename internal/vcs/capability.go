package vcs

import (
	"errors"
	"fmt"
	"strings"
)

// GitLab API capabilities that can differ between server versions. A site
// declares the ones it lacks so the client can say so before sending a request,
// instead of letting a 404 look like a missing project or an auth failure.
const (
	CapIssueEvents     = "issue_events"      // resource state/label events used by issue history
	CapCIVariableScope = "ci_variable_scope" // environment-scoped CI/CD variables
	CapMRPipeline      = "mr_pipeline"       // head pipeline of a merge request (CI status gate)
	CapRunners         = "runners"           // project runner listing
	CapJobPlay         = "job_play"          // starting a manual job
)

// KnownCapabilities lists every capability name a site may declare unsupported.
var KnownCapabilities = []string{CapIssueEvents, CapCIVariableScope, CapMRPipeline, CapRunners, CapJobPlay}

// ValidCapability reports whether name is a declared capability.
func ValidCapability(name string) bool {
	for _, c := range KnownCapabilities {
		if c == name {
			return true
		}
	}
	return false
}

// IsSiteAlias reports whether s can be a GitLab site alias: lowercase letters,
// digits, '_' and '-', starting with a letter or digit. It never contains '/'
// or ':' so "alias:group/project" splits unambiguously.
func IsSiteAlias(s string) bool {
	if s == "" || len(s) > 32 || s == "github" || s == "gitlab" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '_' || r == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

// SplitRepoKey splits a repository key into its site qualifier and project
// path. GitHub keys and pre-existing GitLab keys are bare ("owner/repo",
// "group/sub/project"); GitLab keys created for registered sites are qualified
// ("alias:group/sub/project"). A project path never contains ':'.
func SplitRepoKey(key string) (site, path string) {
	i := strings.IndexByte(key, ':')
	if i <= 0 || strings.ContainsRune(key[:i], '/') || !IsSiteAlias(key[:i]) {
		return "", key
	}
	return key[:i], key[i+1:]
}

// UnsupportedError reports that a site does not offer an API capability. It
// wraps ErrNotSupported and is never an authentication failure.
type UnsupportedError struct {
	Site       string // site alias, or the site URL for a client without an alias
	URL        string
	Repo       string
	Capability string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("GitLab site %s (%s) does not support %q (declared unsupported in the site configuration) for %s; this is not an authentication error and other sites are unaffected", e.Site, e.URL, e.Capability, e.Repo)
}

func (e *UnsupportedError) Unwrap() error { return ErrNotSupported }

// AsUnsupported extracts an UnsupportedError from err.
func AsUnsupported(err error) (*UnsupportedError, bool) {
	var u *UnsupportedError
	if errors.As(err, &u) {
		return u, true
	}
	return nil, false
}

// AuthError reports that a site rejected the credential sent to it. Only that
// site's own credential was used and no other site was contacted.
type AuthError struct {
	Site   string
	URL    string
	Status int
	Path   string
	NoCred bool // no credential is configured for this site
}

func (e *AuthError) Error() string {
	if e.NoCred {
		return fmt.Sprintf("no credential is configured for GitLab site %s (%s); nothing was sent. Set one with: clawflow gitlab site set-token %s (no other site's credential is used)", e.Site, e.URL, e.Site)
	}
	hint := "check the credential configured for this site"
	return fmt.Sprintf("GitLab site %s (%s) rejected the request with HTTP %d for %s: %s (clawflow gitlab site show %s). This is an authentication or permission failure, not a version incompatibility, and no other site was tried", e.Site, e.URL, e.Status, e.Path, hint, e.Site)
}

// SiteMismatchError reports a call whose repository belongs to another site
// than the client's. The request is not sent.
type SiteMismatchError struct {
	ClientSite string
	URL        string
	Repo       string
}

func (e *SiteMismatchError) Error() string {
	return fmt.Sprintf("repository %q does not belong to GitLab site %s (%s); refusing to send the request", e.Repo, e.ClientSite, e.URL)
}
