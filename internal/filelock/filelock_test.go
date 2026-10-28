package filelock

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockLegacyAndPersistentOwner(t *testing.T) {
	for _, data := range []string{
		fmt.Sprintf("%d\n", os.Getpid()),
		fmt.Sprintf(`{"pid":%d,"started_at":"2026-01-01T00:00:00Z"}`, os.Getpid()),
	} {
		path := filepath.Join(t.TempDir(), "lock")
		if err := os.WriteFile(path, []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
		if !Held(path) {
			t.Fatal("live legacy owner ignored")
		}
		if f, err := Try(path, map[string]any{"pid": os.Getpid()}); err != ErrHeld {
			if f != nil {
				Release(f)
			}
			t.Fatalf("legacy acquire: %v", err)
		}
	}
	path := filepath.Join(t.TempDir(), "lock")
	f, err := Try(path, map[string]any{"pid": os.Getpid(), "started_at": time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	if !Held(path) {
		t.Fatal("kernel owner ignored")
	}
	Release(f)
	if Held(path) {
		t.Fatal("released record treated as ownership")
	}
	g, err := Try(path, map[string]any{"pid": os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	Release(g)
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("inode changed")
	}
}
