package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	// errWorktreeWriters marks a recovery postponed because a process outside
	// the original process group is still writing in the retained worktree.
	errWorktreeWriters = errors.New("retained worktree has running writers outside the original process group")
	// errWorktreeWritersExhausted marks the end of the bounded wait. Automatic
	// recovery stops scanning and waiting; an operator decides what happens next.
	errWorktreeWritersExhausted = errors.New("retained worktree still has running writers after the wait limit")
)

const (
	worktreeWriterWaitFile   = "worktree-writers.json"
	worktreeScanTimeout      = 5 * time.Second
	maxReportedWorktreeProcs = 5
)

// worktreeWriterWaitLimit bounds how long recovery is postponed for one retained
// worktree, counted from the first scan that found a writer. It is a variable
// so tests can shorten it.
//
// The coordinator's recovery_stalled watchdog rule reports a task that stays
// recovery_pending 30 minutes after its lease expired. The first scan runs on
// the next worker pass after expiry, so this limit ends well before that rule
// fires and the stuck task is reported rather than retried silently.
var worktreeWriterWaitLimit = 15 * time.Minute

// worktreeProcess is a process found using the worktree. Writes is the
// worktree-relative path of one regular file it holds open for writing; it is
// empty when the process only has its working directory there.
type worktreeProcess struct {
	PID     int
	Command string
	Writes  string
}

// hostWorktreeProcesses asks the operating system which processes use the
// worktree: /proc on Linux, lsof elsewhere.
func hostWorktreeProcesses(ctx context.Context, workspace string) ([]worktreeProcess, error) {
	// Both scanners report resolved paths, so a worktree reached through a
	// symlink (macOS /tmp, /var) would otherwise never match and hide writers.
	if resolved, err := filepath.EvalSymlinks(workspace); err == nil {
		workspace = resolved
	}
	if runtime.GOOS == "linux" {
		return scanProcWorktree("/proc", workspace, os.Getpid())
	}
	return scanLsofWorktree(ctx, workspace, os.Getpid())
}

// scanWorktreeProcesses is a variable so tests can supply scan results.
var scanWorktreeProcesses = hostWorktreeProcesses

type explicitRecoveryKey struct{}

// withExplicitRecovery marks a recovery requested by an operator through
// "worker recover". It re-scans even after the automatic wait was exhausted.
func withExplicitRecovery(ctx context.Context) context.Context {
	return context.WithValue(ctx, explicitRecoveryKey{}, true)
}

type worktreeWaitState struct {
	FirstSeen time.Time `json:"first_seen"`
	Summary   string    `json:"summary"`
}

// ensureNoWorktreeWriters returns nil when no other process is writing in the
// retained worktree. The journal's task directory keeps the wait clock.
//
// Offload can only prove that the original native process group ended. A tool
// that detached with setsid(2) is outside that group, so before a retained
// worktree is handed to a resumed session, recovery asks the operating system
// which processes still use it. Nothing is signalled or killed.
//
//   - A writer is a process holding a regular file inside the worktree open for
//     writing. It postpones recovery with the pid, command and file, until it
//     exits or worktreeWriterWaitLimit has passed since it was first seen. After
//     that the automatic path stops scanning and returns
//     errWorktreeWritersExhausted on every pass, leaving the task
//     recovery_pending for the watchdog to report. "clawflow worker recover"
//     scans again and proceeds once the writers are gone.
//   - A process that only has its working directory in the worktree (an idle
//     shell, an editor, a file watcher) does not postpone recovery. It is
//     logged as a warning.
//   - A scan that fails postpones recovery under the same limit.
//
// Known limits, none of which this check can close:
//   - A process that writes through a writable mmap and has already closed the
//     descriptor holds no file open and is not detected.
//   - A process that opens, writes and closes files in bursts is only seen if
//     the scan lands inside a burst, and a process started after the scan is
//     not seen at all.
//   - Only processes the worker user may inspect are visible: /proc/<pid>/fd on
//     Linux and lsof without root on macOS skip other users' processes.
//
// Cost: Linux reads /proc/<pid>/{cwd,fd,fdinfo} for every visible process and
// needs no external tool. macOS runs lsof once for all processes, limited to
// cwd and numeric descriptors (no +D tree walk, so the cost does not depend on
// the worktree size).
func ensureNoWorktreeWriters(ctx context.Context, workspace, journalPath string) error {
	statePath := filepath.Join(filepath.Dir(journalPath), worktreeWriterWaitFile)
	state := readWorktreeWaitState(statePath)
	explicit, _ := ctx.Value(explicitRecoveryKey{}).(bool)
	if state != nil && !explicit && !time.Now().Before(state.FirstSeen.Add(worktreeWriterWaitLimit)) {
		return exhaustedWorktreeWriters(state, journalPath)
	}

	scanCtx, cancel := context.WithTimeout(ctx, worktreeScanTimeout)
	defer cancel()
	procs, scanErr := scanWorktreeProcesses(scanCtx, workspace)

	var summary string
	switch {
	case scanErr != nil:
		summary = fmt.Sprintf("cannot inspect running processes (%v); worktree %s", scanErr, workspace)
	default:
		writers, idle := splitWorktreeProcesses(procs)
		if len(writers) == 0 {
			if err := os.Remove(statePath); err != nil && !os.IsNotExist(err) {
				return err
			}
			if len(idle) > 0 {
				slog.Warn("worker/recovery-worktree-cwd", "worktree", workspace,
					"processes", describeWorktreeProcesses(idle),
					"note", "only the working directory is inside the worktree; recovery continues")
			}
			return nil
		}
		summary = describeWorktreeProcesses(writers)
	}

	if state == nil {
		state = &worktreeWaitState{FirstSeen: time.Now().UTC()}
	}
	if state.Summary != summary {
		state.Summary = summary
		if err := writeWorktreeWaitState(statePath, state); err != nil {
			return fmt.Errorf("record worktree writer wait: %w", err)
		}
	}
	deadline := state.FirstSeen.Add(worktreeWriterWaitLimit)
	if !time.Now().Before(deadline) {
		return exhaustedWorktreeWriters(state, journalPath)
	}
	return fmt.Errorf("%w; recovery is postponed until they exit, or until %s when automatic recovery stops waiting: %s",
		errWorktreeWriters, deadline.Format(time.RFC3339), summary)
}

// exhaustedWorktreeWriters builds a message that stays identical between passes,
// so the recovery diagnostic is written and logged once.
func exhaustedWorktreeWriters(state *worktreeWaitState, journalPath string) error {
	return fmt.Errorf("%w: first seen %s, waited %s; automatic recovery stopped waiting and the task stays recovery_pending for the watchdog to report; stop the processes, then run `clawflow worker recover --journal %s`: %s",
		errWorktreeWritersExhausted, state.FirstSeen.Format(time.RFC3339), worktreeWriterWaitLimit, journalPath, state.Summary)
}

func readWorktreeWaitState(path string) *worktreeWaitState {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var state worktreeWaitState
	// A corrupt or incomplete file restarts the clock; the next write makes it valid.
	if json.Unmarshal(raw, &state) != nil || state.FirstSeen.IsZero() {
		return nil
	}
	return &state
}

func writeWorktreeWaitState(path string, state *worktreeWaitState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeDurableFile(path, raw, 0600)
}

func splitWorktreeProcesses(procs []worktreeProcess) (writers, idle []worktreeProcess) {
	for _, p := range procs {
		if p.Writes != "" {
			writers = append(writers, p)
		} else {
			idle = append(idle, p)
		}
	}
	return writers, idle
}

func describeWorktreeProcesses(procs []worktreeProcess) string {
	sorted := append([]worktreeProcess(nil), procs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].PID < sorted[j].PID })
	parts := make([]string, 0, maxReportedWorktreeProcs+1)
	for i, p := range sorted {
		if i == maxReportedWorktreeProcs {
			parts = append(parts, fmt.Sprintf("and %d more", len(sorted)-maxReportedWorktreeProcs))
			break
		}
		if p.Writes != "" {
			parts = append(parts, fmt.Sprintf("pid %d (%s) has %s open for writing", p.PID, p.Command, p.Writes))
		} else {
			parts = append(parts, fmt.Sprintf("pid %d (%s)", p.PID, p.Command))
		}
	}
	return strings.Join(parts, "; ")
}

// insideWorktree reports whether p is root or below it. The separator check
// keeps /work/repo-old from matching /work/repo.
func insideWorktree(root, p string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimRight(root, "/")+"/")
}

func relativeToWorktree(root, p string) string {
	rel := strings.TrimPrefix(strings.TrimPrefix(p, strings.TrimRight(root, "/")), "/")
	if rel == "" {
		return "."
	}
	return rel
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

func scanLsofWorktree(ctx context.Context, workspace string, self int) ([]worktreeProcess, error) {
	bin, err := findLsof()
	if err != nil {
		return nil, err
	}
	// -F pcftan: pid, command, descriptor, type, access mode, name. -d limits the
	// listing to cwd and numeric descriptors, which leaves out executables and
	// memory mappings. +c 0 keeps whole command names. -w hides warnings about
	// entries that cannot be read.
	cmd := exec.CommandContext(ctx, bin, "-nP", "-w", "+c", "0", "-F", "pcftan", "-d", "cwd,0-65535")
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
	return parseLsofWorktree(stdout.Bytes(), workspace, self), nil
}

// parseLsofWorktree reads lsof -F output. A process record starts with "p"; each
// file inside it starts with "f" and is complete at the next "f", "p" or the end.
func parseLsofWorktree(out []byte, workspace string, self int) []worktreeProcess {
	found := map[int]*worktreeProcess{}
	var (
		pid                    int
		command                string
		fd, typ, access, label string
	)
	flush := func() {
		defer func() { fd, typ, access, label = "", "", "", "" }()
		if pid == 0 || pid == self || label == "" || !insideWorktree(workspace, label) {
			return
		}
		writes := fd != "cwd" && typ == "REG" && (access == "w" || access == "u")
		if fd != "cwd" && !writes {
			return
		}
		p := found[pid]
		if p == nil {
			p = &worktreeProcess{PID: pid, Command: command}
			found[pid] = p
		}
		if writes && p.Writes == "" {
			p.Writes = relativeToWorktree(workspace, label)
		}
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
		case 't':
			typ = value
		case 'a':
			access = value
		case 'n':
			label = value
		}
	}
	flush()
	procs := make([]worktreeProcess, 0, len(found))
	for _, p := range found {
		procs = append(procs, *p)
	}
	return procs
}

// scanProcWorktree walks a Linux-style proc tree. Entries that disappear or
// cannot be read (other users' processes, exited processes) are skipped; only a
// failure to list the tree itself is an error.
func scanProcWorktree(procRoot, workspace string, self int) ([]worktreeProcess, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	var procs []worktreeProcess
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || pid == self {
			continue
		}
		dir := filepath.Join(procRoot, entry.Name())
		p := worktreeProcess{PID: pid}
		cwdInside := false
		if cwd, err := os.Readlink(filepath.Join(dir, "cwd")); err == nil && insideWorktree(workspace, cwd) {
			cwdInside = true
		}
		if fds, err := os.ReadDir(filepath.Join(dir, "fd")); err == nil {
			for _, fd := range fds {
				fdPath := filepath.Join(dir, "fd", fd.Name())
				target, err := os.Readlink(fdPath)
				if err != nil || !insideWorktree(workspace, target) || strings.HasSuffix(target, " (deleted)") {
					continue
				}
				if info, err := os.Stat(fdPath); err != nil || !info.Mode().IsRegular() {
					continue
				}
				if procFDWritable(filepath.Join(dir, "fdinfo", fd.Name())) {
					p.Writes = relativeToWorktree(workspace, target)
					break
				}
			}
		}
		if !cwdInside && p.Writes == "" {
			continue
		}
		p.Command = procCommand(dir)
		procs = append(procs, p)
	}
	return procs, nil
}

func procCommand(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "comm"))
	if name := strings.TrimSpace(string(raw)); err == nil && name != "" {
		return name
	}
	return "unknown"
}

// procFDWritable reads the open flags of a descriptor from fdinfo ("flags:" is
// octal; the low two bits are the access mode, O_WRONLY=1 and O_RDWR=2). A
// descriptor that vanished is not writable. An unreadable or unparsable entry
// counts as writable so an unknown mode fails closed.
func procFDWritable(fdinfo string) bool {
	raw, err := os.ReadFile(fdinfo)
	if os.IsNotExist(err) {
		return false
	}
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
		return flags&3 != 0
	}
	return true
}
