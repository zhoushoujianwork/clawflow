//go:build darwin || linux

package execution

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestOwnershipExitHelper(t *testing.T) {
	if os.Getenv("CLAWFLOW_OWNERSHIP_HELPER") != "1" {
		return
	}
	// Deliberately omit release, including during process exit.
	release, err := acquireOwnership(os.Getenv("CLAWFLOW_OWNERSHIP_WORKSPACE"), "profile", "native-session", true)
	if err != nil {
		os.Exit(2)
	}
	fmt.Fprintln(os.Stdout, "ready")
	var input [1]byte
	_, _ = os.Stdin.Read(input[:])
	runtime.KeepAlive(release)
	os.Exit(0)
}

func TestOwnershipReleasedOnProcessExitWithoutUnlink(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestOwnershipExitHelper$")
	cmd.Env = append(os.Environ(), "CLAWFLOW_OWNERSHIP_HELPER=1", "CLAWFLOW_OWNERSHIP_WORKSPACE="+workspace)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("ready=%q err=%v", line, err)
	}
	if release, err := acquireOwnership(workspace, "profile", "native-session", true); !IsKind(err, "session_busy") {
		if release != nil {
			release()
		}
		t.Fatalf("concurrent workspace owner: %v", err)
	}
	other := t.TempDir()
	if release, err := acquireOwnership(other, "profile", "native-session", true); !IsKind(err, "session_busy") {
		if release != nil {
			release()
		}
		t.Fatalf("concurrent session owner: %v", err)
	}
	// A failed multi-key acquisition must release its earlier workspace lock.
	release, err := acquireOwnership(other, "profile", "other-session", true)
	if err != nil {
		t.Fatal(err)
	}
	release()
	dir := filepath.Join(os.Getenv("HOME"), ".clawflow", "execution-locks")
	paths, err := filepath.Glob(filepath.Join(dir, "*.lock"))
	if err != nil {
		t.Fatal(err)
	}
	before := map[string]os.FileInfo{}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = info
	}
	_ = input.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	release, err = acquireOwnership(workspace, "profile", "native-session", true)
	if err != nil {
		t.Fatalf("exited owner left permanent exclusion: %v", err)
	}
	release()
	for path, info := range before {
		after, err := os.Stat(path)
		if err != nil || !os.SameFile(info, after) {
			t.Fatalf("lock inode replaced: %s %v", path, err)
		}
	}
}
