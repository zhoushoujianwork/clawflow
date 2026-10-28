package snapshot

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var lockKinds = []string{"issue", "run", "repo", "snapshot", "config", "pilot"}

func processLockPath(kind string) string {
	switch kind {
	case "issue":
		return LockPath("o/r", 1)
	case "run":
		return RunLockPath()
	case "repo":
		return RepoRunLockPath("o/r")
	case "snapshot", "snapshot-wait":
		return SnapshotLockPath()
	case "config", "config-wait":
		return ConfigSyncLockPath()
	default:
		return PilotScheduleLockPath()
	}
}
func processLock(kind string) (func(), error) {
	switch kind {
	case "snapshot-wait", "config-wait":
		path := processLockPath(kind)
		return func() { releasePIDLock(path) }, acquireWaiting(path, 5*time.Second)
	case "issue":
		return func() { ReleaseLock("o/r", 1) }, AcquireLock("o/r", 1, "implement")
	case "run":
		return ReleaseRunLock, AcquireRunLock("test")
	case "repo":
		return func() { ReleaseRepoRunLock("o/r") }, AcquireRepoRunLock("o/r", "test")
	default:
		path := processLockPath(kind)
		_, err := acquirePIDLock(path, RunLockInfo{PID: os.Getpid(), StartedAt: time.Now()})
		return func() { releasePIDLock(path) }, err
	}
}

// TestLockProcessHelper is re-executed with pipes as explicit barriers.
func TestLockProcessHelper(t *testing.T) {
	kind := os.Getenv("CLAWFLOW_LOCK_HELPER")
	if kind == "" {
		return
	}
	var signal [1]byte
	if _, err := io.ReadFull(os.Stdin, signal[:]); err != nil {
		os.Exit(2)
	}
	if os.Getenv("CLAWFLOW_WAIT_READY") == "1" {
		fmt.Println("waiting")
	}
	release, err := processLock(kind)
	if err != nil {
		fmt.Println("busy")
		os.Exit(0)
	}
	fmt.Println("ready")
	if _, err := io.ReadFull(os.Stdin, signal[:]); err != nil {
		os.Exit(3)
	}
	release()
	os.Exit(0)
}

func TestLockCrossProcess(t *testing.T) {
	for _, kind := range lockKinds {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			for _, kill := range []bool{false, true} {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLockProcessHelper$")
				cmd.Env = append(os.Environ(), "CLAWFLOW_LOCK_HELPER="+kind, "GORACE=atexit_sleep_ms=0")
				stdin, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				cmd.Stderr = os.Stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { cancel(); _ = cmd.Process.Kill() })
				_, _ = stdin.Write([]byte{1})
				line, err := bufio.NewReader(stdout).ReadString('\n')
				if err != nil || line != "ready\n" {
					t.Fatalf("helper: %q %v", line, err)
				}
				if release, err := processLock(kind); err == nil {
					release()
					t.Fatal("second process acquired held lock")
				}
				if kill {
					_ = cmd.Process.Kill()
				} else {
					_, _ = stdin.Write([]byte{1})
				}
				err = cmd.Wait()
				cancel()
				if !kill && err != nil {
					t.Fatal(err)
				}
				release, err := processLock(kind)
				if err != nil {
					t.Fatalf("acquire after exit (kill=%v): %v", kill, err)
				}
				release()
			}
		})
	}
}

func TestLockConcurrentProcesses(t *testing.T) {
	for _, kind := range lockKinds {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			// Seed an obsolete owner; no process is allowed to unlink its inode.
			path := processLockPath(kind)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(`{"pid":2999999}`), 0644); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Stat(path)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			var cmds []*exec.Cmd
			var inputs []io.WriteCloser
			var readers []*bufio.Reader
			for i := 0; i < 4; i++ {
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLockProcessHelper$")
				cmd.Env = append(os.Environ(), "CLAWFLOW_LOCK_HELPER="+kind, "GORACE=atexit_sleep_ms=0")
				input, _ := cmd.StdinPipe()
				output, _ := cmd.StdoutPipe()
				cmd.Stderr = os.Stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				cmds = append(cmds, cmd)
				inputs = append(inputs, input)
				readers = append(readers, bufio.NewReader(output))
			}
			for _, input := range inputs {
				_, _ = input.Write([]byte{1})
			}
			winners := 0
			for _, reader := range readers {
				line, err := reader.ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				if line == "ready\n" {
					winners++
				} else if line != "busy\n" {
					t.Fatalf("unexpected %q", line)
				}
			}
			for _, input := range inputs {
				_, _ = input.Write([]byte{1})
				_ = input.Close()
			}
			for _, cmd := range cmds {
				if err := cmd.Wait(); err != nil {
					t.Fatal(err)
				}
			}
			if winners != 1 {
				t.Fatalf("owners=%d, want 1", winners)
			}
			after, _ := os.Stat(path)
			if !os.SameFile(before, after) {
				t.Fatal("lock inode was replaced")
			}
		})
	}
}

func TestLockAllKindsGoroutines(t *testing.T) {
	for _, kind := range lockKinds {
		t.Run(kind, func(t *testing.T) {
			testConcurrentLock(t, func() string { return processLockPath(kind) },
				func() error { _, err := processLock(kind); return err },
				func() { releasePIDLock(processLockPath(kind)) })
		})
	}
}

// Waiting subprocesses must wake after release without replacing the inode.
func TestLockWaitingProcess(t *testing.T) {
	for _, kind := range []string{"snapshot", "config"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			release, err := processLock(kind)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLockProcessHelper$")
			cmd.Env = append(os.Environ(), "CLAWFLOW_LOCK_HELPER="+kind+"-wait", "CLAWFLOW_WAIT_READY=1", "GORACE=atexit_sleep_ms=0")
			input, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			_, _ = input.Write([]byte{1})
			reader := bufio.NewReader(output)
			if line, err := reader.ReadString('\n'); err != nil || line != "waiting\n" {
				t.Fatalf("waiting: %q %v", line, err)
			}
			ready := make(chan string, 1)
			go func() { line, _ := reader.ReadString('\n'); ready <- line }()
			select {
			case line := <-ready:
				t.Fatalf("acquired before release: %q", line)
			case <-time.After(50 * time.Millisecond):
			}
			release()
			select {
			case line := <-ready:
				if line != "ready\n" {
					t.Fatalf("acquire: %q", line)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if release, err := processLock(kind); err == nil {
				release()
				t.Fatal("waiter did not hold lock")
			}
			_, _ = input.Write([]byte{1})
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLockWaitingGoroutines(t *testing.T) {
	for _, kind := range []string{"snapshot", "config"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			var active atomic.Int32
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					fn := func() {
						if active.Add(1) != 1 {
							t.Error("overlapping owners")
						}
						time.Sleep(time.Millisecond)
						active.Add(-1)
					}
					if kind == "snapshot" {
						if err := WithSnapshotLock(func() error { fn(); return nil }); err != nil {
							t.Error(err)
						}
					} else if !WithConfigSyncLock(5*time.Second, fn) {
						t.Error("config wait failed")
					}
				}()
			}
			wg.Wait()
		})
	}
}

func TestLockConfigSyncNoWait(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	called := false
	if !WithConfigSyncLock(0, func() {
		called = true
		if WithConfigSyncLock(0, func() { t.Error("overlapping owner") }) {
			t.Error("acquired held lock")
		}
	}) || !called {
		t.Fatal("free lock not acquired")
	}
}
