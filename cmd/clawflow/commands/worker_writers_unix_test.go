//go:build unix

package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	clawexecution "github.com/zhoushoujianwork/clawflow/internal/execution"
)

// startSetsidProcess runs script in its own session, the way a tool that called
// setsid(2) leaves the original native process group. The process is stopped
// on cleanup so a failing test cannot leak it.
func startSetsidProcess(t *testing.T, dir, script string, args ...string) (pid int, stop func()) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{"-c", script, "sh"}, args...)...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid = cmd.Process.Pid
	if pgid, err := syscall.Getpgid(pid); err != nil || pgid != pid {
		t.Fatalf("process did not leave the test's process group: pgid=%d err=%v", pgid, err)
	}
	var stopped atomic.Bool
	stop = func() {
		if stopped.Swap(true) {
			return
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Wait()
	}
	t.Cleanup(stop)
	return pid, stop
}

// recoveryRig prepares an acknowledged execution-interrupted library journal
// whose worktree is a real git repository, plus a controller that counts
// recovery authorizations and an executor stand-in that records its launch.
type recoveryRig struct {
	options   workerOptions
	journal   string
	workspace string
	authority *atomic.Int32
	spawned   string
}

func newRecoveryRig(t *testing.T) *recoveryRig {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("CLAWFLOW_STATE_DIR", filepath.Join(root, "clawflow-state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "fixture")
	workspace := makeRecoveryRepo(t)
	tokenFile := filepath.Join(root, "worker.token")
	if err := os.WriteFile(tokenFile, []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rig := &recoveryRig{workspace: workspace, authority: &atomic.Int32{}, spawned: filepath.Join(root, "spawned")}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/tasks/task/recover") {
			http.NotFound(w, r)
			return
		}
		rig.authority.Add(1)
		future := time.Now().Add(time.Hour)
		_ = json.NewEncoder(w).Encode(map[string]any{"task": coordinator.Task{ID: "task", Repo: "o/r", Kind: "implement", Operator: "implement", FencingToken: 2, LeaseExpiresAt: &future}})
	}))
	t.Cleanup(server.Close)
	sum := sha256.Sum256(server.Certificate().Raw)
	rig.options = workerOptions{StateDir: filepath.Join(root, "worker-state"), Coordinator: server.URL, TokenFile: tokenFile, Fingerprint: hex.EncodeToString(sum[:])}
	rig.journal = filepath.Join(workerJournalRoot(rig.options), "task", "journal.json")
	if err := os.MkdirAll(filepath.Dir(rig.journal), 0o700); err != nil {
		t.Fatal(err)
	}
	executor := filepath.Join(root, "executor")
	if err := os.WriteFile(executor, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" > '"+rig.spawned+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	j := &workerJournal{Version: 1, Engine: clawexecution.EngineLibrary, Executable: executor, Options: rig.options, State: "acknowledged", Action: "fail",
		Report: json.RawMessage(`{"fencing_token":1,"result":{"status":"failed","infra_kind":"execution-interrupted","provenance":{"session_id":"native-retained"}}}`),
		Task:   coordinator.Task{ID: "task", Repo: "o/r", Kind: "implement", Operator: "implement", FencingToken: 1, LeaseExpiresAt: &past}}
	if err := writeWorkerJournal(rig.journal, j); err != nil {
		t.Fatal(err)
	}
	cp, _ := json.Marshal(clawexecution.Checkpoint{Version: 1, Workspace: workspace, Engine: clawexecution.EngineLibrary, Profile: "fixture", Session: "native-retained", Backend: "claude", RunID: "run-retained"})
	if err := writeDurableFile(filepath.Join(filepath.Dir(rig.journal), "execution.json"), cp, 0o600); err != nil {
		t.Fatal(err)
	}
	// The detector is the subject; Offload already confirmed the group ended.
	oldAbandon := abandonOrphanedRun
	abandonOrphanedRun = func(context.Context, string) (AbandonState, error) { return AbandonAlreadyEnded, nil }
	t.Cleanup(func() { abandonOrphanedRun = oldAbandon })
	return rig
}

func (r *recoveryRig) diagnostics(t *testing.T) string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(workerJournalRoot(r.options), "recovery", "*.json"))
	var all strings.Builder
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(raw)
	}
	return all.String()
}

func (r *recoveryRig) launched() bool {
	_, err := os.Stat(r.spawned)
	return err == nil
}

// A background process that left the native process group with setsid is
// invisible to Offload. While it still has the worktree as its working
// directory, recovery must be postponed with the reason recorded; once it
// exits, the next pass resumes. The scan is real: no stubbed detector.
func TestRecoveryPostponedWhileDetachedProcessRunsInWorktree(t *testing.T) {
	rig := newRecoveryRig(t)
	marker := filepath.Join(rig.workspace, "detached.log")
	pid, stop := startSetsidProcess(t, rig.workspace, `while :; do echo tick >> "$1"; sleep 0.05; done`, marker)
	waitRecoveryCondition(t, "detached writer to start", func() bool {
		raw, _ := os.ReadFile(marker)
		return len(raw) > 0
	})

	if err := recoverWorkerExecutors(rig.options); err != nil {
		t.Fatal(err)
	}
	reason := rig.diagnostics(t)
	if !strings.Contains(reason, "retained worktree is still in use by running processes") || !strings.Contains(reason, "recovery is postponed") || !strings.Contains(reason, "pid "+strconv.Itoa(pid)) {
		t.Fatalf("postponement reason missing from recovery diagnostics: %q", reason)
	}
	if rig.authority.Load() != 0 || rig.launched() {
		t.Fatalf("recovery proceeded while the worktree was in use: authority=%d launched=%v", rig.authority.Load(), rig.launched())
	}
	// Nothing is killed, and the receipt is untouched for the next pass.
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("detached process was disturbed: %v", err)
	}
	j, err := readWorkerJournal(rig.journal)
	if err != nil || j.State != "acknowledged" || j.Resume || j.Task.FencingToken != 1 {
		t.Fatalf("journal changed during postponement: %+v err=%v", j, err)
	}
	if _, err := os.Stat(filepath.Join(workerJournalRoot(rig.options), "history")); !os.IsNotExist(err) {
		t.Fatalf("attempt was archived during postponement: %v", err)
	}

	// Still postponed on the next pass.
	if err := recoverWorkerExecutors(rig.options); err != nil || rig.authority.Load() != 0 || rig.launched() {
		t.Fatalf("second pass: err=%v authority=%d launched=%v", err, rig.authority.Load(), rig.launched())
	}

	stop()
	if err := recoverWorkerExecutors(rig.options); err != nil {
		t.Fatal(err)
	}
	waitRecoveryCondition(t, "recovered executor launch", rig.launched)
	if rig.authority.Load() != 1 {
		t.Fatalf("authority calls=%d", rig.authority.Load())
	}
	j, err = readWorkerJournal(rig.journal)
	if err != nil || j.State != "prepared" || !j.Resume || j.Task.FencingToken != 2 {
		t.Fatalf("journal after recovery: %+v err=%v", j, err)
	}
	if left := rig.diagnostics(t); left != "" {
		t.Fatalf("postponement diagnostic was not cleared: %q", left)
	}
}

// The writer need not have the worktree as its working directory: an open
// writable descriptor is enough. A process that only reads from the worktree
// does not postpone recovery.
func TestRecoveryPostponedByDetachedProcessHoldingWritableFile(t *testing.T) {
	rig := newRecoveryRig(t)
	target := filepath.Join(rig.workspace, "held.log")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	pid, stop := startSetsidProcess(t, "/", `exec 3>>"$1"; : > "$2"; exec sleep 600`, target, ready)
	waitRecoveryCondition(t, "writer to open its file", func() bool { _, err := os.Stat(ready); return err == nil })

	api, err := newWorkerAPI(rig.options)
	if err != nil {
		t.Fatal(err)
	}
	defer api.client.CloseIdleConnections()
	err = prepareWorkerRecovery(context.Background(), api, rig.journal)
	if err == nil || !strings.Contains(err.Error(), "pid "+strconv.Itoa(pid)) || !strings.Contains(err.Error(), "open for writing: held.log") || rig.authority.Load() != 0 {
		t.Fatalf("err=%v authority=%d", err, rig.authority.Load())
	}
	stop()

	readerReady := filepath.Join(t.TempDir(), "ready")
	_, stopReader := startSetsidProcess(t, "/", `exec 3<"$1"; : > "$2"; exec sleep 600`, target, readerReady)
	waitRecoveryCondition(t, "reader to open its file", func() bool { _, err := os.Stat(readerReady); return err == nil })
	defer stopReader()
	if err := recoverWorkerExecutors(rig.options); err != nil {
		t.Fatal(err)
	}
	waitRecoveryCondition(t, "recovered executor launch", rig.launched)
}

// Without leftover processes the extra check changes nothing.
func TestRecoveryUnaffectedWithoutLeftoverProcesses(t *testing.T) {
	rig := newRecoveryRig(t)
	if err := recoverWorkerExecutors(rig.options); err != nil {
		t.Fatal(err)
	}
	waitRecoveryCondition(t, "recovered executor launch", rig.launched)
	j, err := readWorkerJournal(rig.journal)
	if err != nil || j.State != "prepared" || !j.Resume || j.Task.FencingToken != 2 || rig.authority.Load() != 1 {
		t.Fatalf("journal=%+v err=%v authority=%d", j, err, rig.authority.Load())
	}
	if left := rig.diagnostics(t); left != "" {
		t.Fatalf("unexpected recovery diagnostic: %q", left)
	}
}

// Both platform scanners must see a real detached process. lsof is the macOS
// path; the /proc scanner is the Linux path and runs only where /proc exists.
func TestPlatformScannersSeeDetachedProcess(t *testing.T) {
	workspace := makeRecoveryRepo(t)
	pid, stop := startSetsidProcess(t, workspace, `exec sleep 600`)
	defer stop()
	contains := func(hs []worktreeHolder) bool {
		for _, h := range hs {
			if h.PID == pid && h.Reason == holderReasonCWD {
				return true
			}
		}
		return false
	}
	t.Run("lsof", func(t *testing.T) {
		if _, err := findLsof(); err != nil {
			t.Skip("lsof is not installed")
		}
		hs, err := scanLsofHolders(context.Background(), workspace, os.Getpid())
		if err != nil || !contains(hs) {
			t.Fatalf("holders=%+v err=%v", hs, err)
		}
	})
	t.Run("proc", func(t *testing.T) {
		if _, err := os.Stat("/proc/self/cwd"); err != nil {
			t.Skip("no Linux-style /proc on this system")
		}
		hs, err := scanProcHolders("/proc", workspace, os.Getpid())
		if err != nil || !contains(hs) {
			t.Fatalf("holders=%+v err=%v", hs, err)
		}
	})
	t.Run("default", func(t *testing.T) {
		hs, err := scanWorktreeHolders(context.Background(), workspace)
		if err != nil || !contains(hs) {
			t.Fatalf("holders=%+v err=%v", hs, err)
		}
	})
}
