package commands

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// workerLogPath is where the detached worker writes its stdout/stderr.
// Keeping it next to worker.pid means `clawflow worker logs` doesn't need
// any config lookup.
func workerLogPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "worker.log")
}

// Sentinel env var the re-exec'd child recognizes: "you're the daemon, run
// the worker loop in-process instead of spawning again."
const daemonEnvVar = "CLAWFLOW_WORKER_DAEMONIZED"

// spawnWorkerDaemon re-execs the current binary with the same args but
// detached from the controlling terminal, redirecting stdout+stderr to
// the log file. Returns after the child has been launched — the parent
// should exit nil so the user gets their shell back.
func spawnWorkerDaemon() error {
	// Cheap pre-flight: if another live worker already holds the lock,
	// don't even fork — print the same error the child would have.
	if pid, ok := readWorkerPID(); ok {
		return fmt.Errorf("worker already running (pid=%d) — stop it first: clawflow worker stop", pid)
	}

	logPath := workerLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("mkdir log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer logFile.Close()

	exe, err := os.Executable()
	if err != nil {
		return err
	}

	// Propagate the same CLI args so flags like --poll-interval persist.
	c := exec.Command(exe, os.Args[1:]...)
	c.Env = append(os.Environ(), daemonEnvVar+"=1")
	c.Stdin = nil
	c.Stdout = logFile
	c.Stderr = logFile
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := c.Start(); err != nil {
		return fmt.Errorf("spawn daemon: %w", err)
	}
	pid := c.Process.Pid
	// Release so the child is orphaned cleanly — we don't want Wait().
	_ = c.Process.Release()

	// Give the child up to 2s to acquire the pidfile, so that an immediate
	// follow-up `clawflow worker status` sees "running" instead of a race.
	for i := 0; i < 20; i++ {
		time.Sleep(100 * time.Millisecond)
		if p, ok := readWorkerPID(); ok && p == pid {
			break
		}
	}

	fmt.Printf("worker started in background (pid=%d)\n", pid)
	fmt.Printf("  logs:  clawflow worker logs     (file: %s)\n", logPath)
	fmt.Printf("  stop:  clawflow worker stop\n")
	return nil
}

// isDaemonChild reports whether the current process is the re-exec'd
// daemon copy (vs the user-invoked parent).
func isDaemonChild() bool {
	return os.Getenv(daemonEnvVar) == "1"
}

// tailWorkerLog prints the last nLines of the worker log, then optionally
// streams new lines (tail -f style) until Ctrl+C.
func tailWorkerLog(nLines int, follow bool) error {
	logPath := workerLogPath()
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("no worker log yet at %s\n", logPath)
			return nil
		}
		return err
	}
	defer f.Close()

	// Dump the last nLines first.
	if nLines > 0 {
		if err := printLastLines(f, nLines); err != nil {
			return err
		}
	}

	if !follow {
		return nil
	}

	// Seek to end for the follow loop.
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(stop)

	buf := make([]byte, 8192)
	for {
		select {
		case <-stop:
			fmt.Println()
			return nil
		default:
		}
		n, err := f.Read(buf)
		if n > 0 {
			_, _ = os.Stdout.Write(buf[:n])
		}
		if err == io.EOF {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if err != nil {
			return err
		}
	}
}

// printLastLines reads the file and prints its last n lines. For a log
// that's a few MB, it's fine to slurp and split; we don't need the
// backwards-seek tricks real tail uses. Resets the read cursor to the
// caller's current position on exit so the subsequent follow loop starts
// from the true end.
func printLastLines(f *os.File, n int) error {
	cur, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	// Restore cursor so the caller's Seek(End) semantics aren't disturbed.
	if _, err := f.Seek(cur, io.SeekStart); err != nil {
		return err
	}

	// Split on '\n' and print the last n non-empty trailing entries. If the
	// file ends without a newline, the final partial line still gets shown.
	start := len(data)
	count := 0
	for i := len(data) - 1; i >= 0 && count < n; i-- {
		if data[i] == '\n' {
			if i < len(data)-1 {
				count++
			}
			if count == n {
				start = i + 1
				break
			}
		}
		if i == 0 {
			start = 0
		}
	}
	_, _ = os.Stdout.Write(data[start:])
	return nil
}
