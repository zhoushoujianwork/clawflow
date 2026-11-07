//go:build unix

package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	clawexecution "github.com/zhoushoujianwork/clawflow/internal/execution"
	offload "github.com/zhoushoujianwork/offload/pkg/execution"
)

// timeoutBackend behaves as a native agent in three ways, chosen per call:
// resumed (--resume), a quick unrelated run (quick flag file), or the original
// run that keeps a background writer alive until it is stopped.
const timeoutBackend = `#!/bin/sh
d="$CLAWFLOW_FAKE_DIR"
case " $* " in
*" --resume "*)
  cat >/dev/null
  for pid in $(cat "$d/pids"); do
    if kill -0 "$pid" 2>/dev/null; then echo "$pid" >> "$d/alive-at-resume"; fi
  done
  printf '%s\n' "$*" > "$d/resume-args"
  printf 'new\n' >> "$d/writes"
  printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-timeout","model":"fixture"}'
  printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"native-timeout","result":"resumed"}'
  exit 0
  ;;
esac
if [ -e "$d/quick" ]; then
  cat >/dev/null
  printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-quick","model":"fixture"}'
  printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"native-quick","result":"quick"}'
  exit 0
fi
(
  while :; do
    printf 'old\n' >> "$d/writes"
    sleep 0.02
  done
) &
child=$!
printf '%s %s\n' "$$" "$child" > "$d/pids"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-timeout","model":"fixture"}'
wait
`

// A library task that ran into its timeout returns from Run with the native
// process group already stopped, so the executor reports an interruption and
// the journal is acknowledged. A later run reusing the worker slot makes
// ReclaimRun answer ErrRunNotFound for the finished run. Recovery must then
// proceed on the next worker pass and continue the original session with a
// single writer, while an executor that may have died mid-run stays postponed.
func TestLibraryTimeoutRecoveryResumesOriginalSessionWithSingleWriter(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("CLAWFLOW_STATE_DIR", filepath.Join(root, "clawflow-state"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("OFFLOAD_MAX_WORKERS", "1")
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "fixture")
	fakeDir := filepath.Join(root, "fake")
	if err := os.MkdirAll(fakeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAWFLOW_FAKE_DIR", fakeDir)

	configDir := filepath.Join(root, "config", "offload")
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
	if err := os.WriteFile(fake, []byte(timeoutBackend), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OFFLOAD_CLAUDE", fake)

	workspace := makeRecoveryRepo(t)
	tokenFile := filepath.Join(root, "worker.token")
	if err := os.WriteFile(tokenFile, []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var authorityCalls atomic.Int32
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/tasks/task/recover") {
			http.NotFound(w, r)
			return
		}
		authorityCalls.Add(1)
		future := time.Now().Add(time.Hour)
		_ = json.NewEncoder(w).Encode(map[string]any{"task": coordinator.Task{ID: "task", Repo: "o/r", Kind: "implement", Operator: "implement", FencingToken: 2, LeaseExpiresAt: &future}})
	}))
	defer server.Close()
	sum := sha256.Sum256(server.Certificate().Raw)
	options := workerOptions{StateDir: filepath.Join(root, "worker-state"), Coordinator: server.URL, TokenFile: tokenFile, Fingerprint: hex.EncodeToString(sum[:])}
	journalPath := filepath.Join(workerJournalRoot(options), "task", "journal.json")
	checkpointPath := filepath.Join(filepath.Dir(journalPath), "execution.json")
	if err := os.MkdirAll(filepath.Dir(journalPath), 0o700); err != nil {
		t.Fatal(err)
	}

	// 1. The original run reaches its timeout and Run returns.
	config := clawexecution.Config{Engine: clawexecution.EngineLibrary, Profile: "fixture"}
	request := func(timeoutMS int64) clawexecution.Request {
		return clawexecution.Request{Version: 1, RequestID: clawexecution.NewSessionKey(), Workspace: workspace, Prompt: "implement", PermissionMode: "bypassPermissions", TimeoutMS: timeoutMS}
	}
	result, err := clawexecution.Run(clawexecution.WithCheckpoint(context.Background(), checkpointPath, false), config, request(2000), nil, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) || result.ErrorKind != "timeout" {
		t.Fatalf("original run did not time out: err=%v result=%+v", err, result)
	}
	checkpoint, err := clawexecution.ReadCheckpoint(checkpointPath)
	if err != nil || checkpoint.RunID == "" || checkpoint.Session != "native-timeout" || checkpoint.Completed {
		t.Fatalf("checkpoint=%+v err=%v", checkpoint, err)
	}
	var nativePIDs []int
	raw, err := os.ReadFile(filepath.Join(fakeDir, "pids"))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range strings.Fields(string(raw)) {
		pid, convErr := strconv.Atoi(field)
		if convErr != nil {
			t.Fatal(convErr)
		}
		nativePIDs = append(nativePIDs, pid)
	}
	if len(nativePIDs) != 2 {
		t.Fatalf("native pids=%q", raw)
	}
	// Run returned, so the whole native process group is already gone.
	for _, pid := range nativePIDs {
		if processAlive(pid) {
			t.Fatalf("native process %d outlived the timed-out Run", pid)
		}
	}
	writesPath := filepath.Join(fakeDir, "writes")
	before, _ := os.ReadFile(writesPath)
	if !strings.Contains(string(before), "old") {
		t.Fatal("original writer never wrote")
	}

	// 2. An unrelated run reuses the only worker slot, so Offload no longer
	// records the finished run.
	if err := os.WriteFile(filepath.Join(fakeDir, "quick"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := clawexecution.Run(context.Background(), config, request(15000), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(fakeDir, "quick")); err != nil {
		t.Fatal(err)
	}
	state, err := abandonOrphanedRun(context.Background(), checkpoint.RunID)
	if state != AbandonUnknown || !errors.Is(err, offload.ErrRunNotFound) {
		t.Fatalf("precondition: reclaim state=%q err=%v", state, err)
	}

	past := time.Now().Add(-time.Minute)
	journal := func(state string) *workerJournal {
		j := &workerJournal{Version: 1, Engine: clawexecution.EngineLibrary, Executable: filepath.Join(root, "executor"), Options: options, State: state,
			Task: coordinator.Task{ID: "task", Repo: "o/r", Kind: "implement", Operator: "implement", FencingToken: 1, LeaseExpiresAt: &past}}
		if state == "acknowledged" {
			j.Action = "fail"
			j.Report = json.RawMessage(`{"fencing_token":1,"result":{"status":"failed","infra_kind":"execution-interrupted","provenance":{"session_id":"native-timeout"}}}`)
		}
		return j
	}

	// 3. An executor that may have died mid-run is still postponed.
	if err := writeWorkerJournal(journalPath, journal("needs_recovery")); err != nil {
		t.Fatal(err)
	}
	err = prepareWorkerRecovery(context.Background(), &workerAPI{base: server.URL, token: "fixture", client: server.Client()}, journalPath)
	if !errors.Is(err, offload.ErrRunNotFound) || !strings.Contains(err.Error(), "recovery is postponed") || authorityCalls.Load() != 0 {
		t.Fatalf("needs_recovery was not postponed: err=%v authority_calls=%d", err, authorityCalls.Load())
	}

	// 4. The acknowledged interruption is recovered by the next worker pass.
	executor := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$CLAWFLOW_FAKE_DIR/spawned\"\n"
	if err := os.WriteFile(filepath.Join(root, "executor"), []byte(executor), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkerJournal(journalPath, journal("acknowledged")); err != nil {
		t.Fatal(err)
	}
	if err := recoverWorkerExecutor(options, journalPath); err != nil {
		t.Fatalf("automatic recovery did not proceed: %v", err)
	}
	waitRecoveryCondition(t, "recovery executor launch", func() bool {
		_, statErr := os.Stat(filepath.Join(fakeDir, "spawned"))
		return statErr == nil
	})
	if spawned, _ := os.ReadFile(filepath.Join(fakeDir, "spawned")); !strings.Contains(string(spawned), "worker execute --journal "+journalPath) {
		t.Fatalf("executor args=%q", spawned)
	}
	recovered, err := readWorkerJournal(journalPath)
	if err != nil || recovered.State != "prepared" || !recovered.Resume || recovered.Task.FencingToken != 2 || authorityCalls.Load() != 1 {
		t.Fatalf("journal=%+v err=%v authority_calls=%d", recovered, err, authorityCalls.Load())
	}
	if _, err := os.Stat(filepath.Join(workerJournalRoot(options), "history", "task", "epoch-1", "journal.json")); err != nil {
		t.Fatalf("interruption receipt was not archived: %v", err)
	}

	// 5. The resumed run continues the original session as the only writer.
	resumed, err := clawexecution.Run(clawexecution.WithCheckpoint(context.Background(), checkpointPath, true), config, request(15000), nil, nil, nil)
	if err != nil || resumed.Text != "resumed" || resumed.NativeSessionID != "native-timeout" {
		t.Fatalf("resumed run: err=%v result=%+v", err, resumed)
	}
	if args, _ := os.ReadFile(filepath.Join(fakeDir, "resume-args")); !strings.Contains(string(args), "--resume native-timeout") {
		t.Fatalf("resume did not target the original session: %q", args)
	}
	if alive, _ := os.ReadFile(filepath.Join(fakeDir, "alive-at-resume")); len(alive) != 0 {
		t.Fatalf("old writer still alive when the resumed run started: %q", alive)
	}
	after, _ := os.ReadFile(writesPath)
	if !strings.HasPrefix(string(after), string(before)) || strings.Count(string(after), "old") != strings.Count(string(before), "old") || strings.Count(string(after), "new") != 1 {
		t.Fatalf("writers overlapped: before=%d bytes after=%d bytes new=%d", len(before), len(after), strings.Count(string(after), "new"))
	}
	time.Sleep(100 * time.Millisecond)
	if later, _ := os.ReadFile(writesPath); len(later) != len(after) {
		t.Fatal("a writer kept writing after the resumed run finished")
	}
}
