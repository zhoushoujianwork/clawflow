package commands

import (
	"bytes"
	"context"
	"encoding/json"
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
		cp.ArtifactDir = filepath.Join(root, "artifacts")
		if err := os.MkdirAll(cp.ArtifactDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cp.ArtifactDir, "result.json"), []byte(`{"status":"ok"}`), 0600); err != nil {
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
	if err == nil || !strings.Contains(err.Error(), "offload#100") || *calls != 0 {
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
	cp, err := execution.ReadCheckpoint(filepath.Join(filepath.Dir(path), "execution.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cp.ArtifactDir, "result.json"), []byte(`{"status":"error"}`), 0600); err != nil {
		t.Fatal(err)
	}
	old := abandonOrphanedRun
	called := false
	abandonOrphanedRun = func(context.Context, string) (AbandonState, error) {
		called = true
		return AbandonEnded, nil
	}
	defer func() { abandonOrphanedRun = old }()
	err = prepareWorkerRecovery(context.Background(), api, path)
	if err == nil || !strings.Contains(err.Error(), "completed native result retained") || *calls != 0 || called {
		t.Fatalf("err=%v calls=%d abandon_called=%v", err, *calls, called)
	}
}
