package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

func workerClaimFixture(t *testing.T) (workerOptions, *coordinator.Task, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "fixture")
	binary := filepath.Join(t.TempDir(), "fixture")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAWFLOW_OFFLOAD_COMMAND", binary)
	o := workerOptions{StateDir: t.TempDir(), Coordinator: "https://fixture", TokenFile: "/fixture/token", Executable: binary, Durable: true}
	until := time.Now().Add(time.Hour)
	task := &coordinator.Task{ID: "task", Repo: "o/r", Issue: 1, Kind: "implement", Operator: "implement", State: "leased", LeaseOwner: "worker", FencingToken: 4, LeaseExpiresAt: &until, Payload: []byte(`{"durable_execution":true}`)}
	old := *task
	old.FencingToken = 2
	path := filepath.Join(workerJournalRoot(o), task.ID, "journal.json")
	if err := writeWorkerJournal(path, &workerJournal{Version: 1, Task: old, Executable: binary, State: "acknowledged", Action: "fail", Report: json.RawMessage(`{"result":"retained"}`)}); err != nil {
		t.Fatal(err)
	}
	return o, task, path
}

func TestWorkerClaimReplaysRetryAndPreservesAcknowledgedAttempt(t *testing.T) {
	o, task, path := workerClaimFixture(t)
	dir := filepath.Dir(path)
	artifacts := map[string]string{"execution.json": `{"Session":"old-native-session"}`, "executor.log": "previous execution output\n"}
	for name, raw := range artifacts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Lock paths must retain their inodes across attempts.
	lock, err := filelock.Try(path+".lock", map[string]any{"PID": os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := lock.Stat()
	filelock.Release(lock)
	claim, _, err := prepareWorkerClaim(o)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"task": task})
	}))
	defer server.Close()
	api := &workerAPI{base: server.URL, token: "fixture", client: server.Client()}
	if err := recoverWorkerClaims(context.Background(), api, o); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(claim); !os.IsNotExist(err) {
		t.Fatal("replayed claim not cleared", err)
	}
	current, err := readWorkerJournal(path)
	if err != nil || current.Task.FencingToken != 4 || current.State != "prepared" || current.Resume || string(current.Report) != "null" {
		t.Fatal("retry was not prepared as a separate attempt", current, err)
	}
	archive := filepath.Join(workerJournalRoot(o), "history", task.ID, "epoch-2")
	old, err := readWorkerJournal(filepath.Join(archive, "journal.json"))
	if err != nil || old.Task.FencingToken != 2 || old.State != "acknowledged" || len(old.Report) == 0 {
		t.Fatal("old receipt lost", old, err)
	}
	for name, expected := range artifacts {
		raw, err := os.ReadFile(filepath.Join(archive, name))
		if err != nil || string(raw) != expected {
			t.Fatal("old artifact lost", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "execution.json")); !os.IsNotExist(err) {
		t.Fatal("previous session left as the fresh retry's recovery checkpoint", err)
	}
	after, err := os.Stat(path + ".lock")
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("lock inode replaced", err)
	}
	// Lost acknowledgements of the replay must not archive or launch again.
	if err := acceptWorkerClaim(o, task); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerClaimRefusesUnsafeRetry(t *testing.T) {
	for _, scenario := range []string{"running", "pending_sync", "rejected", "needs_recovery", "older_epoch", "different_owner", "different_subject", "expired", "legacy", "executor_lock", "session_lock"} {
		t.Run(scenario, func(t *testing.T) {
			o, task, path := workerClaimFixture(t)
			j, _ := readWorkerJournal(path)
			switch scenario {
			case "running", "pending_sync", "rejected", "needs_recovery":
				j.State = scenario
			case "older_epoch":
				task.FencingToken = 1
			case "different_owner":
				task.LeaseOwner = "another-worker"
			case "different_subject":
				task.Issue++
			case "expired":
				until := time.Now().Add(-time.Minute)
				task.LeaseExpiresAt = &until
			case "legacy":
				task.Payload = []byte(`{}`)
			case "executor_lock", "session_lock":
				lockPath := path + ".lock"
				if scenario == "session_lock" {
					lockPath = filepath.Join(filepath.Dir(path), "execution.json.lock")
				}
				lock, err := filelock.Try(lockPath, map[string]any{"PID": os.Getpid()})
				if err != nil {
					t.Fatal(err)
				}
				defer filelock.Release(lock)
			}
			if err := writeWorkerJournal(path, j); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			if err := acceptWorkerClaim(o, task); err == nil {
				t.Fatal("unsafe retry accepted")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("retained journal changed")
			}
		})
	}
}

func TestWorkerClaimArchiveSurvivesManagerRestart(t *testing.T) {
	o, _, path := workerClaimFixture(t)
	if err := archiveWorkerAttempt(o, path, 2); err != nil {
		t.Fatal(err)
	}
	if err := archiveWorkerAttempt(o, path, 2); err != nil {
		t.Fatal("archive replay failed", err)
	}
	j, _ := readWorkerJournal(path)
	j.Error = "changed evidence"
	if err := writeWorkerJournal(path, j); err != nil {
		t.Fatal(err)
	}
	if err := archiveWorkerAttempt(o, path, 2); err == nil {
		t.Fatal("conflicting archive overwritten")
	}
}

func TestWorkerClaimReplaysReleasedAttemptWithoutOldSession(t *testing.T) {
	for _, checkpoint := range []bool{false, true} {
		t.Run(fmt.Sprint(checkpoint), func(t *testing.T) {
			o, task, path := workerClaimFixture(t)
			j, _ := readWorkerJournal(path)
			j.State, j.Action, j.Report = "released", "release", nil
			if err := writeWorkerJournal(path, j); err != nil {
				t.Fatal(err)
			}
			if checkpoint {
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), "execution.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := acceptWorkerClaim(o, task)
			if checkpoint {
				if err == nil {
					t.Fatal("released session was reused")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			archived, err := readWorkerJournal(filepath.Join(workerJournalRoot(o), "history", task.ID, "epoch-2", "journal.json"))
			if err != nil || archived.State != "released" || archived.Action != "release" {
				t.Fatal("release evidence lost", archived, err)
			}
			prepared, err := readWorkerJournal(path)
			if err != nil || prepared.Task.FencingToken != task.FencingToken || prepared.State != "prepared" {
				t.Fatal(prepared, err)
			}
		})
	}
}
