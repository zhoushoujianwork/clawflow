// Package clone resolves and provisions the local-disk clone for a
// configured repo. Lives outside cmd/clawflow/commands so both the CLI
// and the HTTP API can call it.
package clone

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// Token holds the VCS credentials used for clone authentication.
// Passed in by the caller so clone doesn't need to load credentials itself.
type Token struct {
	GHToken     string
	GitLabToken string
}

// EnsureLocalClone returns the local repo path for `ownerRepo`,
// auto-cloning when necessary. Behavior:
//
//  1. If repoCfg.LocalPath is set and exists on disk → return it.
//  2. If LocalPath is set but missing → clone to that path.
//  3. Otherwise, build a default path under the platform's clone-base
//     dir (cloneBase/<repo-tail>); reuse if it already exists, clone
//     fresh otherwise. Either way, save the resolved path back to the
//     config so future calls skip discovery.
//
// `progress` receives `git clone`'s stdout+stderr so the caller can
// stream it (HTTP response, log file, …); pass io.Discard to silence it.
//
// `token` provides VCS credentials for HTTPS clone. If nil or empty,
// falls back to SSH URL (git@host:owner/repo.git) which relies on the
// user's local SSH key configuration.
func EnsureLocalClone(cfg *config.Config, ownerRepo string, repoCfg config.Repo, progress io.Writer, token *Token) (string, error) {
	if progress == nil {
		progress = io.Discard
	}

	if repoCfg.LocalPath != "" {
		expanded := ExpandHome(repoCfg.LocalPath)
		if _, err := os.Stat(expanded); err == nil {
			return expanded, nil
		}
		fmt.Fprintf(progress, "local path %q not found, cloning %s ...\n", expanded, ownerRepo)
		if err := cloneRepo(ownerRepo, expanded, repoCfg, progress, token); err != nil {
			return "", fmt.Errorf("auto-clone failed: %w", err)
		}
		return expanded, nil
	}

	cloneBase := cfg.Settings.ResolveGithubCloneDir()
	if repoCfg.Platform == "gitlab" {
		cloneBase = cfg.Settings.ResolveGitlabCloneDir()
	}

	// Layout: cloneBase/<repo-name-only>. The user's convention is a
	// flat directory under github/ or gitlab/ — no group subdirs even
	// for nested GitLab paths. Earlier versions tried to mirror the
	// platform's group hierarchy here and ended up double-cloning a
	// repo that already existed at the flat path.
	//
	// Examples (gitlabBase = ~/gitlab):
	//   "owner/repo"           → ~/gitlab/repo
	//   "ns/group/repo"        → ~/gitlab/repo  (NOT ~/gitlab/group/repo)
	//   "ns/sub/group/repo"    → ~/gitlab/repo
	parts := strings.Split(ownerRepo, "/")
	subPath := parts[len(parts)-1]
	candidate := filepath.Join(cloneBase, subPath)

	if _, err := os.Stat(candidate); err == nil {
		// Found existing directory — verify it's actually a clone of the
		// expected repo by checking the git remote origin URL.
		if matchesRepo(candidate, ownerRepo, repoCfg) {
			saveLocalPath(cfg, ownerRepo, candidate)
			return candidate, nil
		}
		// Name collision: the flat path is occupied by a different repo.
		// Try a disambiguated path: <cloneBase>/<owner>-<repo>
		// e.g. ~/github/daboluocc-bbclaw instead of ~/github/bbclaw
		fallback := filepath.Join(cloneBase, strings.ReplaceAll(ownerRepo, "/", "-"))
		if _, err := os.Stat(fallback); err == nil {
			// Fallback path already exists — check if it's already the right clone.
			if matchesRepo(fallback, ownerRepo, repoCfg) {
				saveLocalPath(cfg, ownerRepo, fallback)
				return fallback, nil
			}
			// Both flat and disambiguated paths are occupied by other repos.
			return "", fmt.Errorf("directory %s exists but is not a clone of %s (also tried %s)", candidate, ownerRepo, fallback)
		}
		fmt.Fprintf(progress, "path %s is occupied by another repo, cloning %s to %s instead ...\n", candidate, ownerRepo, fallback)
		if err := cloneRepo(ownerRepo, fallback, repoCfg, progress, token); err != nil {
			return "", fmt.Errorf("auto-clone failed: %w", err)
		}
		saveLocalPath(cfg, ownerRepo, fallback)
		return fallback, nil
	}

	fmt.Fprintf(progress, "local clone not found, cloning %s to %s ...\n", ownerRepo, candidate)
	if err := cloneRepo(ownerRepo, candidate, repoCfg, progress, token); err != nil {
		return "", fmt.Errorf("auto-clone failed: %w", err)
	}
	saveLocalPath(cfg, ownerRepo, candidate)
	return candidate, nil
}

// cloneRepo runs `git clone <url> <dest>`, deriving the URL from the
// platform/base_url combo. Authentication strategy:
//
//  1. If a token is available → HTTPS URL with token embedded
//     (https://x-access-token:<token>@github.com/owner/repo.git)
//  2. If no token → SSH URL (git@host:owner/repo.git) relying on
//     the user's local SSH key configuration.
//
// GIT_TERMINAL_PROMPT=0 is always set to prevent git from hanging on
// interactive credential prompts when running headless.
func cloneRepo(ownerRepo, dest string, repoCfg config.Repo, progress io.Writer, token *Token) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}

	cloneURL := buildCloneURL(ownerRepo, repoCfg, token)
	// Log the URL without the embedded token for security
	safeURL := sanitizeURLForLog(cloneURL)
	fmt.Fprintf(progress, "clone url: %s\n", safeURL)

	c := exec.Command("git", "clone", cloneURL, dest)
	c.Stdout = progress
	c.Stderr = progress
	// Prevent git from waiting for interactive password input
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return c.Run()
}

// buildCloneURL constructs the clone URL based on available credentials.
// Priority: token-based HTTPS > SSH fallback.
func buildCloneURL(ownerRepo string, repoCfg config.Repo, token *Token) string {
	isGitLab := repoCfg.Platform == "gitlab"

	// Determine the effective token
	var effectiveToken string
	if token != nil {
		if isGitLab {
			effectiveToken = token.GitLabToken
		} else {
			effectiveToken = token.GHToken
		}
	}

	if isGitLab && repoCfg.BaseURL != "" {
		baseURL := strings.TrimSuffix(repoCfg.BaseURL, "/")
		if effectiveToken != "" {
			// Preserve the user's configured scheme (http or https).
			// Format: <scheme>://oauth2:<token>@<host>/<owner>/<repo>.git
			scheme := "http"
			host := baseURL
			if strings.HasPrefix(baseURL, "https://") {
				scheme = "https"
				host = strings.TrimPrefix(baseURL, "https://")
			} else if strings.HasPrefix(baseURL, "http://") {
				host = strings.TrimPrefix(baseURL, "http://")
			}
			return scheme + "://oauth2:" + effectiveToken + "@" + host + "/" + ownerRepo + ".git"
		}
		// SSH fallback: extract host from base_url
		host := strings.TrimPrefix(baseURL, "https://")
		host = strings.TrimPrefix(host, "http://")
		return "git@" + host + ":" + ownerRepo + ".git"
	}

	// GitHub
	if effectiveToken != "" {
		// https://x-access-token:<token>@github.com/owner/repo.git
		return "https://x-access-token:" + effectiveToken + "@github.com/" + ownerRepo + ".git"
	}
	// SSH fallback
	return "git@github.com:" + ownerRepo + ".git"
}

// sanitizeURLForLog removes embedded credentials from a URL for safe logging.
func sanitizeURLForLog(rawURL string) string {
	// Pattern: https://<user>:<token>@host/...
	if idx := strings.Index(rawURL, "://"); idx >= 0 {
		rest := rawURL[idx+3:]
		if atIdx := strings.Index(rest, "@"); atIdx >= 0 {
			// Has credentials embedded — mask them
			return rawURL[:idx+3] + "***@" + rest[atIdx+1:]
		}
	}
	return rawURL
}

func saveLocalPath(cfg *config.Config, ownerRepo, localPath string) {
	if r, ok := cfg.Repos[ownerRepo]; ok {
		r.LocalPath = localPath
		cfg.Repos[ownerRepo] = r
		_ = cfg.Save()
	}
}

// matchesRepo checks whether the directory at `dir` is a git clone whose
// origin remote matches the expected ownerRepo. Tolerates both HTTPS and
// SSH URL formats.
func matchesRepo(dir, ownerRepo string, repoCfg config.Repo) bool {
	remoteURL, err := config.ReadGitRemoteURL(dir)
	if err != nil {
		return false
	}
	// Normalize: strip trailing .git, protocol prefix, and user@ prefix
	normalized := normalizeRemoteURL(remoteURL)
	// Build expected suffixes for matching
	// e.g. "github.com/owner/repo" or "gitlab.example.com/ns/repo"
	var expected string
	if repoCfg.Platform == "gitlab" && repoCfg.BaseURL != "" {
		host := strings.TrimPrefix(repoCfg.BaseURL, "https://")
		host = strings.TrimPrefix(host, "http://")
		host = strings.TrimSuffix(host, "/")
		expected = host + "/" + ownerRepo
	} else {
		expected = "github.com/" + ownerRepo
	}
	return strings.EqualFold(normalized, expected)
}

// normalizeRemoteURL strips a git remote URL down to "host/path" form
// for comparison. Handles:
//   - https://github.com/owner/repo.git
//   - git@github.com:owner/repo.git
//   - ssh://git@github.com/owner/repo.git
func normalizeRemoteURL(rawURL string) string {
	u := rawURL
	u = strings.TrimSuffix(u, ".git")
	u = strings.TrimSpace(u)

	// SSH shorthand: git@host:path
	if strings.HasPrefix(u, "git@") {
		u = strings.TrimPrefix(u, "git@")
		u = strings.Replace(u, ":", "/", 1)
		return u
	}
	// ssh:// or https:// or http://
	u = strings.TrimPrefix(u, "ssh://")
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	// Strip user@ if present (e.g. git@)
	if atIdx := strings.Index(u, "@"); atIdx >= 0 && atIdx < strings.Index(u, "/") {
		u = u[atIdx+1:]
	}
	return u
}

// DetectLocalClone checks if a local clone already exists for the given
// ownerRepo at the expected path (based on platform clone dir settings).
// Checks both the flat path (<cloneBase>/<repo>) and the disambiguated path
// (<cloneBase>/<owner>-<repo>) that EnsureLocalClone falls back to on collision.
// Returns the local path if found and verified, empty string otherwise.
func DetectLocalClone(cfg *config.Config, ownerRepo string, repoCfg config.Repo) string {
	cloneBase := cfg.Settings.ResolveGithubCloneDir()
	if repoCfg.Platform == "gitlab" {
		cloneBase = cfg.Settings.ResolveGitlabCloneDir()
	}

	parts := strings.Split(ownerRepo, "/")
	subPath := parts[len(parts)-1]
	candidate := filepath.Join(cloneBase, subPath)

	if _, err := os.Stat(candidate); err == nil {
		if matchesRepo(candidate, ownerRepo, repoCfg) {
			return candidate
		}
	}

	// Also check the disambiguated fallback path used when the flat name collides.
	fallback := filepath.Join(cloneBase, strings.ReplaceAll(ownerRepo, "/", "-"))
	if _, err := os.Stat(fallback); err == nil {
		if matchesRepo(fallback, ownerRepo, repoCfg) {
			return fallback
		}
	}

	return ""
}

// ExpandHome resolves a leading `~/` to the current user's home dir.
// Other paths pass through unchanged.
func ExpandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}
