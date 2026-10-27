package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Per-repository run locks.
//
// A `clawflow run` pass used to hold one global lock (run.lock) for its whole
// lifetime, so a long pass for repo A blocked repos B and C. Passes now lock
// only the repositories they process, one run-<owner>__<name>.lock per repo.
// The legacy global run.lock is still honored: while a live process holds it
// (an older binary during rollout, or `clawflow reset`), every repo counts as
// locked.

const runLockPrefix = "run-"

// RepoRunLockPath returns the per-repository run lockfile path
// (~/.clawflow/locks/run-<owner>__<name>.lock). Every '/' becomes "__", so
// GitLab subgroups (a/b/c) map to a single flat filename like issue locks do.
func RepoRunLockPath(repo string) string {
	return filepath.Join(LockDir(), runLockPrefix+strings.ReplaceAll(repo, "/", "__")+".lock")
}

// liveLegacyRunLock returns the holder of the legacy global run lock when its
// owner process is alive.
func liveLegacyRunLock() (*RunLockInfo, bool) {
	info, err := ReadRunLock()
	if err != nil || !processAlive(info.PID) {
		return nil, false
	}
	return info, true
}

func runLockHeldError(info *RunLockInfo) error {
	return fmt.Errorf("another clawflow run is active (pid=%d, started=%s)",
		info.PID, info.StartedAt.Format(time.RFC3339))
}

// AcquireRepoRunLock takes the run lock for one repository. It fails when a
// live process holds that repo's lock or the legacy global lock. Stale locks
// (dead owner PID) are reclaimed.
func AcquireRepoRunLock(repo, version string) error {
	if legacy, ok := liveLegacyRunLock(); ok {
		return runLockHeldError(legacy)
	}
	holder, err := acquirePIDLock(RepoRunLockPath(repo), RunLockInfo{
		PID:       os.Getpid(),
		StartedAt: time.Now().UTC(),
		Version:   version,
		Repo:      repo,
	})
	if err != nil {
		if holder != nil {
			return runLockHeldError(holder)
		}
		return err
	}
	// Re-check after publishing: a legacy holder (old binary, `clawflow reset`)
	// may have appeared between the first check and our create. Whichever side
	// sees the other backs off, so the two can never both proceed.
	if legacy, ok := liveLegacyRunLock(); ok && legacy.PID != os.Getpid() {
		releasePIDLock(RepoRunLockPath(repo))
		return runLockHeldError(legacy)
	}
	return nil
}

// ReleaseRepoRunLock removes this process's lock for repo. A lock owned by
// another live process is left alone. No-op if missing.
func ReleaseRepoRunLock(repo string) {
	releasePIDLock(RepoRunLockPath(repo))
}

// ReadRepoRunLock parses the per-repository run lockfile.
func ReadRepoRunLock(repo string) (*RunLockInfo, error) {
	return readRunLockFile(RepoRunLockPath(repo))
}

// AnyRunLockHeld reports whether the legacy global lock or any per-repository
// run lock is held by a live process, returning one holder.
func AnyRunLockHeld() (bool, *RunLockInfo) {
	if legacy, ok := liveLegacyRunLock(); ok {
		return true, legacy
	}
	matches, _ := filepath.Glob(filepath.Join(LockDir(), runLockPrefix+"*.lock"))
	for _, path := range matches {
		if info, err := readRunLockFile(path); err == nil && processAlive(info.PID) {
			return true, info
		}
	}
	return false, nil
}

func readRunLockFile(path string) (*RunLockInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var info RunLockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// publishLockFile creates path with data in one step: the payload is written
// to a private temp file which is then hard-linked into place. Unlike
// create-then-write, no other process can observe a half-written lockfile and
// mistake it for a corrupt one. Returns an os.IsExist error when path exists.
func publishLockFile(path string, data []byte) error {
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	defer os.Remove(tmp)
	err := os.Link(tmp, path)
	if err == nil || os.IsExist(err) {
		return err
	}
	// Filesystem without hard links: fall back to exclusive create.
	return writeExclusive(path, data)
}

// acquirePIDLock creates the lockfile at path for info, reclaiming it when the
// existing owner is dead or the payload is corrupt. When a live process holds
// it, the returned holder is non-nil together with a non-nil error.
func acquirePIDLock(path string, info RunLockInfo) (*RunLockInfo, error) {
	lockFileMu.Lock()
	defer lockFileMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir lock dir: %w", err)
	}
	data, err := json.Marshal(info)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		err := publishLockFile(path, data)
		if err == nil {
			return nil, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("create lockfile: %w", err)
		}
		existing, readErr := readRunLockFile(path)
		if readErr == nil && processAlive(existing.PID) {
			return existing, fmt.Errorf("lock %s held by pid %d", filepath.Base(path), existing.PID)
		}
		if readErr != nil && os.IsNotExist(readErr) {
			continue // released between the link attempt and the read
		}
		// Stale (dead owner) or corrupt. Remove only if it is still the same
		// file we judged dead, so a lock freshly re-created by another
		// process is not deleted.
		again, againErr := readRunLockFile(path)
		if (readErr == nil && againErr == nil && again.PID == existing.PID) || (readErr != nil && againErr != nil) {
			_ = os.Remove(path)
		}
	}
	if existing, readErr := readRunLockFile(path); readErr == nil {
		return existing, fmt.Errorf("lock %s held by pid %d", filepath.Base(path), existing.PID)
	}
	return nil, fmt.Errorf("lock %s already held by another process", filepath.Base(path))
}

// releasePIDLock removes the lockfile when this process owns it (or it is
// unreadable). A lock reclaimed by another live process is left in place.
func releasePIDLock(path string) {
	lockFileMu.Lock()
	defer lockFileMu.Unlock()

	if info, err := readRunLockFile(path); err == nil && info.PID != os.Getpid() {
		return
	}
	_ = os.Remove(path)
}
