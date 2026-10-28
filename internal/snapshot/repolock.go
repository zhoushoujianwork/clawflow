package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/filelock"
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
	if !LockHeld(RunLockPath()) {
		return nil, false
	}
	info, err := ReadRunLock()
	if err != nil {
		info = &RunLockInfo{}
	}
	return info, true
}

func runLockHeldError(info *RunLockInfo) error {
	return fmt.Errorf("another clawflow run is active (pid=%d, started=%s)",
		info.PID, info.StartedAt.Format(time.RFC3339))
}

// AcquireRepoRunLock takes the run lock for one repository. It fails when a
// live process holds that repo's lock or the legacy global lock.
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

// ReleaseRepoRunLock unlocks this process's descriptor without deleting the file.
func ReleaseRepoRunLock(repo string) {
	releasePIDLock(RepoRunLockPath(repo))
}

// ReadRepoRunLock parses the per-repository run lockfile.
func ReadRepoRunLock(repo string) (*RunLockInfo, error) {
	return readRunLockFile(RepoRunLockPath(repo))
}

// RepoRunLockHolder returns the live process that blocks a run on repo: the
// legacy global lock holder first, then the repo's own lock holder. Nil when
// nobody live holds either.
func RepoRunLockHolder(repo string) *RunLockInfo {
	if legacy, ok := liveLegacyRunLock(); ok {
		return legacy
	}
	if LockHeld(RepoRunLockPath(repo)) {
		info, err := ReadRepoRunLock(repo)
		if err != nil {
			return &RunLockInfo{Repo: repo}
		}
		return info
	}
	return nil
}

// AnyRunLockHeld reports whether the legacy global lock or any per-repository
// run lock is held by a live process, returning one holder.
func AnyRunLockHeld() (bool, *RunLockInfo) {
	return AnyRunLockHeldExcept(-1)
}

// AnyRunLockHeldExcept is AnyRunLockHeld ignoring locks owned by pid, so a
// process can check for other passes while holding locks of its own.
func AnyRunLockHeldExcept(pid int) (bool, *RunLockInfo) {
	if legacy, ok := liveLegacyRunLock(); ok && legacy.PID != pid {
		return true, legacy
	}
	matches, _ := filepath.Glob(filepath.Join(LockDir(), runLockPrefix+"*.lock"))
	for _, path := range matches {
		if LockHeld(path) {
			info, err := readRunLockFile(path)
			if err != nil {
				return true, &RunLockInfo{}
			}
			if info.PID != pid {
				return true, info
			}
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

// heldFiles is guarded by lockFileMu; descriptors stay open for the entire
// protected operation, not just while publishing the owner record.
var heldFiles = make(map[string]*os.File)

func acquireFileLock(path string, info any) error {
	lockFileMu.Lock()
	defer lockFileMu.Unlock()
	if heldFiles[path] != nil {
		return filelock.ErrHeld
	}
	f, err := filelock.Try(path, info)
	if err == nil {
		heldFiles[path] = f
	}
	return err
}

func acquirePIDLock(path string, info RunLockInfo) (*RunLockInfo, error) {
	err := acquireFileLock(path, info)
	if errors.Is(err, filelock.ErrHeld) {
		holder, readErr := readRunLockFile(path)
		if readErr != nil {
			holder = &RunLockInfo{}
		}
		return holder, err
	}
	return nil, err
}

func releasePIDLock(path string) {
	lockFileMu.Lock()
	defer lockFileMu.Unlock()
	if f := heldFiles[path]; f != nil {
		filelock.Release(f)
		delete(heldFiles, path)
	}
}
