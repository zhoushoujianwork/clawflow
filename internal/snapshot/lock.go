package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

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
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "locks")
}

// LockPath returns the lockfile path for a (repo, issue) pair.
func LockPath(repo string, issueNum int) string {
	slug := strings.ReplaceAll(repo, "/", "__")
	return filepath.Join(LockDir(), slug, fmt.Sprintf("issue-%d.lock", issueNum))
}

// AcquireLock creates a lockfile for the given (repo, issue). Returns nil
// on success, an error if the lock is already held by a live process.
// Stale locks (owner PID is dead) are automatically reclaimed.
func AcquireLock(repo string, issueNum int, operatorName string) error {
	path := LockPath(repo, issueNum)

	if info, err := ReadLock(path); err == nil {
		if processAlive(info.PID) {
			return fmt.Errorf("locked by PID %d (operator %s, since %s)",
				info.PID, info.Operator, info.StartedAt.Format(time.RFC3339))
		}
		// Stale lock — owner is dead, reclaim it.
		_ = os.Remove(path)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir lock dir: %w", err)
	}

	info := LockInfo{
		PID:       os.Getpid(),
		StartedAt: time.Now().UTC(),
		Operator:  operatorName,
		Repo:      repo,
		Issue:     issueNum,
	}
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// ReleaseLock removes the lockfile for the given (repo, issue). No-op if
// the file doesn't exist.
func ReleaseLock(repo string, issueNum int) {
	_ = os.Remove(LockPath(repo, issueNum))
}

// IsLocked reports whether a live lock exists for the given (repo, issue).
// Returns false if the lockfile is missing or the owner PID is dead.
func IsLocked(repo string, issueNum int) bool {
	info, err := ReadLock(LockPath(repo, issueNum))
	if err != nil {
		return false
	}
	return processAlive(info.PID)
}

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

// CleanStaleLocks walks the locks directory and removes any lockfile whose
// owner PID is no longer running. Called by ReconcileStaleRuns.
func CleanStaleLocks() int {
	lockDir := LockDir()
	if _, err := os.Stat(lockDir); os.IsNotExist(err) {
		return 0
	}
	cleaned := 0
	_ = filepath.WalkDir(lockDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".lock" {
			return nil
		}
		info, err := ReadLock(path)
		if err != nil {
			_ = os.Remove(path)
			cleaned++
			return nil
		}
		if !processAlive(info.PID) {
			_ = os.Remove(path)
			cleaned++
		}
		return nil
	})
	return cleaned
}

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
