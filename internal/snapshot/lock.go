package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

// lockFileMu protects the process-owned descriptor registry.
var lockFileMu sync.Mutex

// LockInfo is the JSON payload written to each lockfile.
type LockInfo struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Operator  string    `json:"operator"`
	Repo      string    `json:"repo"`
	Issue     int       `json:"issue"`
}

// LockDir returns ~/.clawflow/locks/.
func LockDir() string {
	return filepath.Join(StateDir(), "locks")
}

// LockPath returns the lockfile path for a (repo, issue) pair.
func LockPath(repo string, issueNum int) string {
	slug := strings.ReplaceAll(repo, "/", "__")
	return filepath.Join(LockDir(), slug, fmt.Sprintf("issue-%d.lock", issueNum))
}

// AcquireLock holds a kernel lock until ReleaseLock.
func AcquireLock(repo string, issueNum int, operatorName string) error {
	return acquireFileLock(LockPath(repo, issueNum), LockInfo{
		PID: os.Getpid(), StartedAt: time.Now().UTC(), Operator: operatorName, Repo: repo, Issue: issueNum,
	})
}

// ReleaseLock releases only a descriptor acquired by this process.
func ReleaseLock(repo string, issueNum int) { releasePIDLock(LockPath(repo, issueNum)) }

// IsLocked checks kernel ownership (or a live legacy PID).
func IsLocked(repo string, issueNum int) bool { return LockHeld(LockPath(repo, issueNum)) }

// LockHeld reports ownership without relying on the informational PID.
func LockHeld(path string) bool { return filelock.Held(path) }

// ReadLock parses a lockfile. Returns an error if the file doesn't exist
// or can't be parsed.
func ReadLock(path string) (*LockInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var info LockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// CleanStaleLocks is retained for callers; kernel locks need no reclamation.
// Persistent lockfiles must never be removed, even when their owner is dead.
func CleanStaleLocks() int { return 0 }

// processAlive checks whether a process with the given PID is still running.
// On Unix, sending signal 0 checks existence without affecting the process.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// RunLockInfo is the JSON payload written to the process-level run lockfile.
type RunLockInfo struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Version   string    `json:"version"`
	// Repo is set on per-repository run locks (run-<owner>__<name>.lock).
	// Absent on the legacy global lock, so old readers ignore it.
	Repo string `json:"repo,omitempty"`
}

// RunLockPath returns the path to the global run lockfile
// (~/.clawflow/locks/run.lock).
func RunLockPath() string {
	return filepath.Join(LockDir(), "run.lock")
}

// AcquireRunLock acquires the legacy global run lock.
func AcquireRunLock(version string) error {
	holder, err := acquirePIDLock(RunLockPath(), RunLockInfo{PID: os.Getpid(), StartedAt: time.Now().UTC(), Version: version})
	if holder != nil {
		return runLockHeldError(holder)
	}
	return err
}

// ReleaseRunLock unlocks and closes the owned descriptor.
func ReleaseRunLock() { releasePIDLock(RunLockPath()) }

// ReadRunLock parses the run lockfile. Returns an error if the file doesn't
// exist or can't be parsed.
func ReadRunLock() (*RunLockInfo, error) {
	data, err := os.ReadFile(RunLockPath())
	if err != nil {
		return nil, err
	}
	var info RunLockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}
