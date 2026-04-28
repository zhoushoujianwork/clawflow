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
func EnsureLocalClone(cfg *config.Config, ownerRepo string, repoCfg config.Repo, progress io.Writer) (string, error) {
	if progress == nil {
		progress = io.Discard
	}

	if repoCfg.LocalPath != "" {
		expanded := ExpandHome(repoCfg.LocalPath)
		if _, err := os.Stat(expanded); err == nil {
			return expanded, nil
		}
		fmt.Fprintf(progress, "local path %q not found, cloning %s ...\n", expanded, ownerRepo)
		if err := cloneRepo(ownerRepo, expanded, repoCfg, progress); err != nil {
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
		// Found existing clone — save path back to config and return.
		saveLocalPath(cfg, ownerRepo, candidate)
		return candidate, nil
	}

	fmt.Fprintf(progress, "local clone not found, cloning %s to %s ...\n", ownerRepo, candidate)
	if err := cloneRepo(ownerRepo, candidate, repoCfg, progress); err != nil {
		return "", fmt.Errorf("auto-clone failed: %w", err)
	}
	saveLocalPath(cfg, ownerRepo, candidate)
	return candidate, nil
}

// cloneRepo runs `git clone <url> <dest>`, deriving the URL from the
// platform/base_url combo. `progress` captures stdout+stderr of git.
func cloneRepo(ownerRepo, dest string, repoCfg config.Repo, progress io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	var cloneURL string
	if repoCfg.Platform == "gitlab" && repoCfg.BaseURL != "" {
		cloneURL = strings.TrimSuffix(repoCfg.BaseURL, "/") + "/" + ownerRepo + ".git"
	} else {
		cloneURL = "https://github.com/" + ownerRepo + ".git"
	}
	c := exec.Command("git", "clone", cloneURL, dest)
	c.Stdout = progress
	c.Stderr = progress
	return c.Run()
}

func saveLocalPath(cfg *config.Config, ownerRepo, localPath string) {
	if r, ok := cfg.Repos[ownerRepo]; ok {
		r.LocalPath = localPath
		cfg.Repos[ownerRepo] = r
		_ = cfg.Save()
	}
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
