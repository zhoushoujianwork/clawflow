package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// errWorktreeInUse marks a recovery postponed because processes still use the
// worktree.
var errWorktreeInUse = errors.New("retained worktree is still in use by running processes")

const (
	worktreeScanTimeout = 5 * time.Second
	maxReportedHolders  = 5
	holderReasonCWD     = "working directory inside worktree"
)

type worktreeHolder struct {
	PID    int
	Name   string
	Reason string
}

// scanWorktreeHolders is a variable so tests can force scanner failures.
var scanWorktreeHolders = func(ctx context.Context, workspace string) ([]worktreeHolder, error) {
	if runtime.GOOS == "linux" {
		return scanProcHolders("/proc", workspace, os.Getpid())
	}
	return scanLsofHolders(ctx, workspace, os.Getpid())
}

// ensureWorktreeHasNoWriters returns nil only when no other process uses the
// canonical worktree path. Both a busy worktree and a failed inspection
// postpone recovery.
//
// Offload can only prove that the original native process group ended. A tool
// that detached with setsid(2) is outside that group, so before a retained
// worktree is handed to a resumed session, recovery asks the operating system
// which processes still use it. Nothing is signalled or killed: a busy
// worktree only postpones recovery and the next worker pass checks again.
//
// A process counts when its working directory is inside the worktree or when
// it holds a file inside the worktree open for writing. Read-only descriptors
// and executable mappings are ignored because they do not make a second
// writer; this keeps editors, indexers and file watchers from blocking
// recovery.
//
// Linux reads /proc/<pid>/{cwd,fd,fdinfo} and needs no external tool. macOS and
// other systems run lsof once for all processes, limited to cwd and numeric
// descriptors; the cost does not grow with the worktree size, because +D is
// never used. Only processes the worker user can inspect are visible, a
// process that has neither cwd nor an open file in the worktree is invisible,
// and a process may start between the scan and the resumed launch.
func ensureWorktreeHasNoWriters(ctx context.Context, workspace string) error {
	scanCtx, cancel := context.WithTimeout(ctx, worktreeScanTimeout)
	defer cancel()
	holders, err := scanWorktreeHolders(scanCtx, workspace)
	if err != nil {
		return fmt.Errorf("cannot inspect processes using worktree %s; recovery is postponed: %w", workspace, err)
	}
	if len(holders) == 0 {
		return nil
	}
	sort.Slice(holders, func(i, j int) bool { return holders[i].PID < holders[j].PID })
	parts := make([]string, 0, maxReportedHolders+1)
	for i, h := range holders {
		if i == maxReportedHolders {
			parts = append(parts, fmt.Sprintf("and %d more", len(holders)-maxReportedHolders))
			break
		}
		parts = append(parts, fmt.Sprintf("pid %d (%s): %s", h.PID, h.Name, h.Reason))
	}
	return fmt.Errorf("%w; recovery is postponed until they exit: %s", errWorktreeInUse, strings.Join(parts, "; "))
}

// insideWorktree reports whether p is root or a path below it. The separator
// check keeps /work/repo-old from matching /work/repo.
func insideWorktree(root, p string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimRight(root, "/")+"/")
}

func writeReason(root, p string) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(p, root), "/")
	if rel == "" {
		rel = "."
	}
	return "open for writing: " + rel
}

// findLsof also checks the usual system directories because a service manager
// may start the worker with a minimal PATH.
func findLsof() (string, error) {
	if p, err := exec.LookPath("lsof"); err == nil {
		return p, nil
	}
	for _, p := range []string{"/usr/sbin/lsof", "/usr/bin/lsof", "/sbin/lsof"} {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	return "", errors.New("lsof not found")
}

func scanLsofHolders(ctx context.Context, workspace string, self int) ([]worktreeHolder, error) {
	// -F pcfan: pid, command, descriptor, access mode, name. +c 0 keeps whole
	// command names. -w hides warnings about unreadable entries.
	bin, err := findLsof()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, "-nP", "-w", "+c", "0", "-F", "pcfan", "-d", "cwd,0-65535")
	cmd.Dir = string(filepath.Separator)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		// lsof exits 1 when it produced output but could not read some entries.
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 || stdout.Len() == 0 {
			if msg := strings.TrimSpace(stderr.String()); msg != "" {
				return nil, fmt.Errorf("lsof: %w: %s", err, msg)
			}
			return nil, fmt.Errorf("lsof: %w", err)
		}
	}
	return parseLsofHolders(stdout.Bytes(), workspace, self), nil
}

// parseLsofHolders reads lsof -F output. A process record starts with "p"; each
// file inside it starts with "f" and is complete at the next "f", "p" or end.
func parseLsofHolders(out []byte, workspace string, self int) []worktreeHolder {
	found := map[int]worktreeHolder{}
	var (
		pid                int
		command            string
		fd, access, target string
	)
	flush := func() {
		defer func() { fd, access, target = "", "", "" }()
		if pid == 0 || pid == self || target == "" || !insideWorktree(workspace, target) {
			return
		}
		var reason string
		switch {
		case fd == "cwd":
			reason = holderReasonCWD
		case access == "w" || access == "u":
			reason = writeReason(workspace, target)
		default:
			return
		}
		if prior, ok := found[pid]; ok && prior.Reason == holderReasonCWD {
			return
		}
		found[pid] = worktreeHolder{PID: pid, Name: command, Reason: reason}
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		value := line[1:]
		switch line[0] {
		case 'p':
			flush()
			pid, _ = strconv.Atoi(value)
			command = ""
		case 'c':
			command = value
		case 'f':
			flush()
			fd = value
		case 'a':
			access = value
		case 'n':
			target = value
		}
	}
	flush()
	holders := make([]worktreeHolder, 0, len(found))
	for _, h := range found {
		holders = append(holders, h)
	}
	return holders
}

// scanProcHolders walks a Linux-style proc tree. Entries that disappear or
// cannot be read (other users' processes, exited processes) are skipped; only
// failure to list the tree itself is an error.
func scanProcHolders(procRoot, workspace string, self int) ([]worktreeHolder, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	var holders []worktreeHolder
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == self {
			continue
		}
		dir := filepath.Join(procRoot, entry.Name())
		if cwd, err := os.Readlink(filepath.Join(dir, "cwd")); err == nil && insideWorktree(workspace, cwd) {
			holders = append(holders, worktreeHolder{PID: pid, Name: procName(dir), Reason: holderReasonCWD})
			continue
		}
		fds, err := os.ReadDir(filepath.Join(dir, "fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(dir, "fd", fd.Name()))
			if err != nil || !insideWorktree(workspace, target) || !procFDWritable(filepath.Join(dir, "fdinfo", fd.Name())) {
				continue
			}
			holders = append(holders, worktreeHolder{PID: pid, Name: procName(dir), Reason: writeReason(workspace, target)})
			break
		}
	}
	return holders, nil
}

func procName(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "comm"))
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		return "unknown"
	}
	return strings.TrimSpace(string(raw))
}

// procFDWritable reads the open flags of a descriptor from fdinfo ("flags:" is
// octal). Unreadable fdinfo counts as writable so an unknown mode fails closed.
func procFDWritable(fdinfo string) bool {
	raw, err := os.ReadFile(fdinfo)
	if err != nil {
		return true
	}
	for _, line := range strings.Split(string(raw), "\n") {
		value, ok := strings.CutPrefix(line, "flags:")
		if !ok {
			continue
		}
		flags, err := strconv.ParseUint(strings.TrimSpace(value), 8, 64)
		if err != nil {
			return true
		}
		// O_ACCMODE is 3; O_WRONLY is 1 and O_RDWR is 2.
		mode := flags & 0o3
		return mode == 0o1 || mode == 0o2
	}
	return true
}
