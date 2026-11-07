package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/retry"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// flakyVCS fails the first failures PR lookups with err and then behaves like
// intentVCS. It counts lookups by method so tests can prove the lookup is one
// head-filtered request and never a walk of the PR history.
type flakyVCS struct {
	intentVCS
	failures    int
	err         error
	headLookups int
	historyWalk int
}

func (c *flakyVCS) ListPRs(string, string) ([]vcs.PR, error) {
	c.historyWalk++
	return nil, errors.New("must not walk PR history")
}

func (c *flakyVCS) ListPRsForHead(_, branch string) ([]vcs.PR, error) {
	c.headLookups++
	if c.failures > 0 {
		c.failures--
		return nil, c.err
	}
	var out []vcs.PR
	for _, p := range c.prs {
		if p.HeadBranch == branch {
			out = append(out, p)
		}
	}
	return out, nil
}

func timeoutErr() error {
	return fmt.Errorf("github list PRs: %w", &url.Error{Op: "Get", URL: "https://api.github.com/repos/o/r/pulls?state=all&head=o%3Afix&per_page=100&page=1", Err: context.DeadlineExceeded})
}

type backoffFixture struct {
	tasks   *coordinator.Store
	task    *coordinator.Task
	service *coordinatorResults
	client  *flakyVCS
	clock   *time.Time
	applied int
	db      *store.Store
}

func newBackoffFixture(t *testing.T, raw string) *backoffFixture {
	t.Helper()
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	withRunLog(t)
	db := openDefaultSQLiteTestStore(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, store.Migrations); err != nil {
		t.Fatal(err)
	}
	tasks := coordinator.New(db.DB())
	if err := tasks.RegisterWorker(ctx, coordinator.Worker{ID: "w", MaxParallel: 1, RunUntagged: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.Enqueue(ctx, coordinator.Task{Repo: "o/r", Issue: 1, Kind: "implement", Operator: "implement"}); err != nil {
		t.Fatal(err)
	}
	task, err := tasks.Claim(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	f := &backoffFixture{tasks: tasks, task: task, db: db, clock: new(time.Time)}
	*f.clock = time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	f.client = &flakyVCS{intentVCS: intentVCS{head: strings.Repeat("a", 40), comments: map[string]bool{}}}
	job := &runJob{client: f.client, repo: "o/r", sub: &operator.Subject{Number: 1}, op: &operator.Operator{Name: "implement"}}
	f.service = &coordinatorResults{WorkerStore: tasks, tasks: tasks, now: func() time.Time { return *f.clock },
		loadJob: func(context.Context, *coordinator.Task) (*runJob, error) { return job, nil },
		apply: func(ctx context.Context, task *coordinator.Task, j *runJob, r *coordinatorReport) error {
			f.applied++
			return applyCoordinatorReport(ctx, task, j, r)
		}}
	if err := f.service.Complete(ctx, task.ID, "w", task.FencingToken, json.RawMessage(raw)); err != nil {
		t.Fatal(err)
	}
	return f
}

const successWithEffects = `{"status":"success","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","effects":[{"kind":"pr-create","repo":"o/r","head":"fix/issue-1","base":"main","title":"fix"},{"kind":"issue-comment","repo":"o/r","number":1,"body":"done"}]}`

func (f *backoffFixture) state(t *testing.T) *coordinator.Task {
	t.Helper()
	got, err := f.tasks.Task(context.Background(), f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// The reported incident: write-back of a succeeded task times out repeatedly.
// The task must never reach failed, the diagnostic stays visible, and after the
// VCS recovers the PR and comment are written exactly once.
func TestSucceededTaskSurvivesTimeoutsAndAppliesOnce(t *testing.T) {
	f := newBackoffFixture(t, successWithEffects)
	f.client.failures, f.client.err = 1000, timeoutErr()
	ctx := context.Background()

	// Many more passes than the old three-failure bound. Each pass is made
	// after the backoff elapsed, so each one really calls the VCS.
	for i := 0; i < 8; i++ {
		before := f.client.headLookups
		if err := f.service.recoverResults(ctx, f.tasks); err != nil {
			t.Fatal(err)
		}
		if f.client.headLookups != before+1 {
			t.Fatalf("pass %d: head lookups %d -> %d, want exactly one request", i, before, f.client.headLookups)
		}
		got := f.state(t)
		if got.State != "leased" || got.ApplicationAttempts != 0 || len(got.Result) == 0 {
			t.Fatalf("pass %d: succeeded task left the pending-result state: %+v", i, got)
		}
		if !strings.Contains(got.ApplicationError, "context deadline exceeded") {
			t.Fatalf("pass %d: diagnostic missing: %q", i, got.ApplicationError)
		}
		*f.clock = f.clock.Add(transientRetryMax + time.Second)
	}
	if f.client.creates != 0 || f.client.commentWrites != 0 || f.applied != 0 {
		t.Fatalf("effects written while the VCS was unreachable: creates=%d comments=%d applied=%d", f.client.creates, f.client.commentWrites, f.applied)
	}

	// The VCS recovers. Several passes, as after restarts, must write once.
	f.client.failures = 0
	for i := 0; i < 3; i++ {
		if err := f.service.recoverResults(ctx, f.tasks); err != nil {
			t.Fatal(err)
		}
	}
	got := f.state(t)
	if got.State != "done" || got.ApplicationAttempts != 0 || got.ApplicationError != "" || got.PR != 11 {
		t.Fatalf("after recovery: %+v", got)
	}
	if f.client.creates != 1 || f.client.commentWrites != 1 || f.applied != 1 || f.client.historyWalk != 0 {
		t.Fatalf("effects not exactly-once: creates=%d comments=%d applied=%d historyWalk=%d", f.client.creates, f.client.commentWrites, f.applied, f.client.historyWalk)
	}
	var intents []coordinator.EffectIntent
	if err := f.db.DB().Where("task_id = ?", f.task.ID).Find(&intents).Error; err != nil || len(intents) != 2 {
		t.Fatalf("intents=%+v err=%v", intents, err)
	}
	for _, in := range intents {
		if in.Status != "applied" {
			t.Fatalf("intent %s: %s", in.Kind, in.Status)
		}
	}
}

// A timeout that lands after GitHub accepted the create must not produce a
// second PR on retry: the head lookup finds the first one.
func TestRetryAfterLostCreateResponseDoesNotDuplicatePR(t *testing.T) {
	f := newBackoffFixture(t, successWithEffects)
	ctx := context.Background()
	f.client.createErr = timeoutErr()
	// The create reached GitHub (race PR registered) but the response was lost.
	f.client.racePR = &vcs.PR{Number: 11, HeadRepo: "o/r", HeadBranch: "fix/issue-1", BaseBranch: "main", State: "open", URL: "https://example.test/pull/11"}
	if err := f.service.recoverResults(ctx, f.tasks); err != nil {
		t.Fatal(err)
	}
	if got := f.state(t); got.State != "leased" || got.ApplicationAttempts != 0 {
		// The reconciliation lookup after the failed create already finds the
		// PR, so the pass may even finish. It must never fail the task.
		if got.State == "failed" {
			t.Fatalf("task failed: %+v", got)
		}
	}
	f.client.createErr = nil
	*f.clock = f.clock.Add(transientRetryMax + time.Second)
	for i := 0; i < 3; i++ {
		if err := f.service.recoverResults(ctx, f.tasks); err != nil {
			t.Fatal(err)
		}
	}
	got := f.state(t)
	if got.State != "done" || f.client.creates != 1 || f.client.commentWrites != 1 {
		t.Fatalf("state=%s creates=%d comments=%d", got.State, f.client.creates, f.client.commentWrites)
	}
}

// Retries are paced: within the backoff window the VCS is not called at all.
func TestTransientFailureBacksOffBetweenRetries(t *testing.T) {
	f := newBackoffFixture(t, successWithEffects)
	f.client.failures, f.client.err = 1000, timeoutErr()
	ctx := context.Background()
	if err := f.service.recoverResults(ctx, f.tasks); err != nil {
		t.Fatal(err)
	}
	calls := f.client.headLookups
	for i := 0; i < 5; i++ {
		*f.clock = f.clock.Add(transientRetryBase / 10)
		if err := f.service.recoverResults(ctx, f.tasks); err != nil {
			t.Fatal(err)
		}
	}
	if f.client.headLookups != calls {
		t.Fatalf("VCS called %d more times inside the backoff window", f.client.headLookups-calls)
	}
	*f.clock = f.clock.Add(transientRetryBase)
	if err := f.service.recoverResults(ctx, f.tasks); err != nil {
		t.Fatal(err)
	}
	if f.client.headLookups != calls+1 {
		t.Fatalf("retry after backoff: lookups %d -> %d", calls, f.client.headLookups)
	}
}

// Unrecoverable errors keep the existing three-attempt bound and end failed.
func TestPermanentWritebackErrorStillFailsTheTask(t *testing.T) {
	f := newBackoffFixture(t, successWithEffects)
	f.client.failures, f.client.err = 1000, errors.New("github list PRs: HTTP 422: Validation Failed")
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := f.service.recoverResults(ctx, f.tasks); err != nil {
			t.Fatal(err)
		}
	}
	got := f.state(t)
	if got.State != "failed" || got.ApplicationAttempts != 3 || !strings.Contains(got.ApplicationError, "422") {
		t.Fatalf("permanent error: %+v", got)
	}
	if f.client.headLookups != 3 {
		t.Fatalf("lookups=%d, want 3 (no retry after the task failed)", f.client.headLookups)
	}
}

// A transient error does not protect a task that did not succeed: a failed
// report keeps the bounded behaviour.
func TestTransientErrorOfFailedReportKeepsBound(t *testing.T) {
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	if deferrable(timeoutErr(), json.RawMessage(`{"status":"failed","coordinator_state":"failed"}`)) {
		t.Fatal("failed report was deferred")
	}
	if deferrable(timeoutErr(), json.RawMessage(`{"status":"blocked","coordinator_state":"failed"}`)) {
		t.Fatal("blocked report was deferred")
	}
	if !deferrable(timeoutErr(), json.RawMessage(`{"status":"success","coordinator_state":"done"}`)) {
		t.Fatal("succeeded report was not deferred")
	}
	if !deferrable(&retry.HTTPError{Status: 503}, json.RawMessage(`{"status":"skipped"}`)) {
		t.Fatal("skipped report with HTTP 503 was not deferred")
	}
	if deferrable(errors.New("HTTP 422"), json.RawMessage(`{"status":"success"}`)) {
		t.Fatal("permanent error was deferred")
	}
}

func TestResultBackoffDoublesAndCaps(t *testing.T) {
	var b resultBackoff
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 10 * time.Minute, 10 * time.Minute}
	for i, w := range want {
		if got := b.failed("t", 1, now); got != w {
			t.Fatalf("failure %d: delay %s, want %s", i+1, got, w)
		}
	}
	if b.wait("t", 1, now) <= 0 {
		t.Fatal("not waiting right after a failure")
	}
	if b.wait("t", 2, now) != 0 {
		t.Fatal("a new lease epoch must not inherit the backoff")
	}
	b.clear("t")
	if b.wait("t", 1, now) != 0 || b.failures("t") != 0 {
		t.Fatal("clear did not reset")
	}
}
