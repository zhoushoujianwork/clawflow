package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// RepoIdentity pins a repo-name slug under data/runs/ to the platform's
// immutable numeric repository ID. owner/name is NOT a stable identifier:
// deleting a repo and recreating it under the same name reuses the slug
// (and restarts issue numbering at #1) but mints a new platform ID, so
// without this file the new repo silently inherits the old repo's run
// history (issue #272).
type RepoIdentity struct {
	RepoID    int64     `json:"repo_id"`
	CheckedAt time.Time `json:"checked_at"`
}

// repoIdentityFile lives directly under runs/<owner__repo>/, next to the
// issue-N subdirectories.
const repoIdentityFile = "repo.json"

// orphanInfix marks a runs/<owner__repo> directory that belonged to a
// previous incarnation of a same-named repo. Walkers over the runs tree
// skip these so archived history never leaks into runs.json / usage.
const orphanInfix = ".orphaned-"

// isOrphanedRepoDir reports whether a directory name under data/runs/ is
// an archived previous-incarnation tree.
func isOrphanedRepoDir(name string) bool {
	return strings.Contains(name, orphanInfix)
}

// ReconcileRepoIdentity compares the platform repository ID observed this
// scan against the one recorded in runs/<slug>/repo.json.
//
//   - No identity recorded yet → adopt: write the current ID and keep any
//     existing history (backward compat for trees created before this file
//     existed).
//   - Same ID → no-op.
//   - Different ID → the repo was deleted and recreated under the same
//     name. Archive the whole runs/<slug>/ tree to
//     runs/<slug>.orphaned-<oldID>/ so the new repo starts with a clean
//     history, then record the new ID.
//
// Returns the archive directory path when history was archived, "" otherwise.
func ReconcileRepoIdentity(repo string, currentID int64) (string, error) {
	return reconcileRepoIdentityAt(filepath.Join(DataDir(), "runs"), repo, currentID)
}

// reconcileRepoIdentityAt is the testable core of ReconcileRepoIdentity.
func reconcileRepoIdentityAt(runsRoot, repo string, currentID int64) (string, error) {
	if currentID == 0 {
		// Unknown ID (platform didn't return one) — never archive on a guess.
		return "", nil
	}
	repoDir := filepath.Join(runsRoot, strings.ReplaceAll(repo, "/", "__"))
	identPath := filepath.Join(repoDir, repoIdentityFile)

	if data, err := os.ReadFile(identPath); err == nil {
		var ident RepoIdentity
		if json.Unmarshal(data, &ident) == nil && ident.RepoID != 0 {
			if ident.RepoID == currentID {
				return "", nil
			}
			archived := orphanTarget(repoDir, ident.RepoID)
			if err := os.Rename(repoDir, archived); err != nil {
				return "", fmt.Errorf("archive recreated repo %s: %w", repo, err)
			}
			if err := writeJSON(identPath, RepoIdentity{RepoID: currentID, CheckedAt: time.Now().UTC()}); err != nil {
				return archived, err
			}
			return archived, nil
		}
		// Malformed / zero identity file — fall through and rewrite it.
	}
	if err := writeJSON(identPath, RepoIdentity{RepoID: currentID, CheckedAt: time.Now().UTC()}); err != nil {
		return "", err
	}
	return "", nil
}

// orphanTarget picks a non-existing archive path for repoDir. A second
// recreation cycle can produce the same oldID suffix only if the archive
// was already made once and the identity file was then tampered with, but
// colliding with an existing directory would make os.Rename nest the tree
// inside it — so probe and append a counter just in case.
func orphanTarget(repoDir string, oldID int64) string {
	base := fmt.Sprintf("%s%s%d", repoDir, orphanInfix, oldID)
	target := base
	for i := 2; ; i++ {
		if _, err := os.Stat(target); os.IsNotExist(err) {
			return target
		}
		target = fmt.Sprintf("%s-%d", base, i)
	}
}
