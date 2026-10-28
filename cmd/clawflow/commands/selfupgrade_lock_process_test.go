package commands

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

var lockKinds = []string{"upgrade"}

func processLockPath(string) string { return selfUpgradeLockPath() }
func processLock(string) (func(), error) {
	release, ok, err := acquireSelfUpgradeLock()
	if err == nil && !ok {
		err = fmt.Errorf("busy")
	}
	return release, err
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
