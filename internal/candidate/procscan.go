package candidate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/devmode"
)

// scannedProcess is a running process that belongs to a candidate root.
type scannedProcess struct {
	PID  int
	Name string
}

// processesUnder lists processes (other than this one) whose command line names
// root or whose working directory lies inside it. The working directory catches
// children that do not mention the root on their command line and processes that
// detached from the service's process group, which is how a leftover agent or
// git process would survive a stop.
func processesUnder(root string) []scannedProcess {
	real := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		real = r
	}
	found := map[int]scannedProcess{}
	self := os.Getpid()
	if out, err := exec.Command("ps", "-axo", "pid=,command=").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 || (!strings.Contains(line, root) && !strings.Contains(line, real)) {
				continue
			}
			if pid, err := strconv.Atoi(fields[0]); err == nil && pid != self {
				found[pid] = scannedProcess{PID: pid, Name: filepath.Base(fields[1])}
			}
		}
	}
	for pid, cwd := range workingDirectories() {
		if pid == self || !(devmode.Within(real, cwd) || devmode.Within(root, cwd)) {
			continue
		}
		if _, ok := found[pid]; !ok {
			found[pid] = scannedProcess{PID: pid, Name: commandName(pid)}
		}
	}
	out := make([]scannedProcess, 0, len(found))
	for _, p := range found {
		out = append(out, p)
	}
	return out
}

// workingDirectories maps pid to its current directory, using /proc where it
// exists and lsof otherwise. Processes that cannot be inspected are skipped.
func workingDirectories() map[int]string {
	dirs := map[int]string{}
	if entries, err := os.ReadDir("/proc"); err == nil {
		for _, e := range entries {
			pid, convErr := strconv.Atoi(e.Name())
			if convErr != nil {
				continue
			}
			if cwd, err := os.Readlink(filepath.Join("/proc", e.Name(), "cwd")); err == nil {
				dirs[pid] = cwd
			}
		}
		return dirs
	}
	out, err := exec.Command("lsof", "-a", "-d", "cwd", "-F", "pn").Output()
	if err != nil && len(out) == 0 {
		return dirs
	}
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "n") && pid > 0:
			dirs[pid] = line[1:]
		}
	}
	return dirs
}

func commandName(pid int) string {
	if c := commandOf(pid); c != "" {
		if f := strings.Fields(c); len(f) > 0 {
			return filepath.Base(f[0])
		}
	}
	return "process"
}
