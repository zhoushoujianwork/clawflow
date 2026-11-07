package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
	offload "github.com/zhoushoujianwork/offload/pkg/execution"
)

func TestWorkerRecoveryPreservesDirtyWorktree(t *testing.T) {
	for _, state := range []string{"needs_recovery", "acknowledged"} {
		t.Run(state, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "fixture")
			work := t.TempDir()
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", work}, args...)...)
				if raw, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v %s", args, err, raw)
				}
			}
			git("init")
			git("config", "user.name", "Fixture")
			git("config", "user.email", "fixture@example.test")
			git("remote", "add", "origin", "https://github.com/o/r.git")
			if err := os.WriteFile(filepath.Join(work, "code.txt"), []byte("base"), 0600); err != nil {
				t.Fatal(err)
			}
			git("add", "code.txt")
			git("commit", "-m", "fixture")
			if err := os.WriteFile(filepath.Join(work, "code.txt"), []byte("retained edits"), 0600); err != nil {
				t.Fatal(err)
			}
			work, err := filepath.EvalSymlinks(work)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			path := filepath.Join(root, "journal.json")
			past := time.Now().Add(-time.Minute)
			journal := &workerJournal{Version: 1, Executable: "/fixture/cli", OffloadCommand: "/usr/bin/true", Options: workerOptions{StateDir: t.TempDir()}, State: state, Task: coordinator.Task{ID: "task", Repo: "o/r", Kind: "implement", Operator: "implement", FencingToken: 1, LeaseExpiresAt: &past}}
			if state == "acknowledged" {
				journal.Action = "fail"
				journal.Report = json.RawMessage(`{"fencing_token":1,"result":{"status":"failed","infra_kind":"execution-interrupted","provenance":{"session_id":"native-retained"}}}`)
			}
			if err := writeWorkerJournal(path, journal); err != nil {
				t.Fatal(err)
			}
			checkpoint := filepath.Join(root, "execution.json")
			raw, _ := json.Marshal(execution.Checkpoint{Version: 1, Workspace: work, Command: "/usr/bin/true", Profile: "fixture", Session: "native-retained"})
			if err := writeDurableFile(checkpoint, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if state == "acknowledged" {
				for _, mode := range []string{"completed", "session-mismatch", "profile-mismatch", "live-authority"} {
					t.Run(mode, func(t *testing.T) {
						var cp execution.Checkpoint
						if err := json.Unmarshal(raw, &cp); err != nil {
							t.Fatal(err)
						}
						copy := *journal
						switch mode {
						case "completed":
							cp.Completed = true
						case "session-mismatch":
							cp.Session = "different-native"
						case "profile-mismatch":
							cp.Profile = "different-profile"
						case "live-authority":
							future := time.Now().Add(time.Hour)
							copy.Task.LeaseExpiresAt = &future
						}
						modified, _ := json.Marshal(cp)
						if err := writeDurableFile(checkpoint, modified, 0600); err != nil {
							t.Fatal(err)
						}
						if err := writeWorkerJournal(path, &copy); err != nil {
							t.Fatal(err)
						}
						// A nil API ensures refusal happens before any authority request.
						if err := prepareWorkerRecovery(context.Background(), nil, path); err == nil {
							t.Fatal("unsafe recovery accepted")
						}
					})
				}
				if err := writeDurableFile(checkpoint, raw, 0600); err != nil {
					t.Fatal(err)
				}
				if err := writeWorkerJournal(path, journal); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				updated := journal.Task
				updated.FencingToken = 2
				future := time.Now().Add(time.Hour)
				updated.LeaseExpiresAt = &future
				_ = json.NewEncoder(w).Encode(map[string]any{"task": updated})
			}))
			defer server.Close()
			api := &workerAPI{base: server.URL, token: "fixture", client: server.Client()}
			lock, err := filelock.Try(checkpoint+".lock", map[string]any{"PID": os.Getpid()})
			if err != nil {
				t.Fatal(err)
			}
			if err := prepareWorkerRecovery(context.Background(), api, path); err == nil || calls != 0 {
				t.Fatal("live native execution was resumed", err, calls)
			}
			filelock.Release(lock)
			if err := prepareWorkerRecovery(context.Background(), api, path); err != nil {
				t.Fatal(err)
			}
			restored, err := readWorkerJournal(path)
			if err != nil || !restored.Resume || restored.Task.FencingToken != 2 || restored.State != "prepared" {
				t.Fatal(restored, err)
			}
			if state == "acknowledged" {
				archived, err := readWorkerJournal(filepath.Join(workerJournalRoot(journal.Options), "history", filepath.Base(root), "epoch-1", "journal.json"))
				if err != nil {
					t.Fatal(err)
				}
				var before, after bytes.Buffer
				if err := json.Compact(&before, journal.Report); err != nil {
					t.Fatal(err)
				}
				if err := json.Compact(&after, archived.Report); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(before.Bytes(), after.Bytes()) || (len(restored.Report) != 0 && string(restored.Report) != "null") || restored.Action != "" {
					t.Fatal("interruption receipt not archived/cleared", archived, restored, err)
				}
			}
			data, err := os.ReadFile(filepath.Join(work, "code.txt"))
			if err != nil || string(data) != "retained edits" {
				t.Fatal("recovery reset worktree", string(data), err)
			}

		})
	}
}

func libraryRecoveryFixture(t *testing.T, withResult bool) (string, *workerJournal, *workerAPI, *int) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "fixture")
	work := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", work}, args...)...)
		if raw, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, raw)
		}
	}
	git("init")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.test")
	git("remote", "add", "origin", "https://github.com/o/r.git")
	if err := os.WriteFile(filepath.Join(work, "code.txt"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "code.txt")
	git("commit", "-m", "fixture")
	work, _ = filepath.EvalSymlinks(work)
	root := t.TempDir()
	path := filepath.Join(root, "journal.json")
	past := time.Now().Add(-time.Minute)
	j := &workerJournal{Version: 1, Engine: execution.EngineLibrary, Executable: "/fixture/clawflow", Options: workerOptions{StateDir: t.TempDir()}, State: "needs_recovery",
		Task: coordinator.Task{ID: "task", Repo: "o/r", Kind: "implement", Operator: "implement", FencingToken: 1, LeaseExpiresAt: &past}}
	if err := writeWorkerJournal(path, j); err != nil {
		t.Fatal(err)
	}
	cp := execution.Checkpoint{Version: 1, Workspace: work, Engine: execution.EngineLibrary, Profile: "fixture", Session: "native-retained", Backend: "claude", RunID: "run-retained"}
	if withResult {
		cp.ArtifactDir = filepath.Join(root, "execution-artifacts", "clawflow-offload-fixture")
		if err := os.MkdirAll(cp.ArtifactDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cp.ArtifactDir, "result.json"), []byte(`{"status":"ok","exit_code":0}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(cp)
	if err := writeDurableFile(filepath.Join(root, "execution.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		updated := j.Task
		updated.FencingToken = 2
		future := time.Now().Add(time.Hour)
		updated.LeaseExpiresAt = &future
		_ = json.NewEncoder(w).Encode(map[string]any{"task": updated})
	}))
	t.Cleanup(server.Close)
	return path, j, &workerAPI{base: server.URL, token: "fixture", client: server.Client()}, &calls
}

func TestLibraryRecoveryRequiresKnownEndedOrphan(t *testing.T) {
	path, _, api, calls := libraryRecoveryFixture(t, false)
	err := prepareWorkerRecovery(context.Background(), api, path)
	if err == nil || !strings.Contains(err.Error(), "no worker slot records this run") || *calls != 0 {
		t.Fatalf("err=%v calls=%d", err, *calls)
	}
	j, readErr := readWorkerJournal(path)
	if readErr != nil || j.State != "needs_recovery" || j.Resume {
		t.Fatalf("journal=%+v err=%v", j, readErr)
	}
}

func TestLibraryRecoveryProceedsAfterOrphanEnded(t *testing.T) {
	for _, state := range []AbandonState{AbandonEnded, AbandonAlreadyEnded} {
		t.Run(string(state), func(t *testing.T) {
			path, _, api, calls := libraryRecoveryFixture(t, false)
			old := abandonOrphanedRun
			abandonOrphanedRun = func(context.Context, string) (AbandonState, error) { return state, nil }
			defer func() { abandonOrphanedRun = old }()
			if err := prepareWorkerRecovery(context.Background(), api, path); err != nil {
				t.Fatal(err)
			}
			j, err := readWorkerJournal(path)
			if err != nil || *calls != 1 || !j.Resume || j.State != "prepared" || j.Task.FencingToken != 2 {
				t.Fatalf("journal=%+v calls=%d err=%v", j, *calls, err)
			}
		})
	}
}

func TestLibraryRecoveryTreatsResultJSONAsAwaitingReconciliation(t *testing.T) {
	path, _, api, calls := libraryRecoveryFixture(t, true)
	old := abandonOrphanedRun
	called := false
	abandonOrphanedRun = func(context.Context, string) (AbandonState, error) {
		called = true
		return AbandonEnded, nil
	}
	defer func() { abandonOrphanedRun = old }()
	err := prepareWorkerRecovery(context.Background(), api, path)
	if err == nil || !strings.Contains(err.Error(), "completed native result retained") || *calls != 0 || called {
		t.Fatalf("err=%v calls=%d abandon_called=%v", err, *calls, called)
	}
}

func TestLibraryRecoveryResumesAfterIncompleteResult(t *testing.T) {
	for _, result := range []string{
		`{"status":"error","exit_code":1,"error_kind":"execution"}`,
		`{"status":"timeout","exit_code":124,"error_kind":"timeout"}`,
		`{"status":"cancelled","exit_code":130,"error_kind":"canceled"}`,
	} {
		t.Run(result, func(t *testing.T) {
			path, _, api, calls := libraryRecoveryFixture(t, true)
			cp, err := execution.ReadCheckpoint(filepath.Join(filepath.Dir(path), "execution.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cp.ArtifactDir, "result.json"), []byte(result), 0600); err != nil {
				t.Fatal(err)
			}
			old := abandonOrphanedRun
			called := false
			abandonOrphanedRun = func(context.Context, string) (AbandonState, error) {
				called = true
				return AbandonEnded, nil
			}
			defer func() { abandonOrphanedRun = old }()
			if err := prepareWorkerRecovery(context.Background(), api, path); err != nil {
				t.Fatal(err)
			}
			j, err := readWorkerJournal(path)
			if err != nil || !called || *calls != 1 || !j.Resume || j.State != "prepared" {
				t.Fatalf("journal=%+v calls=%d abandon_called=%v err=%v", j, *calls, called, err)
			}
		})
	}
}

const libraryTimeoutRecord = `{"status":"timeout","exit_code":124,"error_kind":"timeout"}`

// libraryJournalFixture returns a library recovery fixture whose journal is in
// the given state. An acknowledged journal carries the interruption receipt the
// executor reports after Run returns, and the retained Offload run record holds
// the given result.json (empty means no record).
func libraryJournalFixture(t *testing.T, state, record string) (string, *workerAPI, *int) {
	t.Helper()
	path, j, api, calls := libraryRecoveryFixture(t, record != "")
	if record != "" {
		cp, err := execution.ReadCheckpoint(filepath.Join(filepath.Dir(path), "execution.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cp.ArtifactDir, "result.json"), []byte(record), 0600); err != nil {
			t.Fatal(err)
		}
	}
	j.State = state
	if state == "acknowledged" {
		j.Action = "fail"
		j.Report = json.RawMessage(`{"fencing_token":1,"result":{"status":"failed","infra_kind":"execution-interrupted","provenance":{"session_id":"native-retained"}}}`)
	}
	if err := writeWorkerJournal(path, j); err != nil {
		t.Fatal(err)
	}
	return path, api, calls
}

func TestLibraryRecoveryOrphanErrorDependsOnJournalState(t *testing.T) {
	notFound := offload.ErrRunNotFound
	tests := []struct {
		name    string
		state   string
		record  string
		abandon func() (AbandonState, error)
		resume  bool
		wantErr error
	}{
		{name: "acknowledged run not found", state: "acknowledged", record: libraryTimeoutRecord, abandon: func() (AbandonState, error) { return AbandonUnknown, notFound }, resume: true},
		{name: "acknowledged wrapped run not found", state: "acknowledged", record: libraryTimeoutRecord, abandon: func() (AbandonState, error) {
			return AbandonUnknown, fmt.Errorf("reclaim: %w", notFound)
		}, resume: true},
		{name: "acknowledged run not found without run record", state: "acknowledged", abandon: func() (AbandonState, error) { return AbandonUnknown, notFound }, resume: true},
		{name: "needs_recovery run not found", state: "needs_recovery", record: libraryTimeoutRecord, abandon: func() (AbandonState, error) { return AbandonUnknown, notFound }, wantErr: offload.ErrRunNotFound},
		{name: "running run not found", state: "running", record: libraryTimeoutRecord, abandon: func() (AbandonState, error) { return AbandonUnknown, notFound }, wantErr: offload.ErrRunNotFound},
		{name: "acknowledged still owned", state: "acknowledged", record: libraryTimeoutRecord, abandon: func() (AbandonState, error) { return AbandonUnknown, offload.ErrRunStillOwned }, wantErr: offload.ErrRunStillOwned},
		{name: "acknowledged start unrecorded", state: "acknowledged", record: libraryTimeoutRecord, abandon: func() (AbandonState, error) { return AbandonUnknown, offload.ErrRunStartUnrecorded }, wantErr: offload.ErrRunStartUnrecorded},
		{name: "acknowledged other error", state: "acknowledged", record: libraryTimeoutRecord, abandon: func() (AbandonState, error) { return AbandonUnknown, errors.New("reclaim failed") }},
		{name: "acknowledged unconfirmed without error", state: "acknowledged", record: libraryTimeoutRecord, abandon: func() (AbandonState, error) { return AbandonUnknown, nil }},
		{name: "needs_recovery still owned", state: "needs_recovery", record: libraryTimeoutRecord, abandon: func() (AbandonState, error) { return AbandonUnknown, offload.ErrRunStillOwned }, wantErr: offload.ErrRunStillOwned},
		{name: "needs_recovery start unrecorded", state: "needs_recovery", record: libraryTimeoutRecord, abandon: func() (AbandonState, error) { return AbandonUnknown, offload.ErrRunStartUnrecorded }, wantErr: offload.ErrRunStartUnrecorded},
		{name: "running start unrecorded", state: "running", record: libraryTimeoutRecord, abandon: func() (AbandonState, error) { return AbandonUnknown, offload.ErrRunStartUnrecorded }, wantErr: offload.ErrRunStartUnrecorded},
		// Run left a cleanup record: its native processes were not confirmed gone.
		{name: "acknowledged run not found with unresolved cleanup", state: "acknowledged",
			record:  `{"status":"timeout","exit_code":124,"error_kind":"timeout","cleanup":"unresolved: worker processes are still running in process group 4242"}`,
			abandon: func() (AbandonState, error) { return AbandonUnknown, notFound }, wantErr: offload.ErrRunNotFound},
		{name: "acknowledged run not found with unreadable run record", state: "acknowledged", record: `{not json`, abandon: func() (AbandonState, error) { return AbandonUnknown, notFound }, wantErr: offload.ErrRunNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, api, calls := libraryJournalFixture(t, tt.state, tt.record)
			old := abandonOrphanedRun
			abandonOrphanedRun = func(context.Context, string) (AbandonState, error) { return tt.abandon() }
			defer func() { abandonOrphanedRun = old }()
			err := prepareWorkerRecovery(context.Background(), api, path)
			j, readErr := readWorkerJournal(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tt.resume {
				if err != nil || *calls != 1 || !j.Resume || j.State != "prepared" || j.Task.FencingToken != 2 {
					t.Fatalf("err=%v calls=%d journal=%+v", err, *calls, j)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "recovery is postponed") || *calls != 0 {
				t.Fatalf("err=%v calls=%d", err, *calls)
			}
			if j.State != tt.state || j.Resume || j.Task.FencingToken != 1 {
				t.Fatalf("postponed recovery changed the journal: %+v", j)
			}
			// The visible reason keeps the Offload error for the operator.
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("postponed error %v does not preserve %v", err, tt.wantErr)
			}
		})
	}
}

func TestInterruptionSessionDiffers(t *testing.T) {
	for _, c := range []struct {
		name   string
		result string
		want   bool
	}{
		{"same session", `{"provenance":{"session_id":"native"}}`, false},
		{"blocked before launch reports none", `{"status":"failed","infra_kind":"execution-interrupted"}`, false},
		{"different session", `{"provenance":{"session_id":"other"}}`, true},
		{"unreadable", `not json`, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := interruptionSessionDiffers(json.RawMessage(c.result), "native"); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}
