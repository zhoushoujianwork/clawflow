package snapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

// The dashboard snapshot files (issues.json, pending.json, runs.json, ...) are
// shared by every concurrent `clawflow run` pass and by `clawflow web`. Writers
// that read-modify-write them do so under a short cross-process lock so two
// passes cannot lose each other's updates.

var (
	// snapshotLockWait bounds how long a writer waits for the snapshot lock;
	// variable so tests can shorten it.
	snapshotLockWait = 30 * time.Second

	// snapMu serializes goroutines of this process so they queue in memory
	// instead of waiting in the kernel against each other.
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

// acquireWaiting uses blocking flock without holding the descriptor registry
// mutex, so the current owner can release while this caller waits.
func acquireWaiting(path string, wait time.Duration) error {
	if wait <= 0 {
		_, err := acquirePIDLock(path, RunLockInfo{PID: os.Getpid(), StartedAt: time.Now().UTC()})
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	f, err := filelock.Wait(ctx, path, RunLockInfo{PID: os.Getpid(), StartedAt: time.Now().UTC()})
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s still held after %s: %w", filepath.Base(path), wait, err)
		}
		return err
	}
	lockFileMu.Lock()
	heldFiles[path] = f
	lockFileMu.Unlock()
	return nil
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
