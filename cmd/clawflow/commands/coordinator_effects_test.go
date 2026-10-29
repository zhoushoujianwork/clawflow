package commands

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/zhoushoujianwork/clawflow/internal/retry"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestRateLimitedApplicationRetriesWithoutConsumingFailures(t *testing.T) {
	calls := 0
	err := retryRateLimited(context.Background(), func() error {
		calls++
		if calls == 1 {
			return fmt.Errorf("history: %w", &retry.HTTPError{Status: 403, RateLimited: true, After: time.Millisecond})
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	err = retryRateLimited(ctx, func() error { return &retry.HTTPError{Status: 429, RateLimited: true, After: time.Hour} })
	var limited *retry.HTTPError
	if !errors.As(err, &limited) || !limited.RateLimited {
		t.Fatalf("deferred error lost identity: %v", err)
	}
}

func TestCoordinatorRateLimitedResultRemainsPendingThenApplies(t *testing.T) {
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
	if _, err := tasks.Enqueue(ctx, coordinator.Task{Repo: "o/r", Issue: 1, Kind: "implement", Operator: "implement"}); err != nil {
		t.Fatal(err)
	}
	task, err := tasks.Claim(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	if err := tasks.AcceptResult(ctx, task.ID, "w", task.FencingToken, json.RawMessage(`{"status":"success"}`)); err != nil {
		t.Fatal(err)
	}

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()
	budget := github.Budget{Limit: 5000, Remaining: 0, Reset: time.Now().Add(10 * time.Minute)}
	client := resultBudgetClient(t, server.URL, budget)
	applied := 0
	service := &coordinatorResults{tasks: tasks, loadJob: func(ctx context.Context, _ *coordinator.Task) (*runJob, error) {
		client.SetRequestContext(ctx)
		_, err := client.ListIssueEvents("o/r", 1)
		return &runJob{client: client}, err
	}, apply: func(context.Context, *coordinator.Task, *runJob, *coordinatorReport) error { applied++; return nil }}

	if err := service.recoverResults(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("exhausted budget started %d HTTP requests", calls)
	}
	current, err := tasks.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.ApplicationAttempts != 0 || current.State == "failed" || current.State == "done" || applied != 0 {
		t.Fatalf("result lost during rate limit: %+v applied=%d", current, applied)
	}

	// Simulate recovery after reset from an expired persisted budget snapshot.
	budget.Reset = time.Now().Add(-time.Second)
	client = resultBudgetClient(t, server.URL, budget)
	if err := service.recoverResults(ctx, tasks); err != nil {
		t.Fatal(err)
	}
	current, err = tasks.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != "done" || current.ApplicationAttempts != 0 || calls != 1 || applied != 1 {
		t.Fatalf("result=%+v calls=%d applied=%d", current, calls, applied)
	}
}

// deferredResultStore fails the test if recovery spends an application attempt.
type deferredResultStore struct {
	coordinator.EffectStore
	failures int
}

func (s *deferredResultStore) PendingResults(context.Context) ([]coordinator.Task, error) {
	return []coordinator.Task{{ID: "limited", Result: json.RawMessage(`{"status":"success"}`)}}, nil
}
func (s *deferredResultStore) RecordApplicationFailure(context.Context, string, int64, string) error {
	s.failures++
	return nil
}

func TestCoordinatorExhaustedBudgetDoesNotConsumeAttempts(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%v", deadline), func(t *testing.T) {
			withRunLog(t)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("X-RateLimit-Limit", "5000")
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.Header().Set("X-RateLimit-Reset", fmt.Sprint(time.Now().Add(10*time.Minute).Unix()))
				fmt.Fprint(w, "[]")
			}))
			defer server.Close()
			client := github.New("fixture", server.URL)
			if _, err := client.ListIssueEvents("o/r", 1); err != nil {
				t.Fatal(err)
			}
			durable := &deferredResultStore{}
			service := &coordinatorResults{loadJob: func(ctx context.Context, _ *coordinator.Task) (*runJob, error) {
				if deadline {
					expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
					defer cancel()
					ctx = expired
				}
				client.SetRequestContext(ctx)
				_, err := client.ListIssueEvents("o/r", 1)
				return nil, err
			}}
			for i := 0; i < 4; i++ {
				if err := service.recoverResults(context.Background(), durable); err != nil {
					t.Fatal(err)
				}
			}
			if durable.failures != 0 || calls != 1 {
				t.Fatalf("failures=%d HTTP calls=%d", durable.failures, calls)
			}
		})
	}
}

// resultBudgetClient restores the same credential's tracked budget as a restarted
// applier would, with a separate private cache directory for each time snapshot.
func resultBudgetClient(t *testing.T, base string, budget github.Budget) *github.Client {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, fmt.Sprintf("%x", sha256.Sum256([]byte(base+"\x00fixture"))))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(budget)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "budget.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	client := github.New("fixture", base)
	client.UseCacheDir(root)
	return client
}
