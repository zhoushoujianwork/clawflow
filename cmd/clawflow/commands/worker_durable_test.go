package commands

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

func TestWorkerOutboxSurvivesLostAcknowledgement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "journal.json")
	task := coordinator.Task{ID: "task", FencingToken: 3}
	journal := &workerJournal{Version: 1, Task: task, Executable: "/fixture/clawflow", State: "running"}
	if err := writeWorkerJournal(path, journal); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(409)
			return
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	api := &workerAPI{base: server.URL, token: "fixture", client: server.Client()}
	ctx := context.WithValue(context.Background(), workerJournalKey{}, path)
	if err := persistWorkerReport(ctx, api, &task, "complete", map[string]string{"status": "success"}); err == nil {
		t.Fatal("expected stale receipt")
	}
	retained, err := readWorkerJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if retained.State != "rejected" || len(retained.Report) == 0 {
		t.Fatal("result discarded", retained)
	}
	retained.State = "pending_sync"
	if err := deliverWorkerReport(ctx, api, path, retained); err != nil {
		t.Fatal(err)
	}
	final, err := readWorkerJournal(path)
	if err != nil || final.State != "acknowledged" {
		t.Fatal(final, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("journal not private", err)
	}
}

func TestWorkerRecoveryDoesNotReplayRunningExecutor(t *testing.T) {
	o := workerOptions{StateDir: t.TempDir(), Coordinator: "https://fixture", TokenFile: "/fixture/token"}
	path := filepath.Join(workerJournalRoot(o), "task", "journal.json")
	j := &workerJournal{Version: 1, Task: coordinator.Task{ID: "task"}, Executable: "/fixture/clawflow", State: "running"}
	if err := writeWorkerJournal(path, j); err != nil {
		t.Fatal(err)
	}
	lock, err := filelock.Try(path+".lock", map[string]any{"PID": os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	if err := recoverWorkerExecutors(o); err != nil {
		t.Fatal(err)
	}
	same, err := readWorkerJournal(path)
	if err != nil || same.State != "running" {
		t.Fatal(same, err)
	}
	filelock.Release(lock)
	if err := recoverWorkerExecutors(o); err != nil {
		t.Fatal(err)
	}
	interrupted, err := readWorkerJournal(path)
	if err != nil || interrupted.State != "needs_recovery" {
		t.Fatal(interrupted, err)
	}
}

func TestWorkerJournalRejectsTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.json")
	if err := writeDurableFile(path, []byte(`{"Version":1`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkerJournal(path); err == nil {
		t.Fatal("corrupt journal accepted")
	}
}

func TestLocalWorkerLaunchHasIndependentLifetime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	path := filepath.Join(snapshot.DataDir(), "coordinator", "local-worker.token")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := startLocalWorkerProcess
	defer func() { startLocalWorkerProcess = previous }()
	launched := false
	startLocalWorkerProcess = func(o workerOptions, _ string) error {
		launched = true
		if !o.Durable || o.MaxParallel != 2 || len(o.Roles) != 2 || o.TokenFile != path || o.Coordinator != "https://127.0.0.1:9999" {
			t.Fatal(o)
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop, err := startIndependentLocalWorker(ctx, config.LocalWorkerSettings{Enabled: true, MaxParallel: 2}, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9999}, "fixture", nil, func(error) { t.Fatal("unexpected controller error") })
	if err != nil || !launched {
		t.Fatal(err)
	}
	cancel()
	stop() // No child cancellation or process-group termination is performed.
}

func TestRollingManagersTransferAdmissions(t *testing.T) {
	var oldClaims, newClaims atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/worker/v1/claim" {
			if r.Header.Get("Authorization") == "Bearer old" {
				oldClaims.Add(1)
			} else {
				newClaims.Add(1)
			}
			_, _ = w.Write([]byte(`{"task":null}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	o := workerOptions{Durable: true, Executable: "/fixture/pinned", StateDir: t.TempDir(), Coordinator: server.URL, TokenFile: "/fixture/token", MaxParallel: 1}
	oldCtx, oldCancel := context.WithCancel(context.Background())
	defer oldCancel()
	newCtx, newCancel := context.WithCancel(context.Background())
	defer newCancel()
	oldDone, newDone := make(chan error, 1), make(chan error, 1)
	build := func(context.Context, *coordinator.Task) (*runJob, time.Duration, error) {
		t.Error("unexpected execution")
		return nil, 0, nil
	}
	go func() {
		oldDone <- runManagedWorker(oldCtx, &workerAPI{base: server.URL, token: "old", client: server.Client()}, o, build)
	}()
	wait := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for !condition() {
			if time.Now().After(deadline) {
				t.Fatal("manager did not become ready")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	wait(func() bool { return oldClaims.Load() > 0 })
	go func() {
		newDone <- runManagedWorker(newCtx, &workerAPI{base: server.URL, token: "new", client: server.Client()}, o, build)
	}()
	wait(func() bool { return newClaims.Load() > 0 })
	oldCount := oldClaims.Load()
	// The previous manager has released admissions; existing task executors are
	// outside both manager contexts. KeepAlive standby must not reclaim work.
	time.Sleep(250 * time.Millisecond)
	if oldClaims.Load() != oldCount {
		t.Fatal("old generation continued admissions")
	}
	oldCancel()
	newCancel()
	for _, done := range []chan error{oldDone, newDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("manager did not stop")
		}
	}
}

func TestDurableWorkerTaskSurvivesControllerOutage(t *testing.T) {
	var online atomic.Bool
	var heartbeats atomic.Int64
	online.Store(false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !online.Load() {
			if r.URL.Path == "/worker/v1/tasks/task/heartbeat" {
				heartbeats.Add(1)
			}
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	task := coordinator.Task{ID: "task", Repo: "o/r", Issue: 1, Kind: "implement", FencingToken: 1, Payload: []byte(`{"durable_execution":true}`)}
	until := time.Now().Add(time.Hour)
	task.LeaseExpiresAt = &until
	path := filepath.Join(t.TempDir(), "journal.json")
	if err := writeWorkerJournal(path, &workerJournal{Version: 1, Executable: "/fixture/cli", Task: task, State: "running"}); err != nil {
		t.Fatal(err)
	}
	api := &workerAPI{base: server.URL, token: "fixture", client: server.Client()}
	old := runOneOperatorFn
	defer func() { runOneOperatorFn = old }()
	runOneOperatorFn = func(ctx context.Context, j *runJob, _ time.Duration) (bool, bool, bool) {
		deadline := time.Now().Add(2 * time.Second)
		for heartbeats.Load() == 0 {
			if time.Now().After(deadline) {
				t.Error("no heartbeat reached offline controller")
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if ctx.Err() != nil {
			t.Error("transport outage cancelled execution")
		}
		j.hooks.AfterRun(&jobResult{Status: "success"})
		return true, false, false
	}
	ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), workerJournalKey{}, path), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runWorkerTask(ctx, api, workerOptions{RunUntagged: true, Roles: []string{"operator"}}, &task, 10*time.Millisecond, func(context.Context, *coordinator.Task) (*runJob, time.Duration, error) {
			return &runJob{repo: "o/r", sub: &operator.Subject{Number: 1}, op: &operator.Operator{Name: "implement"}}, time.Minute, nil
		})
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		journal, err := readWorkerJournal(path)
		if err == nil && journal.State == "pending_sync" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completed result was not retained during outage")
		}
		time.Sleep(10 * time.Millisecond)
	}
	online.Store(true)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not reconnect")
	}
	journal, err := readWorkerJournal(path)
	if err != nil || journal.State != "acknowledged" {
		t.Fatal(journal, err)
	}
}
