//go:build !windows

package execution

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSetProcessGroupSetsSetpgid(t *testing.T) {
	cmd := exec.Command("true")
	setProcessGroup(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("expected Setpgid=true, got %+v", cmd.SysProcAttr)
	}
}

// TestTerminateProcessGroupKillsGrandchild reproduces the issue #213 scenario:
// claude (here a shell) forks a long-running grandchild (here `sleep`) and then
// blocks. Killing only the direct child would orphan the sleep; the process-group
// kill must take the whole subtree down.
func TestTerminateProcessGroupKillsGrandchild(t *testing.T) {
	// Print the grandchild PID, then block so the direct child stays alive
	// (mirrors claude waiting on a hung tool call).
	cmd := exec.Command("sh", "-c", "sleep 60 & echo $!; wait")
	setProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("did not read grandchild pid")
	}
	grandPID, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil {
		t.Fatalf("parse grandchild pid %q: %v", scanner.Text(), err)
	}

	// Sanity: the grandchild is alive before we terminate.
	if err := syscall.Kill(grandPID, 0); err != nil {
		t.Fatalf("grandchild %d not alive before terminate: %v", grandPID, err)
	}

	terminateProcessGroup(cmd)
	_ = stdout.Close()
	_ = cmd.Wait()

	// terminateProcessGroup escalates to SIGKILL after a 3s grace; give it room.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(grandPID, 0); err != nil {
			return // grandchild reaped — success
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Best-effort cleanup if the test is about to fail.
	_ = syscall.Kill(grandPID, syscall.SIGKILL)
	t.Fatalf("grandchild %d still alive after terminateProcessGroup", grandPID)
}

func TestOffloadTerminalCleansRemainingGroup(t *testing.T) {
	c, req := fixture(t)
	orig := terminateGrace
	terminateGrace = 100 * time.Millisecond
	defer func() { terminateGrace = orig }()
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("OFFLOAD_TEST_CHILD_PID", pidPath)
	t.Setenv("OFFLOAD_TEST_MODE", "descendant")
	_, err := Run(context.Background(), c, req, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	t.Fatal("native descendant survived successful terminal result")
}
