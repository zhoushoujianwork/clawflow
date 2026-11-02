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
