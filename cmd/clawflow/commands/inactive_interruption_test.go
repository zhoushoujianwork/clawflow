package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// workerRecoveryReport is the report runWorkerTask builds when a retained
// session fails its launch checks: the build error becomes the error text and
// the status is rewritten to an execution interruption (worker_run.go).
func workerRecoveryReport(cause error) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"status": "failed", "infra_kind": "execution-interrupted", "error": cause.Error(), "next_owner": "scheduling"})
	return raw
}

// A retained interruption whose recovery finds the issue closed or owner-skipped
// ends and frees the slot; any other cause of the same coordination-changed
// error keeps the task reserved, as before.
func TestRetainedInterruptionEndsWhenIssueIsInactive(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(c *dispatchClient)
		ends   bool
	}{
		{"issue closed", func(c *dispatchClient) { c.issue.State = "closed" }, true},
		{"owner skipped", func(c *dispatchClient) { c.issue.Labels = []string{"clawflow-skip"} }, true},
		{"new discussion", func(c *dispatchClient) { c.comments = append(c.comments, vcs.IssueComment{ID: 9, Body: "new comment"}) }, false},
		{"issue edited", func(c *dispatchClient) { c.issue.Body = "changed requirements" }, false},
		{"provider unavailable", func(c *dispatchClient) { c.failRead = true }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Controller side: the snapshot approved when the task was queued.
			j, c, _, _ := coordinationFixture(t)
			approved, err := collectCoordination(context.Background(), j, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			// Worker side: recovery re-checks the issue after it changed.
			tc.change(c)
			probe := &runJob{op: j.op, repo: j.repo, sub: &operator.Subject{Number: 1}, client: c}
			cause := validateWorkerCoordination(context.Background(), probe, &approved)
			if !errors.Is(cause, errCoordinationChanged) {
				t.Fatalf("worker did not report a coordination change: %v", cause)
			}
			if got := strings.Contains(cause.Error(), "closed or owner-skipped"); got != tc.ends {
				t.Fatalf("cause %q: closed-or-skipped = %v, want %v", cause, got, tc.ends)
			}

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
			if _, err := tasks.Enqueue(ctx, coordinator.Task{Repo: "o/r", Issue: 773, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
				t.Fatal(err)
			}
			task, err := tasks.Claim(ctx, "w")
			if err != nil {
				t.Fatal(err)
			}
			vc := &intentVCS{comments: map[string]bool{}}
			job := &runJob{repo: "o/r", client: vc, sub: &operator.Subject{Number: 773}, op: &operator.Operator{Name: "implement"}}
			service := &coordinatorResults{WorkerStore: tasks, tasks: tasks, loadJob: func(context.Context, *coordinator.Task) (*runJob, error) { return job, nil }, apply: applyCoordinatorReport}

			// The real path: the worker reports, the controller retains it.
			raw := workerRecoveryReport(cause)
			if err := service.Fail(ctx, task.ID, "w", task.FencingToken, raw); err != nil {
				t.Fatal(err)
			}
			if err := service.recoverResults(ctx, tasks); err != nil {
				t.Fatal(err)
			}
			if got, err := tasks.Task(ctx, task.ID); err != nil || got.State != "leased" {
				t.Fatalf("interruption was not retained first: %+v, %v", got, err)
			}
			if _, err := tasks.Enqueue(ctx, coordinator.Task{Repo: "o/r", Issue: 900, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
				t.Fatal(err)
			}
			if _, err := tasks.Claim(ctx, "other"); !errors.Is(err, coordinator.ErrNoTask) {
				t.Fatalf("retained task does not hold the slot: %v", err)
			}

			if err := endInactiveInterruptions(ctx, tasks); err != nil {
				t.Fatal(err)
			}

			got, err := tasks.Task(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			log, _ := os.ReadFile(filepath.Join(clog.LogsDir(), "run.log"))
			if !tc.ends {
				if got.State != "leased" || got.LeaseOwner != "w" || got.FencingToken != task.FencingToken {
					t.Fatalf("a different cause ended the task: %+v", got)
				}
				if _, err := tasks.Claim(ctx, "other"); !errors.Is(err, coordinator.ErrNoTask) {
					t.Fatalf("slot was released for a retained task: %v", err)
				}
				if strings.Contains(string(log), "interruption_ended") {
					t.Fatalf("ending was logged for a retained task:\n%s", log)
				}
				return
			}
			if got.State != "cancelled" || got.LeaseExpiresAt != nil || got.FencingToken != task.FencingToken {
				t.Fatalf("task not terminal: %+v", got)
			}
			if next, err := tasks.Claim(ctx, "other"); err != nil || next.Issue != 900 {
				t.Fatalf("slot not released: %+v, %v", next, err)
			}
			// Nothing was created or written to the VCS.
			if vc.creates != 0 || vc.commentWrites != 0 {
				t.Fatalf("VCS written: creates=%d comments=%d", vc.creates, vc.commentWrites)
			}
			if len(c.writes) != 0 {
				t.Fatalf("issue written: %v", c.writes)
			}
			var n int64
			if err := db.DB().Model(&coordinator.Task{}).Where("issue = 773").Count(&n).Error; err != nil || n != 1 {
				t.Fatalf("tasks for the issue = %d, %v", n, err)
			}
			if err := db.DB().Model(&coordinator.EffectIntent{}).Where("task_id = ?", task.ID).Count(&n).Error; err != nil || n != 0 {
				t.Fatalf("effect intents = %d, %v", n, err)
			}
			// The reason is in the log.
			text := string(log)
			for _, want := range []string{"coordinator/interruption_ended", "task=" + task.ID, "issue=773", "closed or owner-skipped", "retained worktree and session"} {
				if !strings.Contains(text, want) {
					t.Fatalf("log lacks %q:\n%s", want, text)
				}
			}
		})
	}
}
