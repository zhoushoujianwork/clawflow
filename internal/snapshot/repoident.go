package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// RepoIdentity pins a repo-name slug under data/runs/ to the platform's
// immutable numeric repository ID. owner/name is NOT a stable identifier:
// deleting a repo and recreating it under the same name reuses the slug
// (and restarts issue numbering at #1) but mints a new platform ID, so
// without this file the new repo silently inherits the old repo's run
// history (issue #272).
//
// The numeric ID is only unique inside one platform site, so the record also
// pins the site (platform and URL). Two sites may legitimately report the same
// path or ID; a record written for one never matches the other.
type RepoIdentity struct {
	RepoID    int64     `json:"repo_id"`
	CheckedAt time.Time `json:"checked_at"`
	Site      string    `json:"site,omitempty"`     // registered alias, empty for GitHub or a legacy site
	SiteURL   string    `json:"site_url,omitempty"` // GitLab root URL, empty for GitHub
}

// RepoSite is the platform site a repository ID was read from.
type RepoSite struct {
	Name string
	URL  string
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
	return ReconcileRepoIdentityAtSite(repo, currentID, RepoSite{})
}

// ReconcileRepoIdentityAtSite is ReconcileRepoIdentity for a repository bound
// to a platform site. A record that names a different site is never adopted or
// compared by ID: the history is archived and re-pinned to the new site.
func ReconcileRepoIdentityAtSite(repo string, currentID int64, site RepoSite) (string, error) {
	return reconcileRepoIdentityAt(filepath.Join(DataDir(), "runs"), repo, currentID, site)
}

// reconcileRepoIdentityAt is the testable core of ReconcileRepoIdentity.
func reconcileRepoIdentityAt(runsRoot, repo string, currentID int64, site RepoSite) (string, error) {
	if currentID == 0 {
		// Unknown ID (platform didn't return one) — never archive on a guess.
		return "", nil
	}
	repoDir := filepath.Join(runsRoot, config.RepoSlugFS(repo))
	identPath := filepath.Join(repoDir, repoIdentityFile)

	if data, err := os.ReadFile(identPath); err == nil {
		var ident RepoIdentity
		if json.Unmarshal(data, &ident) == nil && ident.RepoID != 0 {
			sameSite := ident.Site == site.Name && ident.SiteURL == site.URL
			// Records written before sites existed carry no site. They are
			// adopted once, and compared by ID only for the site they were
			// created under.
			legacyRecord := ident.Site == "" && ident.SiteURL == ""
			if ident.RepoID == currentID && (sameSite || legacyRecord) {
				if legacyRecord && (site.Name != "" || site.URL != "") {
					return "", writeJSON(identPath, RepoIdentity{RepoID: currentID, CheckedAt: time.Now().UTC(), Site: site.Name, SiteURL: site.URL})
				}
				return "", nil
			}
			archived := orphanTarget(repoDir, ident.RepoID)
			if err := os.Rename(repoDir, archived); err != nil {
				return "", fmt.Errorf("archive recreated repo %s: %w", repo, err)
			}
			if err := writeJSON(identPath, RepoIdentity{RepoID: currentID, CheckedAt: time.Now().UTC(), Site: site.Name, SiteURL: site.URL}); err != nil {
				return archived, err
			}
			return archived, nil
		}
		// Malformed / zero identity file — fall through and rewrite it.
	}
	if err := writeJSON(identPath, RepoIdentity{RepoID: currentID, CheckedAt: time.Now().UTC(), Site: site.Name, SiteURL: site.URL}); err != nil {
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
