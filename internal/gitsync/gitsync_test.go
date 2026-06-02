package gitsync

import (
	"testing"
)

// TestCacheRoundTripAndUpsert verifies the on-disk cache write/read cycle and
// that upsert replaces an existing repo's entry in place while preserving the
// others. HOME is redirected to a temp dir so DataDir() resolves under it.
func TestCacheRoundTripAndUpsert(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if got := ReadCache(); len(got) != 0 {
		t.Fatalf("fresh cache should be empty, got %d", len(got))
	}

	if err := upsert(Status{Repo: "a/one", Branch: "main", Ahead: 2}); err != nil {
		t.Fatalf("upsert a/one: %v", err)
	}
	if err := upsert(Status{Repo: "b/two", Branch: "main", Behind: 3}); err != nil {
		t.Fatalf("upsert b/two: %v", err)
	}

	got := ReadCache()
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}

	// Update an existing entry — should replace, not append.
	if err := upsert(Status{Repo: "a/one", Branch: "main", Ahead: 5, Dirty: true}); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	got = ReadCache()
	if len(got) != 2 {
		t.Fatalf("expected still 2 entries after update, got %d", len(got))
	}
	var found *Status
	for i := range got {
		if got[i].Repo == "a/one" {
			found = &got[i]
		}
	}
	if found == nil {
		t.Fatal("a/one missing after update")
	}
	if found.Ahead != 5 || !found.Dirty {
		t.Errorf("a/one not updated: ahead=%d dirty=%v, want 5/true", found.Ahead, found.Dirty)
	}
}
