package snapshot

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeRunMetaAt creates runs/<slug>/issue-<n>/<ts>/meta.json under root.
func writeRunMetaAt(t *testing.T, root, slug string, issue int, ts string, m RunMeta) {
	t.Helper()
	dir := filepath.Join(root, slug, fmt.Sprintf("issue-%d", issue), ts)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteRunMeta(dir, m); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileRepoIdentityAdoptsExistingHistory(t *testing.T) {
	root := t.TempDir()
	repo := "owner/repo"
	writeRunMetaAt(t, root, "owner__repo", 1, "2026-06-02T11-23-00Z", RunMeta{Repo: repo, IssueNumber: 1, Status: "success"})

	archived, err := reconcileRepoIdentityAt(root, repo, 111)
	if err != nil {
		t.Fatal(err)
	}
	if archived != "" {
		t.Fatalf("first reconcile must adopt, not archive; got %q", archived)
	}
	// History stays in place and the identity is recorded.
	if _, err := os.Stat(filepath.Join(root, "owner__repo", "issue-1")); err != nil {
		t.Fatalf("existing history moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "owner__repo", repoIdentityFile)); err != nil {
		t.Fatalf("identity not written: %v", err)
	}
}

func TestReconcileRepoIdentitySameIDNoop(t *testing.T) {
	root := t.TempDir()
	repo := "owner/repo"
	if _, err := reconcileRepoIdentityAt(root, repo, 111); err != nil {
		t.Fatal(err)
	}
	writeRunMetaAt(t, root, "owner__repo", 1, "2026-06-02T11-23-00Z", RunMeta{Repo: repo, IssueNumber: 1, Status: "success"})

	archived, err := reconcileRepoIdentityAt(root, repo, 111)
	if err != nil {
		t.Fatal(err)
	}
	if archived != "" {
		t.Fatalf("same ID must be a no-op; got archive %q", archived)
	}
	if _, err := os.Stat(filepath.Join(root, "owner__repo", "issue-1")); err != nil {
		t.Fatalf("history moved on no-op: %v", err)
	}
}

func TestReconcileRepoIdentityArchivesOnIDChange(t *testing.T) {
	root := t.TempDir()
	repo := "owner/repo"
	if _, err := reconcileRepoIdentityAt(root, repo, 111); err != nil {
		t.Fatal(err)
	}
	writeRunMetaAt(t, root, "owner__repo", 1, "2026-06-02T11-23-00Z", RunMeta{Repo: repo, IssueNumber: 1, Status: "success"})

	archived, err := reconcileRepoIdentityAt(root, repo, 222)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "owner__repo.orphaned-111")
	if archived != want {
		t.Fatalf("archived to %q, want %q", archived, want)
	}
	// Old history lives in the archive; the fresh tree only has repo.json.
	if _, err := os.Stat(filepath.Join(archived, "issue-1")); err != nil {
		t.Fatalf("old history missing from archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "owner__repo", "issue-1")); !os.IsNotExist(err) {
		t.Fatalf("old history still visible under active tree: %v", err)
	}
	// Third incarnation: archive again without colliding with the first one.
	if _, err := reconcileRepoIdentityAt(root, repo, 222); err != nil {
		t.Fatal(err)
	}
	archived2, err := reconcileRepoIdentityAt(root, repo, 333)
	if err != nil {
		t.Fatal(err)
	}
	if archived2 != filepath.Join(root, "owner__repo.orphaned-222") {
		t.Fatalf("second archive path: %q", archived2)
	}
}

func TestReconcileRepoIdentityZeroIDIsIgnored(t *testing.T) {
	root := t.TempDir()
	repo := "owner/repo"
	if _, err := reconcileRepoIdentityAt(root, repo, 111); err != nil {
		t.Fatal(err)
	}
	archived, err := reconcileRepoIdentityAt(root, repo, 0)
	if err != nil {
		t.Fatal(err)
	}
	if archived != "" {
		t.Fatalf("zero ID must never archive; got %q", archived)
	}
}

func TestCollectRunEntriesSkipsOrphanedDirs(t *testing.T) {
	root := t.TempDir()
	writeRunMetaAt(t, root, "owner__repo", 1, "2026-06-11T08-54-26Z", RunMeta{Repo: "owner/repo", IssueNumber: 1, Status: "success"})
	writeRunMetaAt(t, root, "owner__repo.orphaned-111", 1, "2026-06-02T11-23-00Z", RunMeta{Repo: "owner/repo", IssueNumber: 1, Status: "success"})

	entries := collectRunEntries(root)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 (orphaned tree must be excluded)", len(entries))
	}
}
