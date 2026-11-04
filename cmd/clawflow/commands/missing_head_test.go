package commands

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/gitexec"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

const publishedHead = "513c325dae2a0c74799040a924196b596aaf3add"

// The real report: the work is pushed, but the worker's lookup failed.
const missingHeadReport = `{"status":"success","outcome":"agent-implemented","output":"done","effects":[{"kind":"pr-create","repo":"o/r","head":"fix/issue-667","base":"main","title":"fix","body":"Closes #667"}]}`

type branchHeadVCS struct {
	*intentVCS
	head    string
	err     error
	lookups int
}

func (c *branchHeadVCS) BranchHead(_, branch string) (string, error) {
	c.lookups++
	if branch != "fix/issue-667" {
		return "", errors.New("unexpected branch " + branch)
	}
	return c.head, c.err
}

func missingHeadFixture(t *testing.T, client *branchHeadVCS) (*coordinatorResults, *coordinator.Store, *coordinator.Task) {
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
	if err := tasks.RegisterWorker(ctx, coordinator.Worker{ID: "w", Durable: true, MaxParallel: 1, RunUntagged: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.Enqueue(ctx, coordinator.Task{Repo: "o/r", Issue: 667, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
		t.Fatal(err)
	}
	task, err := tasks.Claim(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	client.intentVCS = &intentVCS{comments: map[string]bool{}}
	job := &runJob{repo: "o/r", client: client, sub: &operator.Subject{Number: 667}, op: &operator.Operator{Name: "implement"}}
	service := &coordinatorResults{WorkerStore: tasks, tasks: tasks, loadJob: func(context.Context, *coordinator.Task) (*runJob, error) { return job, nil }}
	return service, tasks, task
}

func TestSuccessReportWithoutHeadUsesPlatformBranchHead(t *testing.T) {
	client := &branchHeadVCS{head: publishedHead}
	service, tasks, task := missingHeadFixture(t, client)
	ctx := context.Background()
	raw := json.RawMessage(missingHeadReport)
	if err := service.Complete(ctx, task.ID, "w", task.FencingToken, raw); err != nil {
		t.Fatal("pushed work rejected: ", err)
	}
	got, err := tasks.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Head    string      `json:"head_sha"`
		Effects []jobEffect `json:"effects"`
	}
	if err := json.Unmarshal(got.Result, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Head != publishedHead || len(stored.Effects) != 1 || stored.Effects[0].Kind != "pr-create" {
		t.Fatalf("stored result: %s", got.Result)
	}
	// The unchanged wire report remains acknowledged across worker retries.
	if err := service.Complete(ctx, task.ID, "w", task.FencingToken, raw); err != nil {
		t.Fatal("duplicate receipt rejected: ", err)
	}
}

func TestMissingHeadStaysInvalidWhenBranchDoesNotExist(t *testing.T) {
	client := &branchHeadVCS{}
	service, _, task := missingHeadFixture(t, client)
	err := service.Complete(context.Background(), task.ID, "w", task.FencingToken, json.RawMessage(missingHeadReport))
	if !errors.Is(err, coordinator.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
}

// A platform outage must stay retryable instead of becoming a permanent 400.
func TestMissingHeadLookupFailureIsRetryable(t *testing.T) {
	client := &branchHeadVCS{err: errors.New("HTTP 502")}
	service, tasks, task := missingHeadFixture(t, client)
	ctx := context.Background()
	err := service.Complete(ctx, task.ID, "w", task.FencingToken, json.RawMessage(missingHeadReport))
	if err == nil || errors.Is(err, coordinator.ErrInvalid) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := tasks.Task(ctx, task.ID); got == nil || len(got.Result) != 0 {
		t.Fatalf("result committed on a failed lookup: %+v", got)
	}
}

func TestResolveReportedHeadOnlyFillsSuccessfulImplementation(t *testing.T) {
	pr := jobEffect{Kind: "pr-create", Repo: "o/r", Head: "fix/issue-667"}
	for name, tc := range map[string]struct {
		task   coordinator.Task
		report coordinatorReport
		failed bool
	}{
		"worker head kept": {coordinator.Task{Kind: "implement"}, coordinatorReport{jobResult: jobResult{Status: "success", HeadSHA: publishedHead, Effects: []jobEffect{pr}}}, false},
		"failed endpoint":  {coordinator.Task{Kind: "implement"}, coordinatorReport{jobResult: jobResult{Status: "success", Effects: []jobEffect{pr}}}, true},
		"not success":      {coordinator.Task{Kind: "implement"}, coordinatorReport{jobResult: jobResult{Status: "no-marker", Effects: []jobEffect{pr}}}, false},
		"not implement":    {coordinator.Task{Kind: "repair"}, coordinatorReport{jobResult: jobResult{Status: "success", Effects: []jobEffect{pr}}}, false},
		"no PR effect":     {coordinator.Task{Kind: "implement"}, coordinatorReport{jobResult: jobResult{Status: "success"}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			client := &branchHeadVCS{head: "ffffffffffffffffffffffffffffffffffffffff"}
			want := tc.report.HeadSHA
			if err := resolveReportedHead(&tc.task, &tc.report, tc.failed, client); err != nil {
				t.Fatal(err)
			}
			if tc.report.HeadSHA != want || client.lookups != 0 {
				t.Fatalf("head=%q lookups=%d", tc.report.HeadSHA, client.lookups)
			}
		})
	}
}

func TestRetryDevelopmentHead(t *testing.T) {
	ctx := context.Background()
	calls := 0
	if err := retryDevelopmentHead(ctx, time.Millisecond, func() error {
		calls++
		if calls < 3 {
			return errors.New("Permission denied (publickey)")
		}
		return nil
	}); err != nil || calls != 3 {
		t.Fatalf("recovers from transient failure: %v after %d", err, calls)
	}
	calls = 0
	failure := errors.New("still down")
	if err := retryDevelopmentHead(ctx, time.Millisecond, func() error { calls++; return failure }); !errors.Is(err, failure) || calls != developmentHeadAttempts {
		t.Fatalf("bounded retries: %v after %d", err, calls)
	}
	calls = 0
	if err := retryDevelopmentHead(ctx, time.Millisecond, func() error { calls++; return gitexec.ErrTimeout }); !errors.Is(err, gitexec.ErrTimeout) || calls != 1 {
		t.Fatalf("timeout retried: %v after %d", err, calls)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	calls = 0
	if err := retryDevelopmentHead(cancelled, time.Hour, func() error { calls++; return failure }); !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("cancel ignored: %v after %d", err, calls)
	}
}
