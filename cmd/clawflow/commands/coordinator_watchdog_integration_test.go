package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

// Runs the production log, loop, HTTP handler and store against PostgreSQL.
func TestCoordinatorWatchdogEndToEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db := storetest.New(t)
	if _, err := db.Migrate(ctx, store.Migrations); err != nil {
		t.Fatal(err)
	}
	s := coordinator.New(db.DB())
	secret, err := s.CreateToken(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterWorker(ctx, coordinator.Worker{ID: "w", RunUntagged: true, MaxParallel: 5}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(coordinator.NewHandler(s))
	defer server.Close()
	api := &workerAPI{base: server.URL, client: server.Client(), token: secret}

	enqueue := func(issue int) *coordinator.Task {
		t.Helper()
		task, err := s.Enqueue(ctx, coordinator.Task{Repo: "o/r", Issue: issue, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":3600}`)})
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	claim := func() *coordinator.Task {
		t.Helper()
		task, err := s.Claim(ctx, "w")
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if err := db.DB().Exec(sql, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	progress := func(task *coordinator.Task, age int64) {
		t.Helper()
		in := map[string]any{"fencing_token": task.FencingToken, "last_event_age_sec": age}
		if err := api.postOnce(ctx, "tasks/"+task.ID+"/progress", in, nil); err != nil {
			t.Fatal(err)
		}
	}

	for issue := 1; issue <= 4; issue++ {
		enqueue(issue)
	}
	silent, healthy, finalizing, recovering := claim(), claim(), claim(), claim()
	waiting := enqueue(5)
	progress(silent, 2400)
	progress(healthy, 5)
	exec("UPDATE coordinator_tasks SET lease_expires_at = NULL, updated_at = now() - interval '15 minutes' WHERE id = ?", finalizing.ID)
	exec(`UPDATE coordinator_tasks SET payload = payload || '{"durable_execution":true}', lease_expires_at = now() - interval '45 minutes' WHERE id = ?`, recovering.ID)
	exec("UPDATE coordinator_tasks SET updated_at = now() - interval '15 minutes' WHERE id = ?", waiting.ID)

	// A stale fence and a wrong token are rejected over HTTP.
	if err := api.postOnce(ctx, "tasks/"+silent.ID+"/progress", map[string]any{"fencing_token": silent.FencingToken + 1, "last_event_age_sec": 0}, nil); err == nil {
		t.Fatal("stale fence accepted")
	}
	bad := &workerAPI{base: server.URL, client: server.Client(), token: "wrong"}
	if err := bad.postOnce(ctx, "tasks/"+silent.ID+"/progress", map[string]any{"fencing_token": silent.FencingToken, "last_event_age_sec": 0}, nil); err == nil {
		t.Fatal("wrong token accepted")
	}

	snapshot := func() string {
		t.Helper()
		var rows []struct {
			ID, State, LeaseOwner string
			FencingToken          int64
			Attempt               int
			LeaseExpiresAt        *time.Time
		}
		if err := db.DB().Table("coordinator_tasks").Select("id, state, lease_owner, fencing_token, attempt, lease_expires_at").Order("id").Scan(&rows).Error; err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(rows)
		return string(raw)
	}
	before := snapshot()

	loopCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveCoordinatorWatchdog(loopCtx, s.Board, 20*time.Millisecond)
	}()
	logPath := filepath.Join(clog.LogsDir(), "watchdog.log")
	readLog := func() string {
		raw, _ := os.ReadFile(logPath)
		return string(raw)
	}
	waitFor := func(what string, ok func(string) bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !ok(readLog()) {
			if time.Now().After(deadline) {
				t.Fatalf("%s; log:\n%s", what, readLog())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor("all findings opened", func(log string) bool { return strings.Count(log, "event=open") >= 4 })
	opened := readLog()
	for task, rule := range map[string]string{silent.ID: coordinator.RuleNoProgress, finalizing.ID: coordinator.RuleFinalizingStalled, recovering.ID: coordinator.RuleRecoveryStalled, waiting.ID: coordinator.RuleDispatchStalled} {
		if !strings.Contains(opened, fmt.Sprintf("task=%s", task)) || !strings.Contains(opened, "rule="+rule) {
			t.Errorf("missing %s for %s", rule, task)
		}
	}
	if strings.Contains(opened, "task="+healthy.ID) {
		t.Errorf("healthy task reported:\n%s", opened)
	}
	board, err := s.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("App waiting_reason values:")
	for _, task := range board.Tasks {
		t.Logf("  issue #%d stage=%s reason=%q", task.Issue, task.Stage, task.WaitingReason)
		if task.ID == silent.ID && !strings.HasPrefix(task.WaitingReason, "Watchdog: no execution events for 40 min.") {
			t.Errorf("reason: %q", task.WaitingReason)
		}
	}
	if after := snapshot(); after != before {
		t.Fatalf("watchdog changed task state:\n%s\n%s", before, after)
	}

	// A new event from a real tracker resolves the finding; a claim resolves dispatch.
	tracked := &execution.Progress{}
	reportWorkerProgress(ctx, api, "tasks/"+silent.ID+"/", silent.FencingToken, tracked)
	claim()
	waitFor("findings resolved", func(log string) bool { return strings.Count(log, "event=resolved") == 2 })
	stop()
	<-done
	final := readLog()
	if strings.Count(final, "event=open") != 4 {
		t.Fatalf("repeated log lines:\n%s", final)
	}
	t.Logf("watchdog.log:\n%s", final)
}
