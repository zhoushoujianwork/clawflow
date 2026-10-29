package commands

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type intentVCS struct {
	vcs.Client
	prs           []vcs.PR
	creates       int
	comments      map[string]bool
	commentWrites int
	head          string
}

func (c *intentVCS) ListPRs(string, string) ([]vcs.PR, error) { return c.prs, nil }
func (c *intentVCS) CreatePR(repo string, o vcs.PRCreateOpts) (vcs.PR, error) {
	c.creates++
	p := vcs.PR{Number: 11, HeadRepo: repo, HeadBranch: o.Head, BaseBranch: o.Base, Body: o.Body, State: "open", HeadSHA: c.head, URL: "https://example.test/pull/11"}
	c.prs = append(c.prs, p)
	return p, nil
}
func (c *intentVCS) GetPR(string, int) (vcs.PR, error) {
	p := c.prs[0]
	p.HeadSHA = c.head
	return p, nil
}
func (c *intentVCS) PostIssueCommentIdempotent(_ string, _ int, _ string, marker string) error {
	if !c.comments[marker] {
		c.comments[marker] = true
		c.commentWrites++
	}
	return nil
}
func TestPRIntentReconcilesAndVerifiesHead(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		c := &intentVCS{head: strings.Repeat("a", 40)}
		if mismatch {
			c.head = strings.Repeat("b", 40)
		}
		j := &runJob{client: c}
		r := &coordinatorReport{jobResult: jobResult{HeadSHA: strings.Repeat("a", 40)}}
		payload, _ := json.Marshal(jobEffect{Kind: "pr-create", Repo: "o/r", Head: "fix/1", Base: "main", Title: "fix", RunID: "stable"})
		intent := coordinator.EffectIntent{Payload: payload}
		for i := 0; i < 2; i++ {
			out, err := applyCoordinatorEffect(&coordinator.Task{}, j, r, intent)
			if err != nil || out.PR != 11 || out.Reference != "11" || out.HeadMismatch != mismatch {
				t.Fatalf("out=%+v err=%v", out, err)
			}
		}
		if c.creates != 1 || r.PRNumber != 11 || r.PRURL == "" {
			t.Fatalf("creates=%d report=%+v", c.creates, r)
		}
	}
}
func TestCommentIntentReconcilesStableMarker(t *testing.T) {
	c := &intentVCS{comments: map[string]bool{}}
	for _, kind := range []string{"issue-comment", "pr-comment"} {
		payload, _ := json.Marshal(jobEffect{Kind: kind, Repo: "o/r", Number: 1, Body: "hello", RunID: kind + "-stable"})
		for i := 0; i < 2; i++ {
			if _, err := applyCoordinatorEffect(&coordinator.Task{}, &runJob{client: c}, &coordinatorReport{}, coordinator.EffectIntent{Payload: payload}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if c.commentWrites != 2 {
		t.Fatalf("comments duplicated: %d", c.commentWrites)
	}
}

func TestCoordinatorDurableReportRecovery(t *testing.T) {
	db := storetest.New(t)
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
	c := &intentVCS{head: strings.Repeat("a", 40), comments: map[string]bool{}}
	j := &runJob{client: c}
	finalized := 0
	service := &coordinatorResults{WorkerStore: tasks, tasks: tasks, loadJob: func(context.Context, *coordinator.Task) (*runJob, error) { return j, nil }, apply: func(_ context.Context, _ *coordinator.Task, _ *runJob, r *coordinatorReport) error {
		if r.PRNumber != 11 || r.PRURL == "" || len(r.Effects) != 0 {
			t.Fatalf("recovered report: %+v", r)
		}
		finalized++
		return nil
	}}
	raw, _ := json.Marshal(coordinatorReport{jobResult: jobResult{Status: "success", HeadSHA: c.head, Effects: []jobEffect{{Kind: "pr-create", Repo: "o/r", Head: "fix", Base: "main", Title: "fix"}, {Kind: "issue-comment", Repo: "o/r", Number: 1, Body: "done"}}}})
	if err := service.Complete(ctx, task.ID, "w", task.FencingToken, raw); err != nil {
		t.Fatal(err)
	}
	if c.creates != 0 || c.commentWrites != 0 {
		t.Fatal("worker report performed remote I/O")
	}
	if err := service.Complete(ctx, task.ID, "w", task.FencingToken, raw); !errors.Is(err, coordinator.ErrStaleLease) {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := service.recoverResults(ctx, tasks); err != nil {
			t.Fatal(err)
		}
	}
	if c.creates != 1 || c.commentWrites != 1 || finalized != 1 {
		t.Fatalf("creates=%d comments=%d finalized=%d", c.creates, c.commentWrites, finalized)
	}
	current, _ := tasks.Task(ctx, task.ID)
	if current.State != "done" || current.PR != 11 {
		t.Fatalf("%+v", current)
	}
}

func TestCoordinatorPoisonedResultIsBoundedAndIsolated(t *testing.T) {
	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, store.Migrations); err != nil {
		t.Fatal(err)
	}
	tasks := coordinator.New(db.DB())
	if err := tasks.RegisterWorker(ctx, coordinator.Worker{ID: "w", MaxParallel: 2, RunUntagged: true}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 2} {
		if _, err := tasks.Enqueue(ctx, coordinator.Task{Repo: "o/r", Issue: n, Kind: "implement", Operator: "implement"}); err != nil {
			t.Fatal(err)
		}
		task, err := tasks.Claim(ctx, "w")
		if err != nil {
			t.Fatal(err)
		}
		if err := tasks.AcceptResult(ctx, task.ID, "w", task.FencingToken, json.RawMessage(`{"status":"success"}`)); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	// Recreate the result service on every pass, as during a process restart.
	for i := 0; i < 5; i++ {
		service := &coordinatorResults{tasks: tasks, loadJob: func(context.Context, *coordinator.Task) (*runJob, error) { return &runJob{}, nil }, apply: func(_ context.Context, task *coordinator.Task, _ *runJob, _ *coordinatorReport) error {
			if task.Issue == 1 {
				calls++
				return errors.New("poisoned result")
			}
			return nil
		}}
		if err := service.recoverResults(ctx, tasks); err != nil {
			t.Fatal(err)
		}
	}
	board, err := tasks.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(board.Tasks) != 2 {
		t.Fatalf("calls=%d board=%+v", calls, board)
	}
	for _, task := range board.Tasks {
		if task.Issue == 1 && (task.State != "failed" || task.ApplicationError != "poisoned result" || task.ApplicationAttempts != 3) {
			t.Fatalf("poison: %+v", task)
		}
		if task.Issue == 2 && task.State != "done" {
			t.Fatalf("healthy task: %+v", task)
		}
	}
}

func TestCoordinatorRecoversLegacyRepairWithoutReservation(t *testing.T) {
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	db := storetest.New(t)
	withRunLog(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, store.Migrations); err != nil {
		t.Fatal(err)
	}
	tasks := coordinator.New(db.DB())
	if err := tasks.RegisterWorker(ctx, coordinator.Worker{ID: "w", MaxParallel: 1, RunUntagged: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := tasks.Enqueue(ctx, coordinator.Task{Repo: "o/r", Issue: 1, PR: 11, Kind: "repair", Operator: "repair-ci"}); err != nil {
		t.Fatal(err)
	}
	task, err := tasks.Claim(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	if err := tasks.AcceptResult(ctx, task.ID, "w", task.FencingToken, json.RawMessage(`{"status":"failed","infra_kind":"launch"}`)); err != nil {
		t.Fatal(err)
	}
	j := &runJob{repo: "o/r", op: &operator.Operator{Name: "repair-ci"}, sub: &operator.Subject{Number: 1}, repair: &ciRepairContext{PR: vcs.PR{Number: 11, HeadSHA: strings.Repeat("a", 40)}, Attempt: 1}}
	for i := 0; i < 2; i++ {
		service := &coordinatorResults{tasks: tasks, loadJob: func(context.Context, *coordinator.Task) (*runJob, error) { return j, nil }, apply: applyCoordinatorReport}
		if err := service.recoverResults(ctx, tasks); err != nil {
			t.Fatal(err)
		}
	}
	current, err := tasks.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != "failed" || current.ApplicationAttempts != 0 {
		t.Fatalf("legacy result not recovered: %+v", current)
	}
	_, path := ciRepairLedgerPath(snapshot.DataDir(), "o/r", 11)
	ledger, err := loadCIRepairLedger(path)
	if err != nil || len(ledger.Attempts) != 1 || ledger.Attempts[0].Released != "launch" {
		t.Fatalf("legacy ledger: %+v %v", ledger, err)
	}
}

func TestCoordinatorResultPRRestoration(t *testing.T) {
	pr := vcs.PR{Number: 11, URL: "https://example.test/pull/11", HeadBranch: "fix/1"}
	current := coordinator.Task{Repo: "o/r", PR: 11}
	for _, client := range []vcs.Client{nil, &intentVCS{}} {
		// An empty intentVCS would panic on GetPR; the repair snapshot must win.
		got, err := coordinatorResultPR(current, &runJob{client: client, repair: &ciRepairContext{PR: pr}})
		if err != nil || got.Number != pr.Number || got.URL != pr.URL || got.HeadBranch != pr.HeadBranch {
			t.Fatalf("repair identity: %+v %v", got, err)
		}
	}
	client := &intentVCS{prs: []vcs.PR{pr}}
	if got, err := coordinatorResultPR(current, &runJob{client: client}); err != nil || got.Number != 11 {
		t.Fatalf("client fallback: %+v %v", got, err)
	}
	for _, j := range []*runJob{{}, {repair: &ciRepairContext{PR: vcs.PR{Number: 12}}}} {
		if _, err := coordinatorResultPR(current, j); err == nil {
			t.Fatal("missing PR identity must return an error")
		}
	}
}

func TestCoordinatorEffectWithoutClientReturnsError(t *testing.T) {
	payload, err := json.Marshal(jobEffect{Kind: "issue-comment", Repo: "o/r", Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := applyCoordinatorEffect(&coordinator.Task{}, &runJob{}, &coordinatorReport{}, coordinator.EffectIntent{Payload: payload}); err == nil {
		t.Fatal("missing client must return an error")
	}
}
