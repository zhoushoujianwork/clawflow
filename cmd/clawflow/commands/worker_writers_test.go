package commands

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

func TestParseLsofWorktree(t *testing.T) {
	const root = "/work/repo"
	out := strings.Join([]string{
		// pid 10: only its working directory is in the worktree.
		"p10", "cidle-shell", "fcwd", "tDIR", "a ", "n/work/repo/sub",
		"f0", "tCHR", "au", "n/dev/ttys001",
		// pid 11: writable regular file in the worktree plus read-only ones.
		"p11", "cnode", "fcwd", "tDIR", "a ", "n/elsewhere",
		"f3", "tREG", "ar", "n/work/repo/src/read.go",
		"f4", "tREG", "aw", "n/work/repo/src/out.log",
		// pid 12: read-write descriptor.
		"p12", "cpython", "f5", "tREG", "au", "n/work/repo/data.db",
		// pid 13: sibling directory with the same prefix, and a writable directory descriptor.
		"p13", "csibling", "fcwd", "tDIR", "a ", "n/work/repo-old", "f6", "tREG", "aw", "n/work/repo-old/x",
		"p14", "cdirfd", "f7", "tDIR", "aw", "n/work/repo/dir",
		// pid 99 is the worker itself.
		"p99", "cclawflow", "f8", "tREG", "aw", "n/work/repo/self.log",
		"",
	}, "\n")
	got := map[int]worktreeProcess{}
	for _, p := range parseLsofWorktree([]byte(out), root, 99) {
		got[p.PID] = p
	}
	if len(got) != 3 {
		t.Fatalf("processes = %+v", got)
	}
	if p := got[10]; p.Writes != "" || p.Command != "idle-shell" {
		t.Fatalf("cwd-only process = %+v", p)
	}
	if p := got[11]; p.Writes != "src/out.log" || p.Command != "node" {
		t.Fatalf("writer = %+v", p)
	}
	if p := got[12]; p.Writes != "data.db" {
		t.Fatalf("read-write holder = %+v", p)
	}
}

func fakeProcEntry(t *testing.T, procRoot string, pid int, cwd string, fds map[string]struct {
	target string
	flags  string
}) {
	t.Helper()
	dir := filepath.Join(procRoot, fmt.Sprint(pid))
	for _, d := range []string{"fd", "fdinfo"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(fmt.Sprintf("proc%d\n", pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cwd, filepath.Join(dir, "cwd")); err != nil {
		t.Fatal(err)
	}
	for name, fd := range fds {
		if err := os.Symlink(fd.target, filepath.Join(dir, "fd", name)); err != nil {
			t.Fatal(err)
		}
		info := fmt.Sprintf("pos:\t0\nflags:\t%s\nmnt_id:\t1\n", fd.flags)
		if err := os.WriteFile(filepath.Join(dir, "fdinfo", name), []byte(info), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestScanProcWorktree(t *testing.T) {
	worktree := t.TempDir()
	other := t.TempDir()
	for _, name := range []string{"read.txt", "write.txt", "rw.txt"} {
		if err := os.WriteFile(filepath.Join(worktree, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(other, "out.txt")
	if err := os.WriteFile(outside, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	type fd = struct{ target, flags string }
	procRoot := t.TempDir()
	// 100: only cwd. 101: read-only fd. 102: write-only fd. 103: read-write fd.
	// 104: writes outside. 105: writes a file already deleted. 106: sibling
	// directory sharing the worktree prefix.
	fakeProcEntry(t, procRoot, 100, worktree, nil)
	fakeProcEntry(t, procRoot, 101, other, map[string]fd{"3": {filepath.Join(worktree, "read.txt"), "0100000"}})
	fakeProcEntry(t, procRoot, 102, other, map[string]fd{"3": {filepath.Join(worktree, "write.txt"), "0100001"}})
	fakeProcEntry(t, procRoot, 103, other, map[string]fd{"3": {filepath.Join(worktree, "rw.txt"), "02"}})
	fakeProcEntry(t, procRoot, 104, other, map[string]fd{"3": {outside, "01"}})
	fakeProcEntry(t, procRoot, 105, other, map[string]fd{"3": {filepath.Join(worktree, "gone.txt") + " (deleted)", "01"}})
	fakeProcEntry(t, procRoot, 106, worktree+"-old", nil)
	fakeProcEntry(t, procRoot, 107, other, map[string]fd{"3": {filepath.Join(worktree, "write.txt"), "01"}}) // self
	if err := os.MkdirAll(filepath.Join(procRoot, "self"), 0o700); err != nil {
		t.Fatal(err)
	}

	procs, err := scanProcWorktree(procRoot, worktree, 107)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int]worktreeProcess{}
	for _, p := range procs {
		got[p.PID] = p
	}
	if len(got) != 3 {
		t.Fatalf("processes = %+v", got)
	}
	if p := got[100]; p.Writes != "" || p.Command != "proc100" {
		t.Fatalf("cwd-only = %+v", p)
	}
	if p := got[102]; p.Writes != "write.txt" {
		t.Fatalf("write-only = %+v", p)
	}
	if p := got[103]; p.Writes != "rw.txt" {
		t.Fatalf("read-write = %+v", p)
	}
	if _, err := scanProcWorktree(filepath.Join(procRoot, "missing"), worktree, 1); err == nil {
		t.Fatal("unreadable proc tree accepted")
	}
}

func stubWorktreeScan(t *testing.T, scan func() ([]worktreeProcess, error)) *int {
	t.Helper()
	old := scanWorktreeProcesses
	calls := 0
	scanWorktreeProcesses = func(context.Context, string) ([]worktreeProcess, error) {
		calls++
		return scan()
	}
	t.Cleanup(func() { scanWorktreeProcesses = old })
	return &calls
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func TestCWDOnlyProcessWarnsWithoutBlockingRecovery(t *testing.T) {
	logs := captureLogs(t)
	journal := filepath.Join(t.TempDir(), "journal.json")
	calls := stubWorktreeScan(t, func() ([]worktreeProcess, error) {
		return []worktreeProcess{{PID: 41, Command: "zsh"}}, nil
	})
	if err := ensureNoWorktreeWriters(context.Background(), "/work/repo", journal); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("scans = %d", *calls)
	}
	text := logs.String()
	if !strings.Contains(text, "worker/recovery-worktree-cwd") || !strings.Contains(text, "pid 41 (zsh)") || !strings.Contains(text, "level=WARN") {
		t.Fatalf("missing warning: %s", text)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(journal), worktreeWriterWaitFile)); !os.IsNotExist(err) {
		t.Fatalf("wait state written for cwd-only process: %v", err)
	}
}

func TestWritableDescriptorPostponesRecoveryWithReason(t *testing.T) {
	journal := filepath.Join(t.TempDir(), "journal.json")
	holders := []worktreeProcess{
		{PID: 52, Command: "sleep", Writes: "build/out.log"},
		{PID: 41, Command: "zsh"},
	}
	calls := stubWorktreeScan(t, func() ([]worktreeProcess, error) { return holders, nil })
	err := ensureNoWorktreeWriters(context.Background(), "/work/repo", journal)
	if !errors.Is(err, errWorktreeWriters) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"pid 52 (sleep) has build/out.log open for writing", "postponed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "zsh") {
		t.Fatalf("cwd-only process reported as writer: %v", err)
	}
	// The same writer on the next pass repeats the same message, so the recovery
	// diagnostic is not rewritten on every poll.
	again := ensureNoWorktreeWriters(context.Background(), "/work/repo", journal)
	if again == nil || again.Error() != err.Error() || *calls != 2 {
		t.Fatalf("second pass = %v calls=%d", again, *calls)
	}
	// The writer exits: recovery continues and the wait clock is cleared.
	holders = nil
	if err := ensureNoWorktreeWriters(context.Background(), "/work/repo", journal); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(journal), worktreeWriterWaitFile)); !os.IsNotExist(err) {
		t.Fatalf("wait state kept after writers exited: %v", err)
	}
}

func TestWorktreeWriterWaitIsBounded(t *testing.T) {
	oldLimit := worktreeWriterWaitLimit
	worktreeWriterWaitLimit = 80 * time.Millisecond
	t.Cleanup(func() { worktreeWriterWaitLimit = oldLimit })
	journal := filepath.Join(t.TempDir(), "journal.json")
	holders := []worktreeProcess{{PID: 52, Command: "sleep", Writes: "out.log"}}
	calls := stubWorktreeScan(t, func() ([]worktreeProcess, error) { return holders, nil })

	if err := ensureNoWorktreeWriters(context.Background(), "/work/repo", journal); !errors.Is(err, errWorktreeWriters) {
		t.Fatalf("first pass = %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	exhausted := ensureNoWorktreeWriters(context.Background(), "/work/repo", journal)
	if !errors.Is(exhausted, errWorktreeWritersExhausted) || errors.Is(exhausted, errWorktreeWriters) {
		t.Fatalf("after limit = %v", exhausted)
	}
	for _, want := range []string{"recovery_pending", "watchdog", "worker recover --journal " + journal, "pid 52 (sleep)"} {
		if !strings.Contains(exhausted.Error(), want) {
			t.Fatalf("missing %q in %v", want, exhausted)
		}
	}
	// Automatic passes stop scanning and keep reporting the same error.
	scans := *calls
	for range 3 {
		again := ensureNoWorktreeWriters(context.Background(), "/work/repo", journal)
		if again == nil || again.Error() != exhausted.Error() {
			t.Fatalf("exhausted error changed: %v", again)
		}
	}
	if *calls != scans {
		t.Fatalf("scanned %d more times after the wait limit", *calls-scans)
	}
	// An operator-requested recovery looks again; with the writer still running it stays blocked.
	explicit := withExplicitRecovery(context.Background())
	if err := ensureNoWorktreeWriters(explicit, "/work/repo", journal); !errors.Is(err, errWorktreeWritersExhausted) || *calls != scans+1 {
		t.Fatalf("explicit with writer = %v calls=%d", err, *calls)
	}
	// Once the writer is gone the explicit request proceeds and the wait is cleared.
	holders = nil
	if err := ensureNoWorktreeWriters(explicit, "/work/repo", journal); err != nil {
		t.Fatal(err)
	}
	holders = []worktreeProcess{{PID: 60, Command: "sleep", Writes: "out.log"}}
	if err := ensureNoWorktreeWriters(context.Background(), "/work/repo", journal); !errors.Is(err, errWorktreeWriters) {
		t.Fatalf("new wait did not restart: %v", err)
	}
}

func TestWorktreeScanFailurePostponesWithinLimit(t *testing.T) {
	oldLimit := worktreeWriterWaitLimit
	worktreeWriterWaitLimit = 80 * time.Millisecond
	t.Cleanup(func() { worktreeWriterWaitLimit = oldLimit })
	journal := filepath.Join(t.TempDir(), "journal.json")
	stubWorktreeScan(t, func() ([]worktreeProcess, error) { return nil, errors.New("lsof not found") })
	err := ensureNoWorktreeWriters(context.Background(), "/work/repo", journal)
	if !errors.Is(err, errWorktreeWriters) || !strings.Contains(err.Error(), "cannot inspect running processes (lsof not found)") {
		t.Fatalf("err = %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if err := ensureNoWorktreeWriters(context.Background(), "/work/repo", journal); !errors.Is(err, errWorktreeWritersExhausted) {
		t.Fatalf("scan failure not bounded: %v", err)
	}
}

func TestPrepareWorkerRecoveryPostponesForWritersThenResumes(t *testing.T) {
	path, _, api, calls := libraryRecoveryFixture(t, false)
	old := abandonOrphanedRun
	abandonOrphanedRun = func(context.Context, string) (AbandonState, error) { return AbandonEnded, nil }
	t.Cleanup(func() { abandonOrphanedRun = old })
	holders := []worktreeProcess{{PID: 52, Command: "sleep", Writes: "out.log"}}
	stubWorktreeScan(t, func() ([]worktreeProcess, error) { return holders, nil })

	err := prepareWorkerRecovery(context.Background(), api, path)
	if !errors.Is(err, errWorktreeWriters) || *calls != 0 {
		t.Fatalf("err=%v controller calls=%d", err, *calls)
	}
	j, readErr := readWorkerJournal(path)
	if readErr != nil || j.State != "needs_recovery" || j.Resume || j.Task.FencingToken != 1 {
		t.Fatalf("journal changed while postponed: %+v err=%v", j, readErr)
	}
	holders = nil
	if err := prepareWorkerRecovery(context.Background(), api, path); err != nil {
		t.Fatal(err)
	}
	j, readErr = readWorkerJournal(path)
	if readErr != nil || *calls != 1 || !j.Resume || j.State != "prepared" || j.Task.FencingToken != 2 {
		t.Fatalf("journal=%+v calls=%d err=%v", j, *calls, readErr)
	}
}

func TestPrepareWorkerRecoveryStopsWaitingAfterLimit(t *testing.T) {
	oldLimit := worktreeWriterWaitLimit
	worktreeWriterWaitLimit = 80 * time.Millisecond
	t.Cleanup(func() { worktreeWriterWaitLimit = oldLimit })
	path, _, api, calls := libraryRecoveryFixture(t, false)
	old := abandonOrphanedRun
	abandonOrphanedRun = func(context.Context, string) (AbandonState, error) { return AbandonEnded, nil }
	t.Cleanup(func() { abandonOrphanedRun = old })
	scans := stubWorktreeScan(t, func() ([]worktreeProcess, error) {
		return []worktreeProcess{{PID: 52, Command: "sleep", Writes: "out.log"}}, nil
	})

	if err := prepareWorkerRecovery(context.Background(), api, path); !errors.Is(err, errWorktreeWriters) {
		t.Fatalf("first pass = %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	for range 2 {
		if err := prepareWorkerRecovery(context.Background(), api, path); !errors.Is(err, errWorktreeWritersExhausted) {
			t.Fatalf("after limit = %v", err)
		}
	}
	if *scans != 1 || *calls != 0 {
		t.Fatalf("scans=%d controller calls=%d", *scans, *calls)
	}
	j, err := readWorkerJournal(path)
	if err != nil || j.State != "needs_recovery" || j.Resume {
		t.Fatalf("journal must stay retained for the watchdog: %+v err=%v", j, err)
	}
}

// startDetachedProcess runs script in its own session, so it is outside the
// process group of the test just like a tool that called setsid(2). The script
// prints "ready" once it is set up.
func startDetachedProcess(t *testing.T, dir, script string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{"-c", script, "detached"}, args...)...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopDetachedProcess(cmd) })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("detached process not ready: %q %v", line, err)
	}
	// setsid makes the process a session and group leader, so Offload's process
	// group tracking cannot reach it.
	group, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil || group != cmd.Process.Pid || group == syscall.Getpgrp() {
		t.Fatalf("process is not outside the test's process group: pgid=%d err=%v", group, err)
	}
	return cmd
}

// stopDetachedProcess is safe to call more than once.
func stopDetachedProcess(cmd *exec.Cmd) {
	if cmd.ProcessState != nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
}

func useHostWorktreeScan(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		if _, err := findLsof(); err != nil {
			t.Skip("lsof is not available on this host; exact-head CI runs the Linux /proc scanner")
		}
	}
	old := scanWorktreeProcesses
	scanWorktreeProcesses = hostWorktreeProcesses
	t.Cleanup(func() { scanWorktreeProcesses = old })
}

func worktreeOfFixture(t *testing.T, path string) string {
	t.Helper()
	cp, err := execution.ReadCheckpoint(filepath.Join(filepath.Dir(path), "execution.json"))
	if err != nil {
		t.Fatal(err)
	}
	return cp.Workspace
}

// TestDetachedBackgroundProcessPostponesRecovery runs real processes started
// with setsid semantics against the host's own scanner (/proc on Linux, lsof on
// macOS).
func TestDetachedBackgroundProcessPostponesRecovery(t *testing.T) {
	useHostWorktreeScan(t)
	path, _, api, calls := libraryRecoveryFixture(t, false)
	old := abandonOrphanedRun
	abandonOrphanedRun = func(context.Context, string) (AbandonState, error) { return AbandonEnded, nil }
	t.Cleanup(func() { abandonOrphanedRun = old })
	worktree := worktreeOfFixture(t, path)
	logs := captureLogs(t)

	// No leftover process: recovery is not affected by the scan.
	if err := ensureNoWorktreeWriters(context.Background(), worktree, path); err != nil {
		t.Fatalf("clean worktree rejected: %v", err)
	}

	// A detached process whose only link is its working directory: warning, no postponement.
	idle := startDetachedProcess(t, worktree, `echo ready; exec sleep 300`)
	if err := ensureNoWorktreeWriters(context.Background(), worktree, path); err != nil {
		t.Fatalf("cwd-only process blocked recovery: %v", err)
	}
	if !strings.Contains(logs.String(), fmt.Sprintf("pid %d", idle.Process.Pid)) {
		t.Fatalf("cwd-only process not logged: %s", logs.String())
	}
	stopDetachedProcess(idle)

	// A detached process holding a file in the worktree open for writing.
	writer := startDetachedProcess(t, worktree, `exec 3>>"$1"; echo ready; exec sleep 300`, filepath.Join(worktree, "background.log"))
	err := prepareWorkerRecovery(context.Background(), api, path)
	if !errors.Is(err, errWorktreeWriters) || *calls != 0 {
		t.Fatalf("recovery was not postponed: err=%v controller calls=%d", err, *calls)
	}
	for _, want := range []string{fmt.Sprintf("pid %d", writer.Process.Pid), "background.log open for writing", "postponed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("reason lacks %q: %v", want, err)
		}
	}
	if j, readErr := readWorkerJournal(path); readErr != nil || j.Resume || j.State != "needs_recovery" {
		t.Fatalf("journal changed while postponed: %+v %v", j, readErr)
	}
	if err := writer.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("recovery interfered with the background process: %v", err)
	}

	// The process ends: recovery continues.
	stopDetachedProcess(writer)
	if err := prepareWorkerRecovery(context.Background(), api, path); err != nil {
		t.Fatalf("recovery did not continue after the process ended: %v", err)
	}
	j, readErr := readWorkerJournal(path)
	if readErr != nil || *calls != 1 || !j.Resume || j.State != "prepared" || j.Task.FencingToken != 2 {
		t.Fatalf("journal=%+v calls=%d err=%v", j, *calls, readErr)
	}
}

// TestHostScanFindsWriterThroughSymlinkedWorktree passes the worktree through a
// symlink; the host scanners report resolved paths and must still match.
func TestHostScanFindsWriterThroughSymlinkedWorktree(t *testing.T) {
	useHostWorktreeScan(t)
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "worktree-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	writer := startDetachedProcess(t, link, `exec 3>>"$1"; echo ready; exec sleep 300`, filepath.Join(link, "background.log"))
	procs, err := hostWorktreeProcesses(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	writers, _ := splitWorktreeProcesses(procs)
	for _, p := range writers {
		if p.PID == writer.Process.Pid && p.Writes == "background.log" {
			return
		}
	}
	t.Fatalf("writer %d behind symlinked worktree not found: %+v", writer.Process.Pid, procs)
}
