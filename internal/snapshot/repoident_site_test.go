package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func seedHistory(t *testing.T, runsRoot, key string) string {
	t.Helper()
	dir := filepath.Join(runsRoot, config.RepoSlugFS(key), "issue-1", "2026-11-01T00-00-00Z")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Two sites hold team/tools/service with the same numeric project ID and the
// same issue number. Their run histories live in separate directories and
// reconciling one never archives or adopts the other.
func TestSameIDAndPathOnTwoSitesKeepSeparateHistory(t *testing.T) {
	runs := t.TempDir()
	siteA := RepoSite{Name: "a", URL: "http://gitlab-a.example.com"}
	siteB := RepoSite{Name: "b", URL: "https://gitlab-b.example.com:8443/gitlab"}
	keyA, keyB := "a:team/tools/service", "b:team/tools/service"
	histA, histB := seedHistory(t, runs, keyA), seedHistory(t, runs, keyB)
	if filepath.Dir(histA) == filepath.Dir(histB) {
		t.Fatal("two sites share a history directory")
	}
	for _, c := range []struct {
		key  string
		site RepoSite
	}{{keyA, siteA}, {keyB, siteB}, {keyA, siteA}, {keyB, siteB}} {
		archived, err := reconcileRepoIdentityAt(runs, c.key, 5, c.site) // same ID 5 on both
		if err != nil || archived != "" {
			t.Fatalf("%s: archived=%q err=%v", c.key, archived, err)
		}
	}
	if !exists(histA) || !exists(histB) {
		t.Fatal("history of one site was archived by the other's reconcile")
	}
}

// The existing protection from #272 still holds: a recreated project on the
// same site (same path, new ID) has its old history archived.
func TestRecreatedProjectOnSameSiteIsStillArchived(t *testing.T) {
	runs := t.TempDir()
	site := RepoSite{Name: "b", URL: "https://gitlab-b.example.com"}
	key := "b:team/tools/service"
	hist := seedHistory(t, runs, key)
	if _, err := reconcileRepoIdentityAt(runs, key, 5, site); err != nil {
		t.Fatal(err)
	}
	archived, err := reconcileRepoIdentityAt(runs, key, 6, site)
	if err != nil || archived == "" {
		t.Fatalf("recreated repo must archive old history: %q, %v", archived, err)
	}
	if exists(hist) {
		t.Error("old history still in place after recreation")
	}
	if !exists(archived) {
		t.Errorf("archive %s missing", archived)
	}
	// Same ID again: nothing more happens.
	if archived, err := reconcileRepoIdentityAt(runs, key, 6, site); err != nil || archived != "" {
		t.Fatalf("stable ID: %q, %v", archived, err)
	}
}

// An identity file written before sites existed (no site fields) is adopted
// once for the repo's site and keeps protecting against recreation.
func TestLegacyIdentityRecordIsAdoptedThenSitePinned(t *testing.T) {
	runs := t.TempDir()
	key := "team/tools/service"
	hist := seedHistory(t, runs, key)
	if _, err := reconcileRepoIdentityAt(runs, key, 5, RepoSite{}); err != nil { // legacy record
		t.Fatal(err)
	}
	site := RepoSite{Name: "a", URL: "http://gitlab-a.example.com"}
	if archived, err := reconcileRepoIdentityAt(runs, key, 5, site); err != nil || archived != "" {
		t.Fatalf("same ID, newly bound site must keep history: %q, %v", archived, err)
	}
	if !exists(hist) {
		t.Fatal("history lost when the legacy repo was bound to a site")
	}
	// Now pinned to site a: a different ID is a recreation.
	if archived, err := reconcileRepoIdentityAt(runs, key, 9, site); err != nil || archived == "" {
		t.Fatalf("recreation after pinning: %q, %v", archived, err)
	}
}

// Upgrade path: the first scan after upgrade sees an unregistered ("legacy")
// GitLab repo, which repoSiteOf reports as {Name:"", URL:base_url}. Registering
// that same URL as a named site must keep the history and pin the record to the
// name; a recreated project afterwards is still archived.
func TestLegacyURLRecordIsAdoptedByNamedSiteWithSameURL(t *testing.T) {
	runs := t.TempDir()
	key := "team/tools/service"
	hist := seedHistory(t, runs, key)
	legacy := RepoSite{Name: "", URL: "http://gitlab-a.example.com"}
	if archived, err := reconcileRepoIdentityAt(runs, key, 5, legacy); err != nil || archived != "" {
		t.Fatalf("first scan: %q, %v", archived, err)
	}
	// Same origin spelled differently (case, default port, trailing slash).
	named := RepoSite{Name: "old", URL: "http://GitLab-A.example.com:80/"}
	if archived, err := reconcileRepoIdentityAt(runs, key, 5, named); err != nil || archived != "" {
		t.Fatalf("registering the same URL must keep history: %q, %v", archived, err)
	}
	if !exists(hist) {
		t.Fatal("history lost when the legacy site was registered by name")
	}
	data, err := os.ReadFile(filepath.Join(runs, config.RepoSlugFS(key), repoIdentityFile))
	if err != nil || !strings.Contains(string(data), `"site": "old"`) && !strings.Contains(string(data), `"site":"old"`) {
		t.Fatalf("record not pinned to the site name: %s %v", data, err)
	}
	// Pinned: the same name keeps matching, and a new ID is a recreation.
	if archived, err := reconcileRepoIdentityAt(runs, key, 5, named); err != nil || archived != "" {
		t.Fatalf("pinned record, same ID: %q, %v", archived, err)
	}
	if archived, err := reconcileRepoIdentityAt(runs, key, 9, named); err != nil || archived == "" {
		t.Fatalf("recreation after pinning: %q, %v", archived, err)
	}
}

// A legacy record that carries a URL is not adopted by a site with another URL,
// even when the numeric project ID is the same.
func TestLegacyURLRecordIsNotAdoptedByDifferentSite(t *testing.T) {
	runs := t.TempDir()
	key := "team/tools/service"
	hist := seedHistory(t, runs, key)
	if _, err := reconcileRepoIdentityAt(runs, key, 5, RepoSite{URL: "http://gitlab-a.example.com"}); err != nil {
		t.Fatal(err)
	}
	archived, err := reconcileRepoIdentityAt(runs, key, 5, RepoSite{Name: "b", URL: "https://gitlab-b.example.com:8443/gitlab"})
	if err != nil || archived == "" {
		t.Fatalf("history of another site must not be inherited: %q, %v", archived, err)
	}
	if exists(hist) {
		t.Error("old history still in place")
	}
}

// A record pinned to one site is never matched by ID against another site.
func TestIdentityPinnedToOtherSiteIsNotReused(t *testing.T) {
	runs := t.TempDir()
	key := "team/tools/service"
	seedHistory(t, runs, key)
	if _, err := reconcileRepoIdentityAt(runs, key, 5, RepoSite{Name: "a", URL: "http://a.example.com"}); err != nil {
		t.Fatal(err)
	}
	archived, err := reconcileRepoIdentityAt(runs, key, 5, RepoSite{Name: "b", URL: "https://b.example.com"})
	if err != nil || archived == "" {
		t.Fatalf("history of site a must not be inherited by site b: %q, %v", archived, err)
	}
}

func TestLockAndRunPathsDifferPerSite(t *testing.T) {
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	if LockPath("a:team/tools/service", 3) == LockPath("b:team/tools/service", 3) {
		t.Error("issue lock shared across sites")
	}
	if RepoRunLockPath("a:team/tools/service") == RepoRunLockPath("team/tools/service") {
		t.Error("repo run lock shared between a site and the legacy key")
	}
}
