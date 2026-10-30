package housekeeping

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTempsOwnershipAgeUseAndSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	now := time.Now()
	for _, name := range []string{"clawflow-op-old", "clawflow-op-recent", "clawflow-op-active", "go-build123", "offload-prompt-123", "manual"} {
		p := filepath.Join(root, name)
		if err := os.Mkdir(p, 0755); err != nil {
			t.Fatal(err)
		}
		if name != "clawflow-op-recent" {
			old := now.Add(-25 * time.Hour)
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "clawflow-op-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "clawflow-op-old", "nested")); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-25 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "clawflow-op-old"), old, old); err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{true, false} {
		r := Result{At: now, DryRun: dry}
		r.Temps(root, 24*time.Hour, func(p string) bool { return filepath.Base(p) == "clawflow-op-active" })
		if len(r.Paths) != 1 || filepath.Base(r.Paths[0]) != "clawflow-op-old" || len(r.Errors) != 0 {
			t.Fatalf("%+v", r)
		}
		_, err := os.Stat(filepath.Join(root, "clawflow-op-old"))
		if dry && err != nil {
			t.Fatal("dry-run removed candidate")
		}
		if !dry && !os.IsNotExist(err) {
			t.Fatal("old candidate remains", err)
		}
	}
	for _, name := range []string{"clawflow-op-recent", "clawflow-op-active", "go-build123", "offload-prompt-123", "manual", "clawflow-op-link"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err != nil {
			t.Fatal(name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal("escaped root", err)
	}
}

func TestCacheLimitDryRunAndSafeRoots(t *testing.T) {
	trusted := t.TempDir()
	root := filepath.Join(trusted, "build")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"00", "01", "unowned"} {
		p := filepath.Join(root, name)
		if err := os.Mkdir(p, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "data"), make([]byte, 64), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, dry := range []bool{true, false} {
		r := Result{DryRun: dry}
		r.Cache(trusted, root, 64)
		if len(r.Paths) != 2 || r.Bytes != 128 {
			t.Fatalf("%+v", r)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "unowned")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if SafeDir(root, filepath.Join(root, "link")) || SafeDir(root, outside) {
		t.Fatal("unsafe root accepted")
	}
}

func TestGateProtectsConcurrentTasks(t *testing.T) {
	root := t.TempDir()
	a, err := Gate(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Gate(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	if release, err := Gate(context.Background(), root, true); err == nil {
		release()
		t.Fatal("removed active task protection")
	}
	a()
	b()
	release, err := Gate(context.Background(), root, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if release, err := Gate(ctx, root, false); err == nil {
		release()
		t.Fatal("task started during cleanup")
	}
}

func TestProbe(t *testing.T) {
	d, err := Probe(t.TempDir(), 0)
	if err != nil || d.Total == 0 || d.Available > d.Total || !d.Warning {
		t.Fatalf("%+v %v", d, err)
	}
}

func TestReviewTempsConfinedToOwnedRoot(t *testing.T) {
	trusted := t.TempDir()
	root := filepath.Join(trusted, "clawflow-review-go", "tmp")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, name := range []string{"go-build123", "manual"} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	r := Result{At: time.Now()}
	r.ReviewTemps(trusted, root, 24*time.Hour)
	if len(r.Paths) != 1 || filepath.Base(r.Paths[0]) != "go-build123" {
		t.Fatalf("%+v", r)
	}
	if _, err := os.Stat(filepath.Join(root, "manual")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(trusted, "redirect")); err != nil {
		t.Fatal(err)
	}
	r.ReviewTemps(trusted, filepath.Join(trusted, "redirect"), 0)
	if len(r.Paths) != 1 {
		t.Fatal("followed symlink")
	}
}
