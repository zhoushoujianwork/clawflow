package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireRunLock_BasicAcquireRelease(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// First acquire should succeed.
	if err := AcquireRunLock("v1.0.0"); err != nil {
		t.Fatalf("first AcquireRunLock failed: %v", err)
	}

	// Lock file should exist.
	path := RunLockPath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("run lockfile not created: %v", err)
	}

	// Second acquire from same process should fail (PID is alive).
	if err := AcquireRunLock("v1.0.0"); err == nil {
		t.Error("second AcquireRunLock should fail while lock is held")
	}

	// Release and verify.
	ReleaseRunLock()
	if _, err := os.Stat(path); err != nil || LockHeld(path) {
		t.Error("run lockfile must persist unlocked after ReleaseRunLock")
	}

	// Re-acquire after release should succeed.
	if err := AcquireRunLock("v1.0.0"); err != nil {
		t.Fatalf("re-acquire after release failed: %v", err)
	}
	ReleaseRunLock()
}

func TestAcquireRunLock_ReclaimsStaleLock(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	path := RunLockPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a stale lock with a dead PID (2999999 is extremely unlikely to exist).
	if err := os.WriteFile(path, []byte(`{"pid":2999999,"started_at":"2026-01-01T00:00:00Z","version":"v0.0.1"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Verify the stale PID is actually dead on this system.
	if processAlive(2999999) {
		t.Skip("PID 2999999 is somehow alive on this system")
	}

	// AcquireRunLock should reclaim the stale lock.
	if err := AcquireRunLock("v1.0.0"); err != nil {
		t.Fatalf("AcquireRunLock on stale lock failed: %v", err)
	}

	// Verify the lock is now ours.
	info, err := ReadRunLock()
	if err != nil {
		t.Fatalf("ReadRunLock: %v", err)
	}
	if info.PID != os.Getpid() {
		t.Errorf("lock PID = %d, want %d", info.PID, os.Getpid())
	}
	if info.Version != "v1.0.0" {
		t.Errorf("lock Version = %q, want %q", info.Version, "v1.0.0")
	}
	ReleaseRunLock()
}

func TestAcquireRunLock_CorruptLockfileReclaimed(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	path := RunLockPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a corrupt (non-JSON) lockfile.
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	// AcquireRunLock should remove the corrupt file and succeed.
	if err := AcquireRunLock("v1.0.0"); err != nil {
		t.Fatalf("AcquireRunLock on corrupt lockfile failed: %v", err)
	}
	ReleaseRunLock()
}

func TestReadRunLock_NoFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if _, err := ReadRunLock(); err == nil {
		t.Error("ReadRunLock should return error when no lockfile exists")
	}
}

func TestReleaseRunLock_NoOp(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Should not panic or error on non-existent lockfile.
	ReleaseRunLock()
}

func TestAcquireRunLock_ConcurrentExclusion(t *testing.T) {
	testConcurrentLock(t, RunLockPath,
		func() error { return AcquireRunLock("v1.0.0") }, ReleaseRunLock)
}
