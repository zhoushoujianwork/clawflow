//go:build darwin || linux

package candidate

import (
	"os/exec"
	"strconv"
	"strings"
)

// commandOf returns the executable path and arguments of pid, or "".
func commandOf(pid int) string {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
