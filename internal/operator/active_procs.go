package operator

import (
	"os/exec"
	"sync"
	"time"
)

// activeCmds tracks every claude subprocess currently running under this
// process, across both the operator runner and Pilot wakes (schedule.go
// calls RunClaude too — they share this package). It exists so the `clawflow
// run` self-watchdog (cmd/clawflow/commands/run.go) can terminate whatever is
// still in flight before it force-exits.
//
// Without this, os.Exit(1) leaves any spawned claude process (and its whole
// process group — see setProcessGroup) running as an orphan: it keeps
// writing to the VCS and burning tokens with no meta.json ever reaching a
// terminal status, since the parent that would have written it is gone
// (issue #325).
var (
	activeCmdsMu sync.Mutex
	activeCmds   = map[*exec.Cmd]struct{}{}
)

// registerActiveCmd records a started claude subprocess. Must be called only
// after cmd.Start() succeeds — Process is nil before that, and
// terminateProcessGroup is a no-op on a nil Process anyway, but registering
// pre-Start would create a window where TerminateAllActiveCmds reads Pid 0.
func registerActiveCmd(cmd *exec.Cmd) {
	activeCmdsMu.Lock()
	activeCmds[cmd] = struct{}{}
	activeCmdsMu.Unlock()
}

// unregisterActiveCmd removes a cmd once its wait/cleanup has completed
// (success, failure, or already terminated by the watchdog). Idempotent.
func unregisterActiveCmd(cmd *exec.Cmd) {
	activeCmdsMu.Lock()
	delete(activeCmds, cmd)
	activeCmdsMu.Unlock()
}

// terminateGrace is the SIGTERM→SIGKILL grace period TerminateAllActiveCmds
// waits out synchronously. Kept short (well under the watchdog's own log +
// exit overhead) since the caller is about to os.Exit either way — the goal
// is just giving claude a beat to flush stream-json before the hard kill,
// not a generous shutdown window.
var terminateGrace = 2 * time.Second

// TerminateAllActiveCmds signals every claude subprocess (and its process
// group) currently tracked, and blocks until each has been SIGTERM'd and
// (after a short grace) SIGKILL'd. Called by the run-level self-watchdog
// immediately before os.Exit — it must block synchronously rather than
// leaving the kill to a background goroutine (like the ctx-deadline path
// does), because os.Exit tears down the process image immediately and would
// abort any goroutine that hadn't finished the escalation yet, orphaning the
// child exactly as before (issue #325). Best-effort and safe to call with
// zero active commands. Multiple active commands are terminated concurrently
// so the grace period is paid once, not once per command.
func TerminateAllActiveCmds() {
	activeCmdsMu.Lock()
	cmds := make([]*exec.Cmd, 0, len(activeCmds))
	for cmd := range activeCmds {
		cmds = append(cmds, cmd)
	}
	activeCmdsMu.Unlock()
	if len(cmds) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, cmd := range cmds {
		wg.Add(1)
		go func(cmd *exec.Cmd) {
			defer wg.Done()
			terminateProcessGroupSync(cmd, terminateGrace)
		}(cmd)
	}
	wg.Wait()
}
