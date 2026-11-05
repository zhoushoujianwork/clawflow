package candidate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A healthy scheduler rewrites these constantly, so they must not fail evidence
// for a candidate that never touched the stable host.
func TestStableSnapshotIgnoresSchedulerActivity(t *testing.T) {
	stable := testStable(t)
	data := filepath.Join(stable.Root, "data")
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(data, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// JSON names are files; the others are directories with nested entries.
	entry := func(name string) string {
		if strings.HasSuffix(name, ".json") {
			return name
		}
		return filepath.Join(name, "nested", "entry.json")
	}
	for name := range stableActivityData {
		write(entry(name), "before")
	}
	before, err := TakeStable(stable, StableProbe{})
	if err != nil {
		t.Fatal(err)
	}
	for name := range stableActivityData {
		write(entry(name), "after")
	}
	write("queue-state/o@r.json", "new")
	write("pipeline-prs/o__r.json", "new")
	after, err := TakeStable(stable, StableProbe{})
	if err != nil {
		t.Fatal(err)
	}
	if d := before.Diff(after); len(d) != 0 {
		t.Fatalf("scheduler activity reported as contamination: %v", d)
	}
}

// The exclusion must not widen into state a candidate could damage.
func TestStableSnapshotStillProtectsDurableState(t *testing.T) {
	for _, rel := range []string{
		"queue.json",
		"projects.json",
		"acceptance/o__r/1.json",
		"coordinator/key.pem",
		"ci-repair/o__r.json",
		// A lookalike of an excluded name is not excluded.
		"pipeline.json.bak",
		"runs-archive/old.json",
	} {
		t.Run(rel, func(t *testing.T) {
			stable := testStable(t)
			before, err := TakeStable(stable, StableProbe{})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(stable.Root, "data", rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
			after, err := TakeStable(stable, StableProbe{})
			if err != nil {
				t.Fatal(err)
			}
			if d := strings.Join(before.Diff(after), ";"); !strings.Contains(d, "stable data") {
				t.Fatalf("change missed: %q", d)
			}
		})
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
