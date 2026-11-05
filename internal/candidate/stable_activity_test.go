package candidate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The stable controller keeps writing data/ (SQLite, records, caches). None of
// it may fail evidence, and the evidence names the ignored top-level entries.
func TestStableSnapshotIgnoresUnprotectedData(t *testing.T) {
	paths := []string{
		"clawflow.sqlite3", "clawflow.sqlite3-wal", "clawflow.sqlite3-shm",
		"acceptance/o__r/1.json", "preflight/o__r/7.json", "coordination/d/1.json",
		"tasks/1.json", "some-new-dir/deep/x", "arbitrary.json",
	}
	stable := testStable(t)
	for _, rel := range paths {
		writeData(t, stable, rel, "before")
	}
	writeData(t, stable, "queue.json", "[1,2]")
	before, err := TakeStable(stable, StableProbe{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range paths {
		writeData(t, stable, rel, "after, longer")
	}
	writeData(t, stable, "brand-new/file", "new")
	after, err := TakeStable(stable, StableProbe{})
	if err != nil {
		t.Fatal(err)
	}
	if d := before.Diff(after); len(d) != 0 {
		t.Fatalf("stable data activity reported as contamination: %v", d)
	}
	ign := strings.Join(before.Ignored(after), ";")
	for _, want := range []string{"stable data activity (ignored)", "clawflow.sqlite3-wal", "acceptance", "brand-new"} {
		if !strings.Contains(ign, want) {
			t.Fatalf("ignored evidence %q lacks %q", ign, want)
		}
	}
	if strings.Contains(ign, "queue.json") || strings.Contains(ign, "o__r") {
		t.Fatalf("ignored evidence lists protected or nested paths: %q", ign)
	}
	ev := &Evidence{Layers: map[string]string{"offline": Passed, "environment-ready": Passed, "candidate-loaded": Passed, "lifecycle": Passed}, StableDiff: before.Diff(after), StableIgnored: before.Ignored(after)}
	ev.Finish()
	if ev.Status != Passed {
		t.Fatalf("status=%s", ev.Status)
	}
}

// Only the manual queue order file in data/ is compared.
func TestStableSnapshotProtectsQueueOrder(t *testing.T) {
	stable := testStable(t)
	writeData(t, stable, "queue.json", "[1,2]")
	before, err := TakeStable(stable, StableProbe{})
	if err != nil {
		t.Fatal(err)
	}
	writeData(t, stable, "queue.json", "[2,1]")
	after, err := TakeStable(stable, StableProbe{})
	if err != nil {
		t.Fatal(err)
	}
	if d := strings.Join(before.Diff(after), ";"); !strings.Contains(d, `stable data path modified: "queue.json"`) {
		t.Fatalf("change missed: %q", d)
	}
}

// Go's module cache is read-only. Removal must finish, not delete the manifest
// and then fail on the first read-only directory.
func TestRemoveDeletesReadOnlyModuleCache(t *testing.T) {
	stable := testStable(t)
	p := plan(t, commitA, 0)
	m, err := Prepare(p, stable, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(p.Root, "cache", "gopath", "pkg", "mod", "example.test", "m@v1")
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "go.mod"), []byte("module m\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(p.Root, "cache", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outside, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(outside, 0o700)
	if err := os.Chmod(cache, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.Root); !os.IsNotExist(err) {
		t.Fatalf("root remains: %v", err)
	}
	info, err := os.Stat(outside)
	if err != nil || info.Mode().Perm() != 0o500 {
		t.Fatalf("symlink target changed: %v %v", info, err)
	}
}
