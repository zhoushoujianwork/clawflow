//go:build windows

package execution

import (
	"os/exec"
	"time"
)

// Windows has no POSIX process groups. Cancellation kills the owned direct
// child as a best effort; group cleanup is supported on the release POSIX platforms.
func setProcessGroup(cmd *exec.Cmd) {}

func terminateProcessGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// terminateProcessGroupSync mirrors the direct-child fallback on Windows.
func terminateProcessGroupSync(cmd *exec.Cmd, grace time.Duration) { terminateProcessGroup(cmd) }
