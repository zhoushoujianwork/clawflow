//go:build !windows

package operator

import (
	"os/exec"
	"syscall"
	"time"
)

// setProcessGroup puts the claude subprocess in its own process group so the
// whole subtree (claude plus any Bash tool grandchildren it forks, e.g.
// `git fetch` / `clawflow issue list`) can be signalled at once via a
// negative PID. Without this, exec.CommandContext's deadline kill only
// reaches the direct child; orphaned grandchildren keep the stdout pipe open
// and hang the run far past its timeout (issue #213).
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// terminateProcessGroup signals the claude process group: SIGTERM first so
// claude can flush its stream-json (keeping the run's events.jsonl useful for
// diagnosis), then SIGKILL after a short grace for anything that ignored it.
// Safe to call once Start has succeeded; a no-op otherwise. Mirrors the
// killGroup pattern already used in internal/pty/server.go. The SIGKILL
// escalation runs on its own goroutine because the caller (the ctx-deadline
// guard in runClaudeWithProvider) is already blocked in cmd.Wait(), so it can
// afford to let this return immediately.
func terminateProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid <= 0 {
		return
	}
	// Negative PID targets the whole group (setProcessGroup made claude the
	// group leader, so its PID equals the PGID).
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		// Group signal not permitted (Setpgid didn't take?) — fall back to
		// the single process so we still try our best.
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	go func() {
		time.Sleep(3 * time.Second)
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
			_ = cmd.Process.Kill()
		}
	}()
}

// terminateProcessGroupSync is terminateProcessGroup's blocking twin: SIGTERM
// the group, sleep out the grace period IN THIS GOROUTINE, then SIGKILL
// anything still alive — and only then return. Callers about to os.Exit
// (the run-level self-watchdog, issue #325) cannot use the async escalation
// above: if SIGTERM alone doesn't finish the job before the process image is
// replaced, the SIGKILL goroutine never runs and the child is orphaned —
// exactly the bug this function exists to close.
func terminateProcessGroupSync(cmd *exec.Cmd, grace time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	time.Sleep(grace)
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
