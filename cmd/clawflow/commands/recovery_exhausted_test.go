package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

// commentVCS records issue comments with their idempotency marker and body, and
// fails the labels a product-failure circuit breaker would write.
type commentVCS struct {
	intentVCS
	bodies  map[string]string
	labels  []string
	postErr error
}

func (c *commentVCS) PostIssueCommentIdempotent(repo string, n int, body, marker string) error {
	if c.postErr != nil {
		return c.postErr
	}
	if _, ok := c.bodies[marker]; !ok {
		c.bodies[marker] = body
		c.commentWrites++
	}
	return nil
}

func (c *commentVCS) AddLabel(_ string, _ int, labels ...string) error {
	c.labels = append(c.labels, labels...)
	return nil
}

type exhaustedFixture struct {
	ctx     context.Context
	db      *store.Store
	tasks   *coordinator.Store
	vc      *commentVCS
	service *coordinatorResults
	log     func() string
}

func newExhaustedFixture(t *testing.T) *exhaustedFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	cfg := &config.Config{}
	cfg.Settings.MaxConsecutiveFailures = 1 // any product-failure automation would label the issue
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	old := runLog
	lg, err := clog.Open("run")
	if err != nil {
		t.Fatal(err)
	}
	runLog = lg
	t.Cleanup(func() { lg.Close(); runLog = old })

	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, store.Migrations); err != nil {
		t.Fatal(err)
	}
	tasks := coordinator.New(db.DB())
	tasks.LeaseLimits = func() (int, map[string]int, error) { return 10, map[string]int{"o/r": 1}, nil }
	for _, w := range []string{"w", "other"} {
		if err := tasks.RegisterWorker(ctx, coordinator.Worker{ID: w, Durable: true, MaxParallel: 1, RunUntagged: true}); err != nil {
			t.Fatal(err)
		}
	}
	vc := &commentVCS{intentVCS: intentVCS{comments: map[string]bool{}}, bodies: map[string]string{}}
	job := &runJob{repo: "o/r", client: vc, sub: &operator.Subject{Number: 787}, op: &operator.Operator{Name: "implement"}}
	service := &coordinatorResults{WorkerStore: tasks, tasks: tasks, loadJob: func(context.Context, *coordinator.Task) (*runJob, error) { return job, nil }, apply: applyCoordinatorReport}
	return &exhaustedFixture{ctx: ctx, db: db, tasks: tasks, vc: vc, service: service, log: func() string {
		data, _ := os.ReadFile(filepath.Join(clog.LogsDir(), "run.log"))
		return string(data)
	}}
}

const exhaustedReport = `{"status":"failed","infra_kind":"execution-interrupted","error":"context deadline exceeded","provenance":{"session_id":"retained-native"}}`

// reportAndApply is the real path: the worker reports, the controller's
// reconcile loop applies the result.
func (f *exhaustedFixture) reportAndApply(t *testing.T, task *coordinator.Task) {
	t.Helper()
	if err := f.service.Fail(f.ctx, task.ID, "w", task.FencingToken, json.RawMessage(exhaustedReport)); err != nil {
		t.Fatal(err)
	}
	if err := f.service.recoverResults(f.ctx, f.tasks); err != nil {
		t.Fatal(err)
	}
}

func (f *exhaustedFixture) start(t *testing.T) *coordinator.Task {
	t.Helper()
	if _, err := f.tasks.Enqueue(f.ctx, coordinator.Task{Repo: "o/r", Issue: 787, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
		t.Fatal(err)
	}
	task, err := f.tasks.Claim(f.ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	return task
}

// Before the budget is spent: retained, silent, slot held.
// After: failed, slot free, and the issue says why.
func TestRecoveryExhaustionEndsTaskAndAnnouncesOnIssue(t *testing.T) {
	f := newExhaustedFixture(t)
	task := f.start(t)
	for i := 0; i < coordinator.MaxInfrastructureAttempts-1; i++ {
		f.reportAndApply(t, task)
		got, err := f.tasks.Task(f.ctx, task.ID)
		if err != nil || got.State != "leased" {
			t.Fatalf("attempt %d: interruption was not retained: %+v, %v", i, got, err)
		}
		if f.vc.commentWrites != 0 {
			t.Fatalf("attempt %d: issue was written before the budget ran out", i)
		}
		if task, err = f.tasks.RecoverExecution(f.ctx, task.ID, "w", task.FencingToken); err != nil {
			t.Fatalf("attempt %d: recovery not authorized: %v", i, err)
		}
	}
	if _, err := f.tasks.Enqueue(f.ctx, coordinator.Task{Repo: "o/r", Issue: 900, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tasks.Claim(f.ctx, "other"); !errors.Is(err, coordinator.ErrNoTask) {
		t.Fatalf("slot not held before the last interruption: %v", err)
	}

	f.reportAndApply(t, task)

	got, err := f.tasks.Task(f.ctx, task.ID)
	if err != nil || got.State != "failed" || got.LeaseExpiresAt != nil || got.ApplicationError != "" {
		t.Fatalf("exhausted task not failed: %+v, %v", got, err)
	}
	if !coordinator.MatchingReport(got, "w", task.FencingToken, json.RawMessage(exhaustedReport), true) {
		t.Fatal("the worker's acknowledged report no longer matches")
	}
	if next, err := f.tasks.Claim(f.ctx, "other"); err != nil || next.Issue != 900 {
		t.Fatalf("slot not released: %+v, %v", next, err)
	}

	// One idempotent comment on the issue, stating the exhaustion and task.
	body, ok := f.vc.bodies[recoveryExhaustedRunID(task.ID)]
	if !ok || f.vc.commentWrites != 1 {
		t.Fatalf("comments = %d, bodies = %v", f.vc.commentWrites, f.vc.bodies)
	}
	for _, want := range []string{"恢复次数已用尽", "Recovery attempts exhausted", task.ID, "worktree", "failed"} {
		if !strings.Contains(body, want) {
			t.Fatalf("comment lacks %q:\n%s", want, body)
		}
	}
	// An infrastructure ending is not a product failure: no agent-failed label.
	if len(f.vc.labels) != 0 {
		t.Fatalf("labels written: %v", f.vc.labels)
	}
	// The run record carries the reason and keeps the worker's last error.
	final, err := f.tasks.Task(f.ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := os.ReadFile(filepath.Join(snapshot.RunDir("o/r", 787, final.CreatedAt)+"-task-"+task.ID, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"recovery attempts exhausted", "context deadline exceeded", `"status": "failed"`} {
		if !strings.Contains(string(meta), want) {
			t.Fatalf("run record lacks %q:\n%s", want, meta)
		}
	}

	// Replaying the application changes nothing: no second comment, no revival.
	if err := f.service.recoverResults(f.ctx, f.tasks); err != nil {
		t.Fatal(err)
	}
	if f.vc.commentWrites != 1 {
		t.Fatalf("announcement repeated: %d", f.vc.commentWrites)
	}
	var tasks int64
	if err := f.db.DB().Model(&coordinator.Task{}).Where("issue = 787").Count(&tasks).Error; err != nil || tasks != 1 {
		t.Fatalf("tasks for the issue = %d, %v", tasks, err)
	}
}

// A task an earlier controller left reserved is announced and ended by the
// reconcile sweep. A failed comment keeps it reserved and is retried.
func TestEndExhaustedInterruptionsSweepAnnouncesThenEnds(t *testing.T) {
	f := newExhaustedFixture(t)
	task := f.start(t)
	for i := 0; i < coordinator.MaxInfrastructureAttempts-1; i++ {
		f.reportAndApply(t, task)
		var err error
		if task, err = f.tasks.RecoverExecution(f.ctx, task.ID, "w", task.FencingToken); err != nil {
			t.Fatal(err)
		}
	}
	// Reproduce the old controller: the last report is committed and the task is
	// left leased with an expired lease.
	raw := json.RawMessage(exhaustedReport)
	if err := f.tasks.AcceptResult(coordinator.WithReportDigest(f.ctx, raw, true), task.ID, "w", task.FencingToken, raw); err != nil {
		t.Fatal(err)
	}
	if err := f.db.DB().Model(&coordinator.Task{}).Where("id = ?", task.ID).Update("lease_expires_at", task.CreatedAt).Error; err != nil {
		t.Fatal(err)
	}

	f.vc.postErr = errors.New("provider down")
	if err := f.service.endExhaustedInterruptions(f.ctx, f.tasks); err == nil {
		t.Fatal("announcement failure was swallowed")
	}
	if got, _ := f.tasks.Task(f.ctx, task.ID); got.State != "leased" || got.ApplicationAttempts != 1 {
		t.Fatalf("task ended without announcing: %+v", got)
	}

	f.vc.postErr = nil
	if err := f.service.endExhaustedInterruptions(f.ctx, f.tasks); err != nil {
		t.Fatal(err)
	}
	got, err := f.tasks.Task(f.ctx, task.ID)
	if err != nil || got.State != "failed" || got.LeaseExpiresAt != nil {
		t.Fatalf("task not failed: %+v, %v", got, err)
	}
	if _, ok := f.vc.bodies[recoveryExhaustedRunID(task.ID)]; !ok || f.vc.commentWrites != 1 {
		t.Fatalf("comments = %d, bodies = %v", f.vc.commentWrites, f.vc.bodies)
	}
	text := f.log()
	for _, want := range []string{"coordinator/recovery_exhausted", "task=" + task.ID, "issue=787", "recovery attempts exhausted"} {
		if !strings.Contains(text, want) {
			t.Fatalf("log lacks %q:\n%s", want, text)
		}
	}
	if len(f.vc.labels) != 0 {
		t.Fatalf("labels written: %v", f.vc.labels)
	}
	// Idempotent.
	if err := f.service.endExhaustedInterruptions(f.ctx, f.tasks); err != nil || f.vc.commentWrites != 1 {
		t.Fatalf("sweep repeated: %v, comments=%d", err, f.vc.commentWrites)
	}
}
