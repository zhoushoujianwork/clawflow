//go:build windows

package operator

import (
	"os/exec"
	"time"
)

// setProcessGroup / terminateProcessGroup are no-ops on platforms without
// POSIX process groups. There, exec.CommandContext's own kill of the direct
// child remains the only deadline-enforcement mechanism.
func setProcessGroup(cmd *exec.Cmd) {}

func terminateProcessGroup(cmd *exec.Cmd) {}

// terminateProcessGroupSync mirrors terminateProcessGroup: a no-op on
// platforms without POSIX process groups.
func terminateProcessGroupSync(cmd *exec.Cmd, grace time.Duration) {}
