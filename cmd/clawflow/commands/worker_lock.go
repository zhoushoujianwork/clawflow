package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// workerPIDPath is the single-instance lock file. Keeping it under
// ~/.clawflow alongside the worker.yaml config makes it trivial for the
// user to find and nuke if something really goes sideways.
func workerPIDPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "worker.pid")
}

// acquireWorkerLock refuses to start if another live worker is already
// holding the pidfile. A stale file (process gone) is silently replaced.
func acquireWorkerLock() error {
	path := workerPIDPath()
	if data, err := os.ReadFile(path); err == nil {
		if pid, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil && processAlive(pid) {
			return fmt.Errorf("worker already running (pid=%d) — stop it first: clawflow worker stop", pid)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// releaseWorkerLock removes the pidfile. Safe to call even if it doesn't
// exist (clean shutdown races with a manual delete).
func releaseWorkerLock() {
	_ = os.Remove(workerPIDPath())
}

// readWorkerPID returns the pid of a live worker process on this host, if
// any. Missing / stale pidfiles return (0, false).
func readWorkerPID() (int, bool) {
	data, err := os.ReadFile(workerPIDPath())
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || !processAlive(pid) {
		return 0, false
	}
	return pid, true
}

// processAlive probes pid via signal 0 — delivers nothing, but fails with
// ESRCH if the process is gone. POSIX-only; fine since the CLI targets
// darwin/linux.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// stopWorkerProcess sends SIGTERM, waits up to 5s, then SIGKILL if the
// process hasn't exited. Returns true if the worker was actually running.
func stopWorkerProcess() (stopped bool, err error) {
	pid, ok := readWorkerPID()
	if !ok {
		return false, nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	fmt.Printf("sending SIGTERM to worker pid=%d...\n", pid)
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return false, err
	}
	for i := 0; i < 50; i++ {
		time.Sleep(100 * time.Millisecond)
		if !processAlive(pid) {
			releaseWorkerLock()
			fmt.Println("worker stopped.")
			return true, nil
		}
	}
	fmt.Println("still alive after 5s, sending SIGKILL...")
	_ = proc.Signal(syscall.SIGKILL)
	releaseWorkerLock()
	return true, nil
}
