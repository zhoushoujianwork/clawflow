package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

// The real failure: an operator queued a PR, then ended without an outcome
// marker and without a verified head. No later report can improve on it.
const poisonReport = `{"status":"no-marker","error":"operator produced no outcome marker","effects":[{"kind":"pr-create","repo":"o/r","head":"fix/x","base":"main","title":"fix"},{"kind":"issue-comment","repo":"o/r","number":661,"body":"note"}]}`

func poisonFixture(t *testing.T, durable bool) (*coordinatorResults, *coordinator.Store, *coordinator.Task) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	if err := (&config.Config{}).Save(); err != nil {
		t.Fatal(err)
	}
	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, store.Migrations); err != nil {
		t.Fatal(err)
	}
	tasks := coordinator.New(db.DB())
	if err := tasks.RegisterWorker(ctx, coordinator.Worker{ID: "w", Durable: durable, MaxParallel: 1, RunUntagged: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.Enqueue(ctx, coordinator.Task{Repo: "o/r", Issue: 661, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
		t.Fatal(err)
	}
	task, err := tasks.Claim(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	job := &runJob{repo: "o/r", client: &intentVCS{comments: map[string]bool{}}, sub: &operator.Subject{Number: 661}, op: &operator.Operator{Name: "implement"}}
	service := &coordinatorResults{WorkerStore: tasks, tasks: tasks, loadJob: func(context.Context, *coordinator.Task) (*runJob, error) { return job, nil }}
	return service, tasks, task
}

func TestFailedDurableReportQuarantinesIncompletePR(t *testing.T) {
	service, tasks, task := poisonFixture(t, true)
	ctx := context.Background()
	raw := json.RawMessage(poisonReport)
	if err := service.Fail(ctx, task.ID, "w", task.FencingToken, raw); err != nil {
		t.Fatal("report that can never improve was rejected: ", err)
	}
	got, err := tasks.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Effects   []jobEffect `json:"effects"`
		Unapplied []jobEffect `json:"unapplied_effects"`
	}
	if err := json.Unmarshal(got.Result, &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Unapplied) != 1 || stored.Unapplied[0].Kind != "pr-create" {
		t.Fatalf("PR request not retained as evidence: %s", got.Result)
	}
	if len(stored.Effects) != 1 || stored.Effects[0].Kind != "issue-comment" {
		t.Fatalf("valid effect lost or PR publishable: %s", got.Result)
	}
	if err := service.Fail(ctx, task.ID, "w", task.FencingToken, raw); err != nil {
		t.Fatal("duplicate receipt rejected: ", err)
	}
}

func TestIncompletePRStaysInvalidOutsideFailedDurableReports(t *testing.T) {
	ctx := context.Background()
	t.Run("complete endpoint", func(t *testing.T) {
		service, _, task := poisonFixture(t, true)
		if err := service.Complete(ctx, task.ID, "w", task.FencingToken, json.RawMessage(poisonReport)); !errors.Is(err, coordinator.ErrInvalid) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("non-durable", func(t *testing.T) {
		service, _, task := poisonFixture(t, false)
		if err := service.Fail(ctx, task.ID, "w", task.FencingToken, json.RawMessage(poisonReport)); !errors.Is(err, coordinator.ErrInvalid) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRejectedReportIsRetainedAndRetriedSlowly(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(400)
	}))
	defer server.Close()
	api := &workerAPI{base: server.URL, token: "fixture", client: server.Client()}
	path := filepath.Join(t.TempDir(), "journal.json")
	j := &workerJournal{Version: 1, Task: coordinator.Task{ID: "task", FencingToken: 1}, Executable: filepath.Join(t.TempDir(), "missing"), State: "pending_sync", Action: "fail", Report: json.RawMessage(`{"fencing_token":1,"result":{}}`)}
	if err := writeWorkerJournal(path, j); err != nil {
		t.Fatal(err)
	}
	if err := deliverWorkerReport(context.Background(), api, path, j); !errors.Is(err, workerHTTPError(400)) {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	retained, err := readWorkerJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if retained.State != "pending_sync" || retained.Error != workerReportRejected || len(retained.Report) == 0 {
		t.Fatalf("report not retained: %+v", retained)
	}
	// Within the window recovery does not start an executor at all.
	if err := recoverWorkerExecutor(workerOptions{}, path); err != nil {
		t.Fatalf("retried too soon: %v", err)
	}
	// After the window it retries, so a newer controller can still accept it.
	retained.UpdatedAt = time.Now().Add(-workerRejectedRetry - time.Minute)
	raw, err := json.Marshal(retained)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeDurableFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recoverWorkerExecutor(workerOptions{}, path); err == nil {
		t.Fatal("expected a retry attempt against the missing fixture executable")
	}
}
