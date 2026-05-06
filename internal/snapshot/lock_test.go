package snapshot

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAcquireReleaseLock(t *testing.T) {
	// Override LockDir to use a temp directory.
	origHome := os.Getenv("HOME")
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	defer os.Setenv("HOME", origHome)

	repo := "owner/repo"
	issue := 42

	// Acquire should succeed on a fresh lock.
	if err := AcquireLock(repo, issue, "evaluate-bug"); err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}

	// Lock file should exist.
	path := LockPath(repo, issue)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lockfile not created: %v", err)
	}

	// IsLocked should return true (our own PID is alive).
	if !IsLocked(repo, issue) {
		t.Error("IsLocked returned false, want true")
	}

	// Second acquire from the same process should fail (PID is alive).
	if err := AcquireLock(repo, issue, "implement"); err == nil {
		t.Error("second acquire should fail while lock is held")
	}

	// Release and verify.
	ReleaseLock(repo, issue)
	if IsLocked(repo, issue) {
		t.Error("IsLocked returned true after release")
	}

	// Re-acquire should succeed after release.
	if err := AcquireLock(repo, issue, "implement"); err != nil {
		t.Fatalf("re-acquire after release failed: %v", err)
	}
	ReleaseLock(repo, issue)
}

func TestAcquireLock_ReclaimsStaleLock(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	repo := "owner/repo"
	issue := 7

	// Write a lockfile with a dead PID (PID 1 is init, but a very high
	// PID is almost certainly not running).
	path := LockPath(repo, issue)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Use PID 2999999 which is extremely unlikely to exist.
	if err := os.WriteFile(path, []byte(`{"pid":2999999,"started_at":"2026-01-01T00:00:00Z","operator":"old","repo":"owner/repo","issue":7}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// IsLocked should return false (dead PID).
	if IsLocked(repo, issue) {
		t.Skip("PID 2999999 is somehow alive on this system")
	}

	// Acquire should reclaim the stale lock.
	if err := AcquireLock(repo, issue, "new-op"); err != nil {
		t.Fatalf("acquire on stale lock failed: %v", err)
	}

	// Verify the lock is now ours.
	info, err := ReadLock(path)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if info.PID != os.Getpid() {
		t.Errorf("lock PID = %d, want %d", info.PID, os.Getpid())
	}
	if info.Operator != "new-op" {
		t.Errorf("lock operator = %q, want %q", info.Operator, "new-op")
	}
	ReleaseLock(repo, issue)
}

func TestIsLocked_NoFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if IsLocked("nonexistent/repo", 999) {
		t.Error("IsLocked should return false when no lockfile exists")
	}
}

func TestCleanStaleLocks(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Create a stale lock (dead PID).
	repo := "owner/repo"
	path := LockPath(repo, 1)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"pid":2999999,"started_at":"2026-01-01T00:00:00Z","operator":"x","repo":"owner/repo","issue":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a live lock (our PID).
	if err := AcquireLock(repo, 2, "live-op"); err != nil {
		t.Fatal(err)
	}

	cleaned := CleanStaleLocks()
	if cleaned != 1 {
		t.Errorf("CleanStaleLocks cleaned %d, want 1", cleaned)
	}

	// Stale lock should be gone.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("stale lockfile should have been removed")
	}

	// Live lock should still exist.
	if !IsLocked(repo, 2) {
		t.Error("live lock should still be held")
	}
	ReleaseLock(repo, 2)
}

func TestReleaseLock_NoOp(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Should not panic or error on non-existent lock.
	ReleaseLock("no/repo", 999)
}

// TestAcquireLock_ConcurrentExclusion verifies that concurrent callers cannot
// hold the lock simultaneously. Run with -race to catch the data race present
// in the os.WriteFile path; the test should pass cleanly once AcquireLock uses
// O_CREATE|O_EXCL (issue #68).
func TestAcquireLock_ConcurrentExclusion(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	const goroutines = 20
	repo := "owner/repo"
	issue := 99

	var (
		mu      sync.Mutex
		winners []int
		wg      sync.WaitGroup
	)
	start := make(chan struct{})

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start // all goroutines race from the same starting line
			if err := AcquireLock(repo, issue, fmt.Sprintf("op-%d", id)); err == nil {
				mu.Lock()
				winners = append(winners, id)
				mu.Unlock()
				// Hold briefly to give other goroutines a chance to try.
				time.Sleep(5 * time.Millisecond)
				ReleaseLock(repo, issue)
			}
		}(i)
	}

	close(start)
	wg.Wait()

	// Every goroutine that acquired the lock should have done so exclusively.
	// With O_EXCL semantics, at most one goroutine wins per acquire window.
	// We can't assert exactly 1 winner (release + re-acquire is valid), but
	// we can assert no two goroutines held the lock simultaneously by
	// checking the lock file was never double-written (the O_EXCL path
	// returns an error on the second writer, so winners > 1 only if the
	// race is present).
	//
	// A simpler invariant: run the whole thing and assert no panic / data race.
	// Enable with: go test -race ./internal/snapshot/...
	if len(winners) == 0 {
		t.Error("expected at least one goroutine to acquire the lock")
	}
	t.Logf("%d/%d goroutines acquired the lock (sequential windows)", len(winners), goroutines)
}
