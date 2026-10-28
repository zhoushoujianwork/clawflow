package filelock

import (
	"context"
	"errors"
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
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		f, err := Wait(ctx, path, map[string]any{"pid": os.Getpid()})
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			if f != nil {
				Release(f)
			}
			t.Fatalf("legacy wait: %v", err)
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

func TestLockWaitTimeoutAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	owner := map[string]any{"pid": os.Getpid(), "version": "holder"}
	f, err := Try(path, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer Release(f)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if g, err := Wait(ctx, path, map[string]any{"version": "abandoned"}); !errors.Is(err, context.DeadlineExceeded) {
		if g != nil {
			Release(g)
		}
		t.Fatalf("waiting lock: %v", err)
	}
	if after, err := os.ReadFile(path); err != nil || string(before) != string(after) {
		t.Fatalf("owner changed: %s %v", after, err)
	}
	Release(f)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	g, err := Wait(ctx2, path, owner)
	if err != nil {
		t.Fatal(err)
	}
	defer Release(g)
	if h, err := Try(path, owner); !errors.Is(err, ErrHeld) {
		if h != nil {
			Release(h)
		}
		t.Fatalf("second owner: %v", err)
	}
}
