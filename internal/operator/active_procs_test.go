//go:build !windows

package operator

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestTerminateAllActiveCmdsKillsRegisteredGrandchild is the regression guard
// for issue #325: the run-level self-watchdog must be able to kill an
// in-flight claude subprocess (and its process-group descendants) before
// os.Exit, instead of orphaning it to keep running and writing to the VCS
// after the parent is gone.
func TestTerminateAllActiveCmdsKillsRegisteredGrandchild(t *testing.T) {
	orig := terminateGrace
	terminateGrace = 200 * time.Millisecond
	defer func() { terminateGrace = orig }()

	cmd := exec.Command("sh", "-c", "sleep 60 & echo $!; wait")
	setProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	registerActiveCmd(cmd)
	defer unregisterActiveCmd(cmd)

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatalf("did not read grandchild pid")
	}
	grandPID, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil {
		t.Fatalf("parse grandchild pid %q: %v", scanner.Text(), err)
	}
	if err := syscall.Kill(grandPID, 0); err != nil {
		t.Fatalf("grandchild %d not alive before terminate: %v", grandPID, err)
	}

	// This is the exact call the self-watchdog makes right before os.Exit —
	// it must block until the kill has actually landed, not just fire a
	// signal and return.
	TerminateAllActiveCmds()
	_ = stdout.Close()
	_ = cmd.Wait()

	if err := syscall.Kill(grandPID, 0); err == nil {
		_ = syscall.Kill(grandPID, syscall.SIGKILL)
		t.Fatalf("grandchild %d still alive after TerminateAllActiveCmds returned", grandPID)
	}
}

// TestTerminateAllActiveCmdsNoOpWhenEmpty guards against a panic/hang when
// the watchdog fires with nothing in flight (the common case — most runs
// finish well within budget).
func TestTerminateAllActiveCmdsNoOpWhenEmpty(t *testing.T) {
	TerminateAllActiveCmds() // must not panic or block
}

// TestRegisterUnregisterActiveCmd pins the bookkeeping contract:
// register makes a cmd visible to TerminateAllActiveCmds, unregister removes
// it (the normal path once RunClaude's own cmd.Wait has completed).
func TestRegisterUnregisterActiveCmd(t *testing.T) {
	cmd := exec.Command("true")
	registerActiveCmd(cmd)
	activeCmdsMu.Lock()
	_, tracked := activeCmds[cmd]
	activeCmdsMu.Unlock()
	if !tracked {
		t.Fatal("registerActiveCmd did not add cmd to activeCmds")
	}

	unregisterActiveCmd(cmd)
	activeCmdsMu.Lock()
	_, stillTracked := activeCmds[cmd]
	activeCmdsMu.Unlock()
	if stillTracked {
		t.Fatal("unregisterActiveCmd did not remove cmd from activeCmds")
	}
}
