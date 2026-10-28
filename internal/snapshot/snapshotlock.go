package snapshot

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The dashboard snapshot files (issues.json, pending.json, runs.json, ...) are
// shared by every concurrent `clawflow run` pass and by `clawflow web`. Writers
// that read-modify-write them do so under a short cross-process lock so two
// passes cannot lose each other's updates.

var (
	// snapshotLockWait bounds how long a writer waits for the snapshot lock;
	// variables so tests can shorten them.
	snapshotLockWait = 30 * time.Second
	snapshotLockPoll = 25 * time.Millisecond

	// snapMu serializes goroutines of this process so they queue in memory
	// instead of polling the lockfile against each other.
	snapMu sync.Mutex
)

// SnapshotLockPath returns ~/.clawflow/locks/snapshot.lock.
func SnapshotLockPath() string {
	return filepath.Join(LockDir(), "snapshot.lock")
}

// AcquireSnapshotLock takes the cross-process snapshot lock, waiting up to
// 30s. The kernel releases locks on process exit. Callers inside this package should
// use withSnapshotLock, which also serializes goroutines of this process.
func AcquireSnapshotLock() error {
	return acquireWaiting(SnapshotLockPath(), snapshotLockWait)
}

// acquireWaiting polls acquirePIDLock until it succeeds or wait elapses.
func acquireWaiting(path string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		holder, err := acquirePIDLock(path, RunLockInfo{PID: os.Getpid(), StartedAt: time.Now().UTC()})
		if err == nil {
			return nil
		}
		if holder == nil {
			return err // not contention (e.g. unwritable lock dir)
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%s still held by pid %d after %s", filepath.Base(path), holder.PID, wait)
		}
		time.Sleep(snapshotLockPoll)
	}
}

// ReleaseSnapshotLock releases the snapshot lock if this process holds it.
func ReleaseSnapshotLock() {
	releasePIDLock(SnapshotLockPath())
}

// withSnapshotLock runs fn while holding the snapshot lock. fn must not call
// another function that takes the lock.
func withSnapshotLock(fn func() error) error {
	snapMu.Lock()
	defer snapMu.Unlock()
	if err := AcquireSnapshotLock(); err != nil {
		return fmt.Errorf("snapshot lock: %w", err)
	}
	defer ReleaseSnapshotLock()
	return fn()
}

// PilotScheduleLockPath guards pilot.Schedule so only one pass wakes projects
// at a time (passes are repo-scoped and run concurrently).
func PilotScheduleLockPath() string {
	return filepath.Join(LockDir(), "pilot-schedule.lock")
}

// TryAcquirePilotScheduleLock takes the pilot scheduling lock without waiting.
func TryAcquirePilotScheduleLock() bool {
	_, err := acquirePIDLock(PilotScheduleLockPath(), RunLockInfo{PID: os.Getpid(), StartedAt: time.Now().UTC()})
	return err == nil
}

// ReleasePilotScheduleLock releases the pilot scheduling lock if held by this process.
func ReleasePilotScheduleLock() {
	releasePIDLock(PilotScheduleLockPath())
}

// ConfigSyncLockPath guards config auto-pull / auto-push. Passes are repo
// scoped and run concurrently; config.yaml is rewritten non-atomically by
// ApplyGistConfig, so concurrent syncs must be serialized.
func ConfigSyncLockPath() string {
	return filepath.Join(LockDir(), "config-sync.lock")
}

// WithConfigSyncLock runs fn under the config sync lock, waiting up to wait.
// Returns false without running fn when the lock cannot be taken.
func WithConfigSyncLock(wait time.Duration, fn func()) bool {
	if err := acquireWaiting(ConfigSyncLockPath(), wait); err != nil {
		return false
	}
	defer releasePIDLock(ConfigSyncLockPath())
	fn()
	return true
}

// WithSnapshotLock runs fn under the cross-process snapshot lock for callers
// outside this package (e.g. the queue rank file). fn must not take the lock.
func WithSnapshotLock(fn func() error) error { return withSnapshotLock(fn) }
