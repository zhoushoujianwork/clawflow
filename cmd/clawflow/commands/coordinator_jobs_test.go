package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/zhoushoujianwork/clawflow/internal/api"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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

type resultStoreFake struct {
	coordinator.WorkerStore
	mu   sync.Mutex
	task coordinator.Task
}

func (s *resultStoreFake) VerifyToken(context.Context, string) (*coordinator.Token, error) {
	return &coordinator.Token{Name: "w"}, nil
}
func (s *resultStoreFake) AppendAudit(context.Context, string, string, string) error { return nil }
func (s *resultStoreFake) Task(context.Context, string) (*coordinator.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	copy := s.task
	return &copy, nil
}
func (s *resultStoreFake) AcceptResult(_ context.Context, id, w string, f int64, r json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.task.ID || w != s.task.LeaseOwner || f != s.task.FencingToken || s.task.LeaseExpiresAt == nil || s.task.State != "leased" || !s.task.LeaseExpiresAt.After(time.Now()) {
		return coordinator.ErrStaleLease
	}
	s.task.Result = r
	s.task.LeaseExpiresAt = nil
	return nil
}
func (s *resultStoreFake) FinishResult(_ context.Context, id, w string, f int64, failed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.task.State = "done"
	if failed {
		s.task.State = "failed"
	}
	return nil
}
func (s *resultStoreFake) ExpireLeases(context.Context, time.Time) ([]coordinator.Task, error) {
	return nil, nil
}
func (s *resultStoreFake) EnqueuePrepared(_ context.Context, t coordinator.Task, fn func(*coordinator.Task) error) (*coordinator.Task, error) {
	if s.task.ID != "" {
		return &s.task, nil
	}
	t.ID = "task"
	t.CreatedAt = time.Now().UTC()
	t.State = "pending"
	if err := fn(&t); err != nil {
		return nil, err
	}
	s.task = t
	return &s.task, nil
}

type resultClientFake struct {
	vcs.Client
	calls []string
	fail  string
}

func (c *resultClientFake) record(s string) error {
	c.calls = append(c.calls, s)
	if s == c.fail {
		return errors.New("VCS unavailable")
	}
	return nil
}
func (c *resultClientFake) PostIssueComment(string, int, string) error { return c.record("comment") }
func (c *resultClientFake) PostIssueCommentIdempotent(string, int, string, string) error {
	return c.record("idempotent-comment")
}
func (c *resultClientFake) PostPRComment(string, int, string) error { return c.record("pr-comment") }
func (c *resultClientFake) AddLabel(_ string, _ int, l ...string) error {
	return c.record("add:" + strings.Join(l, ","))
}
func (c *resultClientFake) RemoveLabel(string, int, ...string) error { return c.record("remove") }
func (c *resultClientFake) CloseIssue(string, int) error             { return c.record("close") }

func resultFixture(t *testing.T) (*resultStoreFake, *runJob, *resultClientFake, *coordinatorResults) {
	t.Helper()
	withRunLog(t)
	expires := time.Now().Add(time.Minute)
	s := &resultStoreFake{task: coordinator.Task{ID: "task", Repo: "o/r", Issue: 7, Operator: "reply-comment", State: "leased", LeaseOwner: "w", FencingToken: 3, LeaseExpiresAt: &expires, CreatedAt: time.Now().UTC()}}
	c := &resultClientFake{}
	j := &runJob{repo: "o/r", op: &operator.Operator{Name: "reply-comment"}, sub: &operator.Subject{Number: 7}, client: c}
	svc := &coordinatorResults{WorkerStore: s, tasks: s, loadJob: func(context.Context, *coordinator.Task) (*runJob, error) { return j, nil }, apply: applyCoordinatorReport}
	return s, j, c, svc
}

func TestCoordinatorPayloadAndDispatch(t *testing.T) {
	withRunLog(t)
	j := &runJob{repo: "o/r", op: &operator.Operator{Name: "implement", Prompt: "instructions", Source: "/private/path"}, sub: &operator.Subject{Number: 7, Labels: []string{"clawflow", "worker:heavy", "priority:high"}}, language: "zh", repoCfg: config.Repo{BaseBranch: "main", TestCommand: "go test ./...", AcceptanceSetupCommand: "make assets", CIRequired: true, WorkerTags: []string{"codex"}, Priority: 2}}
	s := &resultStoreFake{}
	old := runOneOperatorFn
	runOneOperatorFn = func(context.Context, *runJob, time.Duration) (bool, bool, bool) {
		t.Fatal("executed locally")
		return false, false, false
	}
	t.Cleanup(func() { runOneOperatorFn = old })
	harvested := 0
	jobs := []*runJob{{harvest: func(context.Context) []firedKey { harvested++; return nil }}, j}
	for i := 0; i < 2; i++ {
		f, failed, err := dispatchCoordinatorJobs(context.Background(), s, jobs, time.Minute)
		if err != nil || len(f) != 1 || len(failed) != 0 {
			t.Fatal(f, failed, err)
		}
	}
	if harvested != 2 || s.task.Kind != "implement" || s.task.Priority != 5 {
		t.Fatal(s.task, harvested)
	}
	var p coordinatorPayload
	if err := json.Unmarshal(s.task.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.TaskID != "task" || p.Repo != "o/r" || p.Issue != 7 || p.Role != "operator" || p.Language != "zh" || p.TimeoutSec != 60 || p.Platform != "github" || p.RepoPolicy.BaseBranch != "main" || !p.RepoPolicy.CIRequired || p.RepoPolicy.TestCommand != "go test ./..." || p.RepoPolicy.AcceptanceSetupCommand != "make assets" || p.Scan.ObservedAt.IsZero() {
		t.Fatalf("payload: %+v", p)
	}
	if !reflect.DeepEqual(p.Tags, []string{"codex", "heavy"}) || len(p.SkillDigest) != 64 {
		t.Fatal(p)
	}
	copy := *j.op
	copy.Source = "/other/path"
	if skillDigest(&copy) != p.SkillDigest {
		t.Fatal("machine-local digest")
	}
	copy.Prompt = "changed"
	if skillDigest(&copy) == p.SkillDigest {
		t.Fatal("digest ignores prompt")
	}
	j.reopen = &reopenContext{Event: vcs.IssueEvent{ID: "reopened"}}
	j.repair = &ciRepairContext{PR: vcs.PR{Number: 8, HeadSHA: "sha", HeadBranch: "fix", HeadRepo: "o/r"}, Attempt: 2, Acceptance: &acceptanceFindings{AttemptID: "a", Verdict: "failed", ContractDigest: "digest"}}
	_, p = coordinatorTask(j, time.Minute)
	if p.ReopenEventID != "reopened" || p.Kind != "repair" || p.Repair.Attempt != 2 || p.Repair.Acceptance.AttemptID != "a" || p.Repair.Reason != repairReasonAcceptance {
		t.Fatal(p)
	}
	j.repair = nil
	j.op.Name = "reply-comment"
	_, p = coordinatorTask(j, time.Minute)
	if p.Kind != "reply" || p.Role != "light" {
		t.Fatal(p)
	}
}

func TestCoordinatorHTTPFencedOrderedResults(t *testing.T) {
	s, _, c, svc := resultFixture(t)
	server := httptest.NewServer(coordinator.NewHandler(svc))
	defer server.Close()
	report := coordinatorReport{jobResult: jobResult{Status: "success", Usage: &snapshot.Usage{ModelUsage: map[string]snapshot.ModelUsage{"model": {InputTokens: 3}}}, Effects: []jobEffect{
		{Kind: "issue-comment", Repo: "o/r", Number: 7, Body: "hello"},
		{Kind: "add-label", Repo: "o/r", Number: 7, Labels: []string{"done"}},
		{Kind: "remove-label", Repo: "o/r", Number: 7, Labels: []string{"pending"}},
		{Kind: "close-issue", Repo: "o/r", Number: 7},
	}}}
	for i := 0; i < 130; i++ {
		report.Events = append(report.Events, json.RawMessage(fmt.Sprintf(`{"n":%d}`, i)))
	}
	send := func(fence int64) int {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"fencing_token": fence, "result": report})
		req, _ := http.NewRequest("POST", server.URL+"/worker/v1/tasks/task/complete", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer valid")
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := send(2); got != 409 || len(c.calls) != 0 {
		t.Fatal(got, c.calls)
	}
	if got := send(3); got != 200 {
		t.Fatal(got)
	}
	if got := send(3); got != 409 {
		t.Fatal(got)
	}
	if !reflect.DeepEqual(c.calls, []string{"comment", "add:done", "remove", "close"}) {
		t.Fatal(c.calls)
	}
	var stored coordinatorReport
	if err := json.Unmarshal(s.task.Result, &stored); err != nil {
		t.Fatal(err)
	}
	if s.task.State != "done" || len(stored.Events) != 100 || string(stored.Events[0]) != `{"n":30}` {
		t.Fatal(s.task.State, len(stored.Events))
	}
}

func TestCoordinatorResultFailureDoesNotReplay(t *testing.T) {
	s, _, c, svc := resultFixture(t)
	c.fail = "add:done"
	r := coordinatorReport{jobResult: jobResult{Status: "success", Effects: []jobEffect{{Kind: "issue-comment", Repo: "o/r", Number: 7}, {Kind: "add-label", Repo: "o/r", Number: 7, Labels: []string{"done"}}, {Kind: "close-issue", Repo: "o/r", Number: 7}}}}
	raw, _ := json.Marshal(r)
	if svc.Complete(context.Background(), "task", "w", 3, raw) == nil {
		t.Fatal("expected remote failure")
	}
	if !errors.Is(svc.Complete(context.Background(), "task", "w", 3, raw), coordinator.ErrStaleLease) {
		t.Fatal("replayed result")
	}
	if !reflect.DeepEqual(c.calls, []string{"comment", "add:done"}) || s.task.State != "leased" || s.task.LeaseExpiresAt != nil || len(s.task.Result) == 0 {
		t.Fatal(c.calls, s.task)
	}
}

func TestCoordinatorConcurrentResultOnce(t *testing.T) {
	_, _, c, svc := resultFixture(t)
	raw := json.RawMessage(`{"status":"success","effects":[{"kind":"issue-comment","repo":"o/r","number":7}]}`)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := svc.Complete(context.Background(), "task", "w", 3, raw)
			if err != nil && !errors.Is(err, coordinator.ErrStaleLease) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if !reflect.DeepEqual(c.calls, []string{"comment"}) {
		t.Fatal(c.calls)
	}
}

func TestCoordinatorAccounting(t *testing.T) {
	for _, status := range []string{"failed", "disallowed-outcome", "skipped-empty", "output-limit", "lease-lost", "auth-error"} {
		t.Run(status, func(t *testing.T) {
			s, j, c, _ := resultFixture(t)
			count := 0
			for i := 0; i < 3; i++ {
				task := s.task
				task.ID = fmt.Sprintf("task-%d", i)
				task.CreatedAt = task.CreatedAt.Add(time.Duration(i) * time.Second)
				r := &coordinatorReport{jobResult: jobResult{Status: status, Usage: &snapshot.Usage{TotalCostUSD: 0.25}}}
				if err := applyCoordinatorReport(context.Background(), &task, j, r); err != nil {
					t.Fatal(err)
				}
			}
			for _, call := range c.calls {
				if call == "add:agent-failed" {
					count++
				}
			}
			want := 1
			if status == "lease-lost" || status == "auth-error" {
				want = 0
			}
			if count != want {
				t.Fatal(status, c.calls)
			}
			runs := snapshot.ReadRunsIndex()
			if len(runs) != 3 || runs[0].Usage.TotalCostUSD != 0.25 {
				t.Fatal(runs)
			}
		})
	}
}

func TestCoordinatorRepairReleaseAndMerge(t *testing.T) {
	s, j, _, _ := resultFixture(t)
	pr := vcs.PR{Number: 8, HeadSHA: strings.Repeat("a", 40), HeadRepo: "o/r", HeadBranch: "fix/issue-7", Body: "Fixes #7"}
	attempt, err := reserveRepair(snapshot.DataDir(), "o/r", pr, repairReasonCI, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	j.repair = &ciRepairContext{PR: pr, Attempt: attempt}
	if err := applyCoordinatorReport(context.Background(), &s.task, j, &coordinatorReport{jobResult: jobResult{Status: "failed", InfraKind: "launch"}}); err != nil {
		t.Fatal(err)
	}
	_, path := ciRepairLedgerPath(snapshot.DataDir(), "o/r", 8)
	ledger, err := loadCIRepairLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	consumed, infra := ledger.counts()
	if consumed != 0 || infra != 1 || snapshot.ConsecutiveFailures("o/r", 7) != 0 {
		t.Fatal(consumed, infra)
	}
	j.repair = nil
	j.op.Name = "implement"
	j.repoCfg = config.Repo{AutoMerge: true, BaseBranch: "main"}
	f := &sweepFake{openPRs: []vcs.PR{pr}}
	j.client = f
	s.task.ID = "merge"
	if err := applyCoordinatorReport(context.Background(), &s.task, j, &coordinatorReport{jobResult: jobResult{Status: "success", Outcome: "agent-implemented", Output: "https://github.com/o/r/pull/8", PRNumber: 999}}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.merged, []int{8}) {
		t.Fatal(f.merged)
	}
}

func TestCoordinatorRepairEnqueueReservesOnce(t *testing.T) {
	withRunLog(t)
	pr, issue := repairFixture()
	j := &runJob{repo: "o/r", repoCfg: config.Repo{AutoMerge: true, CIRequired: true}, client: &repairClient{pr: pr, issue: issue, status: vcs.CIStatusFailure}, op: &operator.Operator{Name: "repair-ci"}, sub: &operator.Subject{Number: issue.Number}, repair: &ciRepairContext{PR: pr}}
	s := &resultStoreFake{}
	for i := 0; i < 2; i++ {
		if _, _, err := dispatchCoordinatorJobs(context.Background(), s, []*runJob{j}, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	var p coordinatorPayload
	if err := json.Unmarshal(s.task.Payload, &p); err != nil {
		t.Fatal(err)
	}
	_, path := ciRepairLedgerPath(snapshot.DataDir(), j.repo, pr.Number)
	ledger, err := loadCIRepairLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Repair.Attempt != 1 || len(ledger.Attempts) != 1 {
		t.Fatal(p, ledger)
	}
}

func TestCoordinatorSchedulingIgnoresLocalExecutionRequirements(t *testing.T) {
	oldGate := coordinatorPRAllowedFn
	coordinatorPRAllowedFn = func(context.Context, string, int) error { return nil }
	t.Cleanup(func() { coordinatorPRAllowedFn = oldGate })
	cfg := config.Repo{CoordinatorMode: true, BoundMachine: "worker", AutoMerge: true, CIRequired: true}
	settings := config.Settings{RequireBinding: true, Coordinator: config.CoordinatorSettings{Enabled: true}}
	if !repoRunsHere(settings, cfg, "coordinator") || deterministicSkip(&operator.Operator{Name: "implement"}, cfg) {
		t.Fatal("local execution requirements block coordinator")
	}
	withRunLog(t)
	pr, issue := repairFixture()
	jobs := scanCIRepairJobs(context.Background(), repairRegistry(t), &repairClient{pr: pr, issue: issue, status: vcs.CIStatusFailure}, "o/r", cfg, 0, nil)
	if len(jobs) != 1 {
		t.Fatal("repair requires coordinator clone or worker hostname")
	}
	cfg.CoordinatorActive = map[int]bool{7: true}
	plan := planRepoQueue(repoQueueInput{Repo: "o/r", Issues: []vcs.Issue{{Number: 7, State: "open", Labels: []string{"clawflow"}}, {Number: 8, State: "open", Labels: []string{"clawflow"}}}, ImplementState: map[int]string{7: "", 8: ""}, ImplementJobs: map[int]bool{8: true}, OpenPRs: func() ([]vcs.PR, error) { return nil, nil }, Locked: func(n int) bool { return cfg.CoordinatorActive[n] }, MaxInFlight: 1, Now: time.Now()})
	if len(plan.Picks) != 0 {
		t.Fatal("active coordinator task did not occupy queue slot")
	}
}

func TestCoordinatorRejectsExpiredAndCrossRepoResults(t *testing.T) {
	s, _, c, svc := resultFixture(t)
	raw := json.RawMessage(`{"status":"success","effects":[{"kind":"close-issue","repo":"other/repo","number":7}]}`)
	if !errors.Is(svc.Complete(context.Background(), "task", "w", 3, raw), coordinator.ErrInvalid) {
		t.Fatal("cross-repository effect accepted")
	}
	expired := time.Now().Add(-time.Second)
	s.task.LeaseExpiresAt = &expired
	if !errors.Is(svc.Complete(context.Background(), "task", "w", 3, json.RawMessage(`{"status":"success"}`)), coordinator.ErrStaleLease) || len(c.calls) != 0 {
		t.Fatal("expired result applied")
	}
}

func TestCoordinatorRunOnceEnqueuesAndPreservesCapacity(t *testing.T) {
	db, dsn := storetest.NewWithDSN(t)
	if _, err := db.Migrate(context.Background(), store.Migrations); err != nil {
		t.Fatal(err)
	}
	withRunLog(t)
	queueConfig(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Settings.Coordinator.Enabled = true
	rc := cfg.Repos["o/r"]
	rc.LocalPath = ""
	rc.MaxInFlight = 1
	rc.WorkerTags = []string{"codex"}
	cfg.Repos["o/r"] = rc
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	queueVCS(t, []map[string]any{targetedIssue(7, "clawflow"), targetedIssue(8, "clawflow")}, nil, nil, nil)
	oldStore, oldRun := requireStore, runOneOperatorFn
	requireStore = func(ctx context.Context) (*store.Store, error) { return store.Open(ctx, dsn) }
	runOneOperatorFn = func(context.Context, *runJob, time.Duration) (bool, bool, bool) {
		t.Error("local executor called in coordinator mode")
		return false, false, true
	}
	t.Cleanup(func() { requireStore = oldStore; runOneOperatorFn = oldRun })
	for i := 0; i < 2; i++ {
		if err := runOnce(context.Background(), "o/r", 0, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	tasks, err := coordinator.New(db.DB()).Active(context.Background())
	if err != nil || len(tasks) != 1 {
		t.Fatal(tasks, err)
	}
	var payload coordinatorPayload
	if err := json.Unmarshal(tasks[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.TaskID != tasks[0].ID || payload.Kind != "implement" {
		t.Fatal(payload)
	}
}

func TestCoordinatorRepairScanExcludesActiveIssue(t *testing.T) {
	oldGate := coordinatorPRAllowedFn
	coordinatorPRAllowedFn = func(context.Context, string, int) error { return nil }
	t.Cleanup(func() { coordinatorPRAllowedFn = oldGate })
	withRunLog(t)
	pr, issue := repairFixture()
	reg := repairRegistry(t)
	for _, onlyIssue := range []int{0, issue.Number} {
		for _, tc := range []struct {
			name   string
			active map[int]bool
			want   int
		}{
			{name: "no active tasks", want: 1},
			{name: "same issue active", active: map[int]bool{issue.Number: true}, want: 0},
			{name: "other issue active", active: map[int]bool{issue.Number + 1: true}, want: 1},
		} {
			t.Run(fmt.Sprintf("target-%d/%s", onlyIssue, tc.name), func(t *testing.T) {
				cfg := config.Repo{CoordinatorMode: true, CoordinatorActive: tc.active, AutoMerge: true, CIRequired: true}
				client := &repairClient{pr: pr, issue: issue, status: vcs.CIStatusFailure}
				// Normal scanning omits the active issue, so it is absent from normal jobs.
				jobs := scanCIRepairJobs(context.Background(), reg, client, "o/r", cfg, onlyIssue, nil)
				if len(jobs) != tc.want {
					t.Fatalf("repair jobs = %d, want %d (active=%v)", len(jobs), tc.want, tc.active)
				}
			})
		}
	}
}

func TestCoordinatorDispatchContinuesAfterRepairEligibilityChange(t *testing.T) {
	for _, change := range []string{"head changed", "CI recovered"} {
		t.Run(change, func(t *testing.T) {
			withRunLog(t)
			pr, issue := repairFixture()
			client := &repairClient{pr: pr, issue: issue, status: vcs.CIStatusFailure}
			if change == "head changed" {
				client.pr.HeadSHA = strings.Repeat("b", 40)
			} else {
				client.status = vcs.CIStatusSuccess
			}
			repair := &runJob{repo: "o/r", repoCfg: config.Repo{AutoMerge: true, CIRequired: true}, client: client,
				op: &operator.Operator{Name: "repair-ci"}, sub: &operator.Subject{Number: issue.Number}, repair: &ciRepairContext{PR: pr}}
			reply := &runJob{repo: "o/r", op: &operator.Operator{Name: "reply-comment"}, sub: &operator.Subject{Number: issue.Number + 1}}
			harvested := false
			jobs := []*runJob{repair, reply, {harvest: func(context.Context) []firedKey { harvested = true; return nil }}}
			s := &resultStoreFake{}
			fired, failed, err := dispatchCoordinatorJobs(context.Background(), s, jobs, time.Minute)
			if err != nil {
				t.Fatalf("repair eligibility change aborted dispatch: %v", err)
			}
			want := []firedKey{{Repo: "o/r", IssueNumber: reply.sub.Number, Operator: "reply-comment"}}
			if !reflect.DeepEqual(fired, want) || len(failed) != 0 || s.task.Issue != reply.sub.Number || s.task.Kind != "reply" || !harvested {
				t.Fatalf("fired=%v failed=%v task=%+v harvested=%v", fired, failed, s.task, harvested)
			}
			_, path := ciRepairLedgerPath(snapshot.DataDir(), repair.repo, pr.Number)
			ledger, err := loadCIRepairLedger(path)
			if err != nil || len(ledger.Attempts) != 0 {
				t.Fatalf("ineligible repair reserved an attempt: %+v, %v", ledger, err)
			}
		})
	}
}

type coordinatorEnqueueFailure struct {
	resultStoreFake
	err error
}

func (s *coordinatorEnqueueFailure) EnqueuePrepared(context.Context, coordinator.Task, func(*coordinator.Task) error) (*coordinator.Task, error) {
	return nil, s.err
}

func TestCoordinatorDispatchPreservesFatalErrors(t *testing.T) {
	withRunLog(t)
	pr, issue := repairFixture()
	job := &runJob{repo: "o/r", repoCfg: config.Repo{AutoMerge: true, CIRequired: true},
		client: &repairClient{pr: pr, issue: issue, status: vcs.CIStatusFailure},
		op:     &operator.Operator{Name: "repair-ci"}, sub: &operator.Subject{Number: issue.Number}, repair: &ciRepairContext{PR: pr}}
	storeErr := errors.New("store unavailable")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		store coordinatorQueue
		want  error
	}{
		{"store failure", context.Background(), &coordinatorEnqueueFailure{err: storeErr}, storeErr},
		{"pass cancelled", cancelled, &resultStoreFake{}, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			harvested := false
			jobs := []*runJob{job, {harvest: func(context.Context) []firedKey { harvested = true; return nil }}}
			fired, failed, err := dispatchCoordinatorJobs(tc.ctx, tc.store, jobs, time.Minute)
			if !errors.Is(err, tc.want) || len(fired) != 0 || len(failed) != 0 || harvested {
				t.Fatalf("fired=%v failed=%v err=%v harvested=%v", fired, failed, err, harvested)
			}
		})
	}
}

func TestCoordinatorHostAffinitySurvivesKindOverride(t *testing.T) {
	j := &runJob{repo: "o/r", op: &operator.Operator{Name: "implement"}, sub: &operator.Subject{Number: 7}, repoCfg: config.Repo{
		WorkerTags: []string{"codex", "host-work-mac"}, WorkerTagsByKind: map[string][]string{"implement": {"heavy"}},
	}}
	task, payload := coordinatorTask(j, time.Minute)
	want := []string{"heavy", "host-work-mac"}
	if !reflect.DeepEqual(task.Tags, want) || !reflect.DeepEqual(payload.Tags, want) {
		t.Fatalf("lost host affinity: %+v / %+v", task.Tags, payload.Tags)
	}
}

func TestCoordinatorPlatformDoesNotAddRoutingTags(t *testing.T) {
	for _, rc := range []config.Repo{
		{},
		{Platform: "gitlab"},
		{Platform: "github", BaseURL: "https://github.example/api/v3"},
		{Platform: "gitlab", BaseURL: "https://gitlab.example"},
	} {
		j := &runJob{repo: "o/r", op: &operator.Operator{Name: "implement"}, sub: &operator.Subject{Number: 7}, repoCfg: rc}
		task, payload := coordinatorTask(j, time.Minute)
		if len(task.Tags) != 0 || len(payload.Tags) != 0 {
			t.Fatalf("platform metadata added routing tags: %+v / %v / %v", rc, task.Tags, payload.Tags)
		}
		j.repoCfg.WorkerTags = []string{"github", "creds-laptop"}
		task, payload = coordinatorTask(j, time.Minute)
		want := []string{"creds-laptop", "github"}
		if !reflect.DeepEqual(task.Tags, want) || !reflect.DeepEqual(payload.Tags, want) {
			t.Fatalf("explicit routing tags lost: %v / %v", task.Tags, payload.Tags)
		}
	}
}

type dependencyClient struct {
	vcs.Client
	state string
}

func (c *dependencyClient) GetIssue(_ string, number int) (*vcs.Issue, error) {
	if number == 5 {
		return &vcs.Issue{State: "closed"}, nil
	}
	return &vcs.Issue{State: c.state}, nil
}
func TestCoordinatorEnqueueHonorsDependsOn(t *testing.T) {
	withRunLog(t)
	for _, state := range []string{"open", "closed"} {
		s := &resultStoreFake{}
		j := &runJob{repo: "o/r", op: &operator.Operator{Name: "implement"}, sub: &operator.Subject{Number: 7, Body: "Depends on: #5\nDepends on: #6"}, client: &dependencyClient{state: state}}
		fired, _, err := dispatchCoordinatorJobs(context.Background(), s, []*runJob{j}, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if (len(fired) == 1) != (state == "closed") {
			t.Fatalf("dependency %s: fired=%v", state, fired)
		}
	}
}

func TestCoordinatorProvenance(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%v", legacy), func(t *testing.T) {
			s, job, _, svc := resultFixture(t)
			code, elapsed := 0, int64(1234)
			local := snapshot.RunMeta{Machine: "laptop", Profile: "dev", Backend: "codex", Model: "gpt-model",
				ExecutionMetadata:  snapshot.ExecutionMetadata{OffloadRunID: "offload-123", CallerRunID: "caller-123", SessionID: "session-123", ExitCode: &code, ElapsedMs: &elapsed, ExecutionStatus: "ok"},
				DevelopmentSession: snapshot.DevelopmentSession{Workdir: "/private/never-upload"}, IssueBody: "never upload prompt"}
			report := jobResult{Status: "success", Usage: &snapshot.Usage{InputTokens: 42, CostUnavailable: true}}
			wantMachine := "w"
			if !legacy {
				report.Provenance = provenanceFromRun(local)
				wantMachine = "laptop"
			}
			raw, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte("never")) {
				t.Fatal("private local fields uploaded")
			}
			if err := svc.Complete(context.Background(), "task", "w", 3, raw); err != nil {
				t.Fatal(err)
			}
			dir := snapshot.RunDir(s.task.Repo, s.task.Issue, s.task.CreatedAt) + "-task-" + s.task.ID
			payload, err := os.ReadFile(filepath.Join(dir, "meta.json"))
			if err != nil {
				t.Fatal(err)
			}
			row, _, err := store.DecodeRun(dir, "run", payload)
			if err != nil {
				t.Fatal(err)
			}
			if row.Machine != wantMachine || row.CostUSD != nil || row.InputTokens == nil || *row.InputTokens != 42 {
				t.Fatalf("row %+v", row)
			}
			if !legacy {
				var got snapshot.RunMeta
				if err := json.Unmarshal(payload, &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(provenanceFromRun(got), report.Provenance) {
					t.Fatalf("provenance %+v", got)
				}
				if row.Model != local.Model || row.Profile != local.Profile || row.Backend != local.Backend || row.OffloadRunID != local.OffloadRunID {
					t.Fatalf("row %+v", row)
				}
			}
			t.Run("usage-api-postgres", func(t *testing.T) {
				db := storetest.New(t)
				if _, err := db.Migrate(context.Background(), store.Migrations); err != nil {
					t.Fatal(err)
				}
				snapshot.BindRunStore(db)
				t.Cleanup(func() { snapshot.BindRunStore(nil) })
				// Exercise the coordinator writer with the SQL store bound as in production.
				var saved coordinatorReport
				if err := json.Unmarshal(s.task.Result, &saved); err != nil {
					t.Fatal(err)
				}
				if err := applyCoordinatorReport(context.Background(), &s.task, job, &saved); err != nil {
					t.Fatal(err)
				}
				rows, err := db.Runs(context.Background(), "run", store.RunFilter{})
				if err != nil || len(rows) != 1 || rows[0].Machine != wantMachine {
					t.Fatalf("rows %+v, %v", rows, err)
				}
				if !legacy && (rows[0].Model != local.Model || rows[0].Profile != local.Profile || rows[0].Backend != local.Backend || rows[0].OffloadRunID != local.OffloadRunID) {
					t.Fatalf("row %+v", rows[0])
				}
				rr := httptest.NewRecorder()
				api.HandleUsage(rr, httptest.NewRequest("GET", "/api/usage", nil))
				var usage store.UsageSummary
				if err := json.Unmarshal(rr.Body.Bytes(), &usage); err != nil || rr.Code != 200 {
					t.Fatalf("usage %d %s: %v", rr.Code, rr.Body.String(), err)
				}
				machine := usage.ByMachine[wantMachine]
				if machine.Runs != 1 || machine.Coverage.Tokens != 1 || machine.Coverage.Cost != 0 || machine.TotalCostUSD != nil {
					t.Fatalf("machine %+v", machine)
				}
				if !legacy && (usage.ByModel[local.Model].Runs != 1 || usage.ByModel[local.Model].TotalCostUSD != nil || usage.ByProfile[local.Profile].Runs != 1) {
					t.Fatalf("usage %+v", usage)
				}
			})
		})
	}
}

func TestCoordinatorRejectsInvalidProvenance(t *testing.T) {
	for _, block := range []string{
		`{"machine":"laptop","prompt":"secret"}`, `{"workdir":"/private"}`,
		`{"machine":"` + strings.Repeat("x", 257) + `"}`,
		`{"model":"` + strings.Repeat("x", 257) + `"}`,
		`{"elapsed_ms":-1}`, `{"session_id":"line\nbreak"}`,
	} {
		t.Run(block[:min(len(block), 40)], func(t *testing.T) {
			s, _, c, svc := resultFixture(t)
			err := svc.Complete(context.Background(), "task", "w", 3, json.RawMessage(`{"status":"success","provenance":`+block+`}`))
			if !errors.Is(err, coordinator.ErrInvalid) || len(s.task.Result) != 0 || len(c.calls) != 0 {
				t.Fatalf("accepted invalid report: %v", err)
			}
		})
	}
}
