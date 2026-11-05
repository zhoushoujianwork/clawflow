//go:build unix

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	clawexecution "github.com/zhoushoujianwork/clawflow/internal/execution"
	offload "github.com/zhoushoujianwork/offload/pkg/execution"
)

const crashBackend = `#!/bin/sh
(
  while :; do
    printf 'old\n' >> "$CLAWFLOW_RECLAIM_WRITES"
    sleep 0.02
  done
) &
child=$!
printf '%s %s\n' "$$" "$child" > "$CLAWFLOW_RECLAIM_PIDS"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-crash","model":"fixture"}'
wait
`

func TestLibraryCrashRecoveryHelper(t *testing.T) {
	checkpoint := os.Getenv("CLAWFLOW_RECLAIM_CHECKPOINT")
	workspace := os.Getenv("CLAWFLOW_RECLAIM_WORKSPACE")
	if checkpoint == "" || workspace == "" {
		t.Skip("crash recovery helper only")
	}
	timeout := time.Hour
	result, err := clawexecution.Run(clawexecution.WithCheckpoint(context.Background(), checkpoint, false), clawexecution.Config{Engine: clawexecution.EngineLibrary, Profile: "fixture"}, clawexecution.Request{
		Version: 1, RequestID: clawexecution.NewSessionKey(), Workspace: workspace, Prompt: "wait for recovery", PermissionMode: "bypassPermissions", TimeoutMS: timeout.Milliseconds(),
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("library run returned before crash: result=%+v err=%v", result, err)
	}
}

func TestLibraryCrashRecoveryReclaimsTreeBeforeResume(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	configHome := filepath.Join(root, "config")
	workspace := makeRecoveryRepo(t)
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("OFFLOAD_MAX_WORKERS", "1")
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "fixture")

	configDir := filepath.Join(configHome, "offload")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer gateway.Close()
	profile := `{"default":"fixture","profiles":{"fixture":{"backend":"claude","base_url":"` + gateway.URL + `","model":"fixture","token":"fixture"}}}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(root, "fake-claude")
	if err := os.WriteFile(fake, []byte(crashBackend), 0o700); err != nil {
		t.Fatal(err)
	}
	pidsPath := filepath.Join(root, "pids")
	writesPath := filepath.Join(root, "writes")
	t.Setenv("OFFLOAD_CLAUDE", fake)
	t.Setenv("CLAWFLOW_RECLAIM_PIDS", pidsPath)
	t.Setenv("CLAWFLOW_RECLAIM_WRITES", writesPath)

	journalRoot := filepath.Join(root, "attempt")
	journalPath := filepath.Join(journalRoot, "journal.json")
	checkpointPath := filepath.Join(journalRoot, "execution.json")
	if err := os.MkdirAll(journalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	journal := &workerJournal{Version: 1, Engine: clawexecution.EngineLibrary, Executable: "/fixture/clawflow", Options: workerOptions{StateDir: filepath.Join(root, "worker-state")}, State: "needs_recovery",
		Task: coordinator.Task{ID: "task", Repo: "o/r", Kind: "implement", Operator: "implement", FencingToken: 1, LeaseExpiresAt: &past}}
	if err := writeWorkerJournal(journalPath, journal); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAWFLOW_RECLAIM_CHECKPOINT", checkpointPath)
	t.Setenv("CLAWFLOW_RECLAIM_WORKSPACE", workspace)
	helper := exec.Command(os.Args[0], "-test.run=^TestLibraryCrashRecoveryHelper$")
	helper.Env = os.Environ()
	var helperOutput bytes.Buffer
	helper.Stdout, helper.Stderr = &helperOutput, &helperOutput
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	helperDone := make(chan struct{})
	go func() { _ = helper.Wait(); close(helperDone) }()
	t.Cleanup(func() {
		_ = helper.Process.Kill()
		select {
		case <-helperDone:
		case <-time.After(5 * time.Second):
		}
	})

	var nativePIDs []int
	waitRecoveryCondition(t, "fake native process tree", func() bool {
		select {
		case <-helperDone:
			t.Fatalf("library helper exited before native start: %s", helperOutput.String())
		default:
		}
		raw, err := os.ReadFile(pidsPath)
		if err != nil {
			return false
		}
		fields := strings.Fields(string(raw))
		if len(fields) != 2 {
			return false
		}
		nativePIDs = nativePIDs[:0]
		for _, field := range fields {
			pid, err := strconv.Atoi(field)
			if err != nil || pid < 2 {
				return false
			}
			nativePIDs = append(nativePIDs, pid)
		}
		return true
	})
	var checkpoint clawexecution.Checkpoint
	waitRecoveryCondition(t, "library checkpoint identity", func() bool {
		var err error
		checkpoint, err = clawexecution.ReadCheckpoint(checkpointPath)
		return err == nil && checkpoint.RunID != "" && checkpoint.Session == "native-crash"
	})
	waitRecoveryCondition(t, "recorded Offload process group", func() bool {
		files, _ := filepath.Glob(filepath.Join(state, "offload", "slots", "slot-*.lock"))
		for _, path := range files {
			var slot struct {
				RunID    string          `json:"run_id"`
				Group    json.RawMessage `json:"group"`
				Starting bool            `json:"starting"`
			}
			raw, _ := os.ReadFile(path)
			if json.Unmarshal(raw, &slot) == nil && slot.RunID == checkpoint.RunID && !slot.Starting && len(slot.Group) > 0 && string(slot.Group) != "null" {
				return true
			}
		}
		return false
	})

	stateResult, err := abandonOrphanedRun(context.Background(), checkpoint.RunID)
	if stateResult != AbandonUnknown || !errors.Is(err, offload.ErrRunStillOwned) {
		t.Fatalf("live owner reclaim: state=%q err=%v", stateResult, err)
	}
	for _, pid := range nativePIDs {
		if !processAlive(pid) {
			t.Fatalf("live-owner refusal killed native process %d", pid)
		}
	}

	if err := helper.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case <-helperDone:
	case <-time.After(20 * time.Second):
		t.Fatal("crashed helper did not exit")
	}
	for _, pid := range nativePIDs {
		if !processAlive(pid) {
			t.Fatalf("native process %d did not outlive crashed executor", pid)
		}
	}

	authorityCalls := 0
	oldTreeAliveAtResume := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		authorityCalls++
		for _, pid := range nativePIDs {
			oldTreeAliveAtResume = oldTreeAliveAtResume || processAlive(pid)
		}
		updated := journal.Task
		updated.FencingToken = 2
		future := time.Now().Add(time.Hour)
		updated.LeaseExpiresAt = &future
		_ = json.NewEncoder(w).Encode(map[string]any{"task": updated})
	}))
	defer server.Close()
	api := &workerAPI{base: server.URL, token: "fixture", client: server.Client()}
	if err := prepareWorkerRecovery(context.Background(), api, journalPath); err != nil {
		t.Fatal(err)
	}
	if authorityCalls != 1 || oldTreeAliveAtResume {
		t.Fatalf("resume authority calls=%d old_tree_alive=%v", authorityCalls, oldTreeAliveAtResume)
	}
	waitRecoveryCondition(t, "old native process tree to stop", func() bool {
		for _, pid := range nativePIDs {
			if processAlive(pid) {
				return false
			}
		}
		return true
	})
	before, _ := os.ReadFile(writesPath)
	time.Sleep(100 * time.Millisecond)
	after, _ := os.ReadFile(writesPath)
	if len(after) != len(before) {
		t.Fatal("old writer continued after recovery authorized resume")
	}
	recovered, err := readWorkerJournal(journalPath)
	if err != nil || recovered.State != "prepared" || !recovered.Resume || recovered.Task.FencingToken != 2 {
		t.Fatalf("journal=%+v err=%v", recovered, err)
	}
}

func TestLibraryCrashBeforeGroupRecordedStaysUnknown(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	slots := filepath.Join(state, "offload", "slots")
	if err := os.MkdirAll(slots, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"owner_pid": 1 << 22, "run_id": "starting-run", "starting": true, "acquired_at": time.Now()})
	if err := os.WriteFile(filepath.Join(slots, "slot-0.lock"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	stateResult, err := abandonOrphanedRun(context.Background(), "starting-run")
	if stateResult != AbandonUnknown || !errors.Is(err, offload.ErrRunStartUnrecorded) {
		t.Fatalf("state=%q err=%v", stateResult, err)
	}
}

func makeRecoveryRepo(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	for _, args := range [][]string{{"init"}, {"config", "user.name", "Fixture"}, {"config", "user.email", "fixture@example.test"}, {"remote", "add", "origin", "https://github.com/o/r.git"}} {
		cmd := exec.Command("git", append([]string{"-C", workspace}, args...)...)
		if raw, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, raw)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "code.txt"), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "code.txt"}, {"commit", "-m", "fixture"}} {
		cmd := exec.Command("git", append([]string{"-C", workspace}, args...)...)
		if raw, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, raw)
		}
	}
	canonical, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func waitRecoveryCondition(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
