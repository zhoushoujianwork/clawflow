package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestLocalWorkerLeasesAndCompletesOverHTTPS(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, store.Migrations); err != nil {
		t.Fatal(err)
	}
	tasks := coordinator.New(db.DB())
	cert, pin, err := coordinator.LoadCertificate(filepath.Join(snapshot.DataDir(), "coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := coordinator.NewServer(listener.Addr().String(), tasks, cert)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		if err := server.ServeTLS(listener, "", ""); err != nil && err != http.ErrServerClosed {
			t.Error(err)
		}
	}()
	defer func() { _ = server.Close(); <-serverDone }()
	task, err := tasks.Enqueue(ctx, coordinator.Task{Repo: "o/r", Issue: 7, Kind: "implement", Operator: "implement", Tags: []string{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	oldBuild, oldRun := localWorkerJob, runOneOperatorFn
	defer func() { localWorkerJob = oldBuild; runOneOperatorFn = oldRun }()
	localWorkerJob = func(context.Context, *coordinator.Task, *operator.Registry) (*runJob, time.Duration, error) {
		return &runJob{}, time.Minute, nil
	}
	runOneOperatorFn = func(_ context.Context, j *runJob, _ time.Duration) (bool, bool, bool) {
		j.hooks.AfterRun(&jobResult{Status: "success"})
		return true, false, false
	}
	stop, err := startLocalWorker(ctx, config.LocalWorkerSettings{Enabled: true, Tags: []string{"github"}}, listener.Addr(), pin, tasks, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		board, err := tasks.Board(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, got := range board.Tasks {
			if got.ID == task.ID && got.State == "done" {
				if got.Worker != "local-worker" {
					t.Fatalf("missing local worker lease: %+v", got)
				}
				if len(board.Workers) != 1 || !board.Workers[0].Live {
					t.Fatalf("worker not visible: %+v", board)
				}
				return
			}
		}
		select {
		case <-deadline:
			t.Fatal("local worker did not complete task")
		case <-tick.C:
		}
	}
}

func TestLocalWorkerDisabled(t *testing.T) {
	stop, err := startLocalWorker(context.Background(), config.LocalWorkerSettings{}, nil, "", nil, func(error) { t.Error("disabled worker started") })
	if err != nil {
		t.Fatal(err)
	}
	stop()
}

// The coordinator and worker use real TLS and PostgreSQL; only operator work
// and the external VCS/native reviewer are fixtures. Each result is recovered by
// a new applier to exercise the persisted protocol rather than an in-memory path.
func TestCoordinatorWorkerLifecycle(t *testing.T) {
	stubDispatchCoordination(t)
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	db := storetest.New(t)
	if _, err := db.Migrate(context.Background(), store.Migrations); err != nil {
		t.Fatal(err)
	}
	f, rc := acceptanceFixture(t)
	f.issue.Labels = []string{"agent-implemented"}
	eventsDir := writeAcceptanceDevelopment(t, "success", time.Now().Add(-2*time.Minute), nil)
	nativeEvents, err := os.ReadFile(filepath.Join(eventsDir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	client := &lifecycleVCS{acceptanceFake: f}
	acceptanceClientFactory = func(config.Repo) (vcs.Client, error) { return client, nil }
	tasks := coordinator.New(db.DB())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cert, pin, err := coordinator.LoadCertificate(filepath.Join(snapshot.DataDir(), "coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	load := func(_ context.Context, task *coordinator.Task) (*runJob, error) {
		var p coordinatorPayload
		if err := json.Unmarshal(task.Payload, &p); err != nil {
			return nil, err
		}
		j := &runJob{repo: task.Repo, repoCfg: rc, client: client, op: &operator.Operator{Name: task.Operator}, sub: &operator.Subject{Number: task.Issue}}
		j.repoCfg.AutoMerge = false
		if p.Repair != nil {
			r := p.Repair
			j.repair = &ciRepairContext{PR: f.pr, Attempt: r.Attempt, Acceptance: r.Acceptance, MergeConflict: r.Reason == "merge_conflict"}
		}
		return j, nil
	}
	service := func() *coordinatorResults {
		return &coordinatorResults{WorkerStore: tasks, tasks: tasks, loadJob: load, apply: applyCoordinatorReport}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := coordinator.NewServer(listener.Addr().String(), service(), cert)
	go server.ServeTLS(listener, "", "")
	defer server.Close()
	oldBuild, oldRun := localWorkerJob, runOneOperatorFn
	defer func() { localWorkerJob, runOneOperatorFn = oldBuild, oldRun }()
	localWorkerJob = func(ctx context.Context, task *coordinator.Task, _ *operator.Registry) (*runJob, time.Duration, error) {
		j, err := load(ctx, task)
		return j, time.Minute, err
	}
	executions := 0
	runOneOperatorFn = func(_ context.Context, j *runJob, _ time.Duration) (bool, bool, bool) {
		executions++
		result := &jobResult{Status: "success", HeadSHA: f.pr.HeadSHA, PRNumber: 11, DevelopmentEvents: string(nativeEvents)}
		if j.repair == nil {
			result.Effects = []jobEffect{{Kind: "pr-create", Repo: "o/r", Head: f.pr.HeadBranch, Base: "main", Title: "Implementation", Body: "Fixes #42"}}
		}
		j.hooks.AfterRun(result)
		return true, false, false
	}
	stop, err := startLocalWorker(ctx, config.LocalWorkerSettings{Enabled: true}, listener.Addr(), pin, tasks, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	wait := func(id string) {
		t.Helper()
		for ctx.Err() == nil {
			if err := service().recoverResults(ctx, tasks); err != nil {
				t.Fatal(err)
			}
			task, err := tasks.Task(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if task.State == "done" {
				return
			}
			if task.State == "failed" {
				t.Fatalf("task failed: %+v", task)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("worker lifecycle timed out")
	}
	enqueue := func(j *runJob) string {
		t.Helper()
		if _, _, err := dispatchCoordinatorJobs(ctx, tasks, []*runJob{j}, time.Minute); err != nil {
			t.Fatal(err)
		}
		active, err := tasks.Active(ctx)
		if err != nil || len(active) != 1 {
			t.Fatalf("active: %+v %v", active, err)
		}
		return active[0].ID
	}
	wait(enqueue(&runJob{repo: "o/r", repoCfg: rc, client: client, op: &operator.Operator{Name: "implement"}, sub: &operator.Subject{Number: 42, Title: "Feature", Body: "Implement the feature."}}))
	if client.creates != 1 {
		t.Fatalf("PR creates: %d", client.creates)
	}
	for _, reason := range []string{"ci", "acceptance", "merge_conflict"} {
		// Advance only the fixture ledger's clock; production cooldown remains intact.
		dir, path := ciRepairLedgerPath(snapshot.DataDir(), "o/r", 11)
		ledger, err := loadCIRepairLedger(path)
		if err != nil {
			t.Fatal(err)
		}
		for i := range ledger.Attempts {
			ledger.Attempts[i].At = time.Now().Add(-time.Hour)
		}
		if len(ledger.Attempts) > 0 {
			if err := saveCIRepairLedger(dir, path, ledger); err != nil {
				t.Fatal(err)
			}
		}
		repair := &ciRepairContext{PR: f.pr, MergeConflict: reason == "merge_conflict"}
		f.ciStatus = vcs.CIStatusFailure
		if reason == "acceptance" {
			f.ciStatus = vcs.CIStatusSuccess
			setReviewerVerdict("failed")
			record := recordVerdict(t, f)
			repair.Acceptance = findingsFromRecord(record)
		}
		if repair.MergeConflict {
			f.mergeability = map[int]vcs.MergeStatus{11: vcs.MergeStatusConflict}
		}
		id := enqueue(&runJob{repo: "o/r", repoCfg: rc, client: client, op: &operator.Operator{Name: "repair-ci"}, sub: &operator.Subject{Number: 42, Title: "Feature", Body: "Implement the feature."}, repair: repair})
		_, path = ciRepairLedgerPath(snapshot.DataDir(), "o/r", 11)
		ledger, _ = loadCIRepairLedger(path)
		if len(ledger.Attempts) == 0 || ledger.Attempts[len(ledger.Attempts)-1].Reason != reason {
			t.Fatalf("missing enqueue reservation: %+v", ledger)
		}
		wait(id)
		ledger, _ = loadCIRepairLedger(path)
		if !ledger.Attempts[len(ledger.Attempts)-1].Completed {
			t.Fatalf("reservation not closed: %+v", ledger)
		}
	}
	stop()
	f.ciStatus = vcs.CIStatusSuccess
	f.mergeability = nil
	setReviewerVerdict("passed")
	contract, _, _, _, err := acceptanceContract(ctx, client, "o/r", 11)
	if err != nil {
		t.Fatal(err)
	}
	task, payload := coordinatorTask(&runJob{repo: "o/r", repoCfg: rc, op: &operator.Operator{Name: "acceptance"}, sub: &operator.Subject{Number: 42}}, time.Minute)
	task.PR = 11
	payload.Acceptance, payload.AcceptanceWorker, payload.DevelopmentWorker = &contract, "different", "local-worker"
	review, err := tasks.EnqueuePrepared(ctx, task, func(task *coordinator.Task) error {
		payload.TaskID = task.ID
		task.Payload, err = json.Marshal(payload)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	// Independence requires a second worker identity for acceptance.
	secret, err := tasks.CreateToken(ctx, "reviewer", "worker")
	if err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	opts := workerOptions{Coordinator: "https://" + listener.Addr().String(), Fingerprint: pin, TokenFile: token, MaxParallel: 1, RunUntagged: true, Roles: []string{"acceptance"}}
	api, err := newWorkerAPI(opts)
	if err != nil {
		t.Fatal(err)
	}
	reviewCtx, stopReview := context.WithCancel(ctx)
	reviewDone := make(chan error, 1)
	go func() { reviewDone <- runWorker(reviewCtx, api, opts, time.Second, 10*time.Millisecond, nil) }()
	defer func() { stopReview(); <-reviewDone; api.client.CloseIdleConnections() }()
	wait(review.ID)
	if _, _, err := currentAcceptance(ctx, client, "o/r", 11); err != nil {
		t.Fatal(err)
	}
	result := attemptAutoMergeResult(autoMergeAttempt{ctx: ctx, client: client, repo: "o/r", repoCfg: rc, prNum: 11, issueNum: 42, ciPoll: true, quiet: true})
	if !result.Merged || len(f.expected) != 1 || executions != 4 {
		t.Fatalf("merged=%v expected=%v executions=%d", result.Merged, f.expected, executions)
	}
}

type lifecycleVCS struct {
	*acceptanceFake
	creates int
}

func (f *lifecycleVCS) ListPRs(string, string) ([]vcs.PR, error) {
	if f.creates == 0 {
		return nil, nil
	}
	return []vcs.PR{f.pr}, nil
}
func (f *lifecycleVCS) CreatePR(string, vcs.PRCreateOpts) (vcs.PR, error) {
	f.creates++
	return f.pr, nil
}
func (f *lifecycleVCS) PostIssueCommentIdempotent(string, int, string, string) error { return nil }
func (f *lifecycleVCS) PostPRComment(string, int, string) error                      { return nil }

func TestWorkerSurvivesCoordinatorRestartDuringLease(t *testing.T) {
	db := storetest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.Migrate(ctx, store.Migrations); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	tasks := coordinator.New(db.DB())
	secret, err := tasks.CreateToken(ctx, "restart-worker", "worker")
	if err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	cert, pin, err := coordinator.LoadCertificate(filepath.Join(snapshot.DataDir(), "coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	server := coordinator.NewServer(addr, tasks, cert)
	go server.ServeTLS(listener, "", "")
	defer func() { server.Close() }()
	for _, n := range []int{1, 2} {
		if _, err := tasks.Enqueue(ctx, coordinator.Task{Repo: "o/r", Issue: n, Kind: "implement", Operator: "implement"}); err != nil {
			t.Fatal(err)
		}
	}
	started, release := make(chan struct{}), make(chan struct{})
	oldRun := runOneOperatorFn
	defer func() { runOneOperatorFn = oldRun }()
	var executed atomic.Int32
	runOneOperatorFn = func(ctx context.Context, j *runJob, _ time.Duration) (bool, bool, bool) {
		if executed.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return false, false, true
			}
		}
		j.hooks.AfterRun(&jobResult{Status: "success"})
		return true, false, false
	}
	opts := workerOptions{Coordinator: "https://" + addr, TokenFile: token, Fingerprint: pin, MaxParallel: 1, RunUntagged: true, Roles: []string{"operator"}}
	api, err := newWorkerAPI(opts)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- runWorker(ctx, api, opts, 10*time.Millisecond, time.Millisecond, func(context.Context, *coordinator.Task) (*runJob, time.Duration, error) {
			return &runJob{}, time.Minute, nil
		})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
		api.client.CloseIdleConnections()
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("worker did not start")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	listener, err = net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	server = coordinator.NewServer(addr, tasks, cert)
	go server.ServeTLS(listener, "", "")
	close(release)
	for ctx.Err() == nil {
		board, err := tasks.Board(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if board.Throughput.Done == 2 {
			if executed.Load() != 2 {
				t.Fatalf("duplicate execution: %d", executed.Load())
			}
			for _, task := range board.Tasks {
				if task.Attempt != 0 {
					t.Fatalf("lease was lost: %+v", task)
				}
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker did not reconnect and finish both tasks")
}
