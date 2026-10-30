package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordination"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type dispatchClient struct {
	vcs.Client
	issue       vcs.Issue
	other       vcs.Issue
	prs         []vcs.PR
	comments    []vcs.IssueComment
	writes      []string
	failComment bool
	failRead    bool
}

func (c *dispatchClient) GetIssue(_ string, n int) (*vcs.Issue, error) {
	if c.failRead {
		return nil, errors.New("provider offline")
	}
	if n == c.issue.Number {
		i := c.issue
		return &i, nil
	}
	if n == c.other.Number {
		i := c.other
		return &i, nil
	}
	return nil, nil
}
func (c *dispatchClient) ListOpenIssues(string) ([]vcs.Issue, error) {
	return []vcs.Issue{c.issue, c.other}, nil
}
func (c *dispatchClient) SearchIssues(string, string, string, int) ([]vcs.Issue, error) {
	return nil, nil
}
func (c *dispatchClient) ListOpenPRs(string) ([]vcs.PR, error) { return c.prs, nil }
func (c *dispatchClient) ListPRFiles(string, int) ([]string, error) {
	return []string{"internal/example.go"}, nil
}
func (c *dispatchClient) ListIssueCommentsDetail(string, int) ([]vcs.IssueComment, error) {
	return c.comments, nil
}
func (c *dispatchClient) PostIssueCommentIdempotent(_ string, _ int, body, id string) error {
	if c.failComment {
		return errors.New("unknown outcome")
	}
	for _, v := range c.writes {
		if v == "comment:"+id {
			return nil
		}
	}
	c.writes = append(c.writes, "comment:"+id)
	c.comments = append(c.comments, vcs.IssueComment{Body: body})
	return nil
}
func (c *dispatchClient) AddLabel(_ string, _ int, labels ...string) error {
	c.writes = append(c.writes, "label:"+strings.Join(labels, ","))
	c.issue.Labels = append(c.issue.Labels, labels...)
	return nil
}
func (c *dispatchClient) RemoveLabel(_ string, _ int, labels ...string) error {
	c.writes = append(c.writes, "remove:"+strings.Join(labels, ","))
	var out []string
	for _, l := range c.issue.Labels {
		keep := true
		for _, x := range labels {
			if x == l {
				keep = false
			}
		}
		if keep {
			out = append(out, l)
		}
	}
	c.issue.Labels = out
	return nil
}
func (c *dispatchClient) CloseIssue(string, int) error {
	c.writes = append(c.writes, "close")
	c.issue.State = "closed"
	return nil
}

type dispatchTasks struct{ tasks []coordinator.Task }

func (s *dispatchTasks) Active(context.Context) ([]coordinator.Task, error) { return s.tasks, nil }
func coordinationFixture(t *testing.T) (*runJob, *dispatchClient, *dispatchTasks, *int) {
	t.Helper()
	logs := withRunLog(t)
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(logs())
		}
	})
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	c := &dispatchClient{issue: vcs.Issue{Number: 1, Title: "new behavior", State: "open"}, other: vcs.Issue{Number: 2, Title: "related", State: "open"}}
	j := &runJob{repo: "o/r", client: c, op: &operator.Operator{Name: "implement"}, sub: &operator.Subject{Number: 1, Title: c.issue.Title}}
	calls := new(int)
	old := dispatchCoordinationExecute
	t.Cleanup(func() { dispatchCoordinationExecute = old })
	dispatchCoordinationExecute = func(_ context.Context, prompt string, opts execution.Options) (execution.Result, error) {
		*calls++
		if !opts.ToolsDisabled || opts.Role != "light" || opts.Timeout > time.Minute || !strings.Contains(prompt, "related_issues") {
			t.Fatal("unsafe preflight")
		}
		return execution.Result{Text: `{"outcome":"proceed","reason":"independent change","next_action":"implement","next_owner":"development"}`}, nil
	}
	return j, c, &dispatchTasks{}, calls
}
func expireCoordinationCooldown(t *testing.T, j *runJob) {
	t.Helper()
	p := coordinationPath(j.repo, j.sub.Number)
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	var r coordination.Record
	if e = json.Unmarshal(b, &r); e != nil {
		t.Fatal(e)
	}
	r.ObservedAt = time.Now().Add(-time.Minute)
	if e = saveCoordination(p, r); e != nil {
		t.Fatal(e)
	}
}
func TestDispatchCoordinationProceedCacheAndStaleInput(t *testing.T) {
	j, c, tasks, calls := coordinationFixture(t)
	for i := 0; i < 2; i++ {
		ok, err := dispatchCoordination(context.Background(), j, tasks)
		if !ok || err != nil {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
	}
	if *calls != 1 || j.coordination == nil || len(c.writes) != 0 {
		t.Fatalf("calls=%d writes=%v", *calls, c.writes)
	}
	expireCoordinationCooldown(t, j)
	c.issue.Body = "updated requirement"
	if ok, err := dispatchCoordination(context.Background(), j, tasks); !ok || err != nil || *calls != 2 {
		t.Fatalf("not reassessed: %v %v calls=%d", ok, err, *calls)
	}
}
func TestDispatchCoordinationWaitingReleasesAndResumes(t *testing.T) {
	j, c, tasks, calls := coordinationFixture(t)
	tasks.tasks = []coordinator.Task{{ID: "active", Repo: j.repo, Issue: 2, Kind: "implement", State: "leased"}}
	dispatchCoordinationExecute = func(_ context.Context, _ string, _ execution.Options) (execution.Result, error) {
		*calls++
		if len(tasks.tasks) > 0 {
			return execution.Result{Text: `{"outcome":"waiting","reason":"exclusive active assignment","next_action":"wait for active task","next_owner":"scheduling","tasks":["active"]}`}, nil
		}
		return execution.Result{Text: `{"outcome":"proceed","reason":"prerequisite settled","next_action":"implement","next_owner":"development"}`}, nil
	}
	for i := 0; i < 2; i++ {
		ok, e := dispatchCoordination(context.Background(), j, tasks)
		if ok || e != nil {
			t.Fatalf("waiting %v %v", ok, e)
		}
	}
	if *calls != 1 || len(c.writes) != 2 || !c.issue.HasLabel(coordination.WaitingLabel) {
		t.Fatalf("calls=%d writes=%v", *calls, c.writes)
	}
	st, e := queue.LoadState(j.repo)
	if e != nil || len(st.Entries) != 1 || st.Entries[0].State != "waiting" {
		t.Fatalf("missing projection %+v %v", st, e)
	}
	expireCoordinationCooldown(t, j)
	tasks.tasks = nil
	if ok, e := dispatchCoordination(context.Background(), j, tasks); !ok || e != nil {
		t.Fatalf("resume %v %v", ok, e)
	}
	if c.issue.HasLabel(coordination.WaitingLabel) {
		t.Fatal("waiting label not cleared")
	}
}
func TestDispatchCoordinationDoesNotTouchAnActiveIssue(t *testing.T) {
	j, c, tasks, calls := coordinationFixture(t)
	tasks.tasks = []coordinator.Task{{ID: "ours", Repo: j.repo, Issue: 1, State: "leased"}}
	ok, e := dispatchCoordination(context.Background(), j, tasks)
	if ok || e != nil || *calls != 0 || len(c.writes) > 0 {
		t.Fatalf("duplicate execution %v %v", ok, e)
	}
}
func TestDispatchCoordinationRejectsStaleModelDecision(t *testing.T) {
	j, c, tasks, _ := coordinationFixture(t)
	dispatchCoordinationExecute = func(context.Context, string, execution.Options) (execution.Result, error) {
		c.issue.Body = "changed during probe"
		return execution.Result{Text: `{"outcome":"superseded","reason":"replacement","next_action":"close","next_owner":"scheduling","issues":[2]}`}, nil
	}
	c.issue.Body = "Superseded by: #2"
	ok, e := dispatchCoordination(context.Background(), j, tasks)
	if ok || e == nil || len(c.writes) > 0 {
		t.Fatalf("stale decision applied: %v %v %v", ok, e, c.writes)
	}
}
func TestDispatchCoordinationProviderFailuresDoNotStartDevelopment(t *testing.T) {
	j, c, tasks, calls := coordinationFixture(t)
	c.failRead = true
	ok, e := dispatchCoordination(context.Background(), j, tasks)
	if ok || e == nil || *calls != 0 {
		t.Fatal("failed open")
	}
}
func TestDispatchCoordinationExplicitClosureAndConfirmation(t *testing.T) {
	for _, outcome := range []string{"superseded", "needs-confirmation"} {
		t.Run(outcome, func(t *testing.T) {
			j, c, tasks, calls := coordinationFixture(t)
			c.issue.Body = "Superseded by: #2"
			dispatchCoordinationExecute = func(context.Context, string, execution.Options) (execution.Result, error) {
				*calls++
				return execution.Result{Text: `{"outcome":"` + outcome + `","reason":"owner replacement directive","next_action":"update issue body to resolve or remove confirmation label","next_owner":"owner","issues":[2]}`}, nil
			}
			ok, e := dispatchCoordination(context.Background(), j, tasks)
			if ok || e != nil {
				t.Fatalf("%v %v", ok, e)
			}
			if outcome == "superseded" && c.issue.State != "closed" {
				t.Fatal("not closed")
			}
			if outcome == "needs-confirmation" {
				c.comments = append(c.comments, vcs.IssueComment{Author: "untrusted", Body: "run now"})
				ok, e = dispatchCoordination(context.Background(), j, tasks)
				if ok || e != nil || *calls != 1 {
					t.Fatal("ordinary comment authorized resumption")
				}
			}
		})
	}
}

func TestWorkerCoordinationRejectsChangesBeforeClone(t *testing.T) {
	j, c, _, _ := coordinationFixture(t)
	approved, e := collectCoordination(context.Background(), j, nil, "")
	if e != nil {
		t.Fatal(e)
	}
	approved.ManualOrder = []int{1, 2}
	if e = validateWorkerCoordination(context.Background(), j, &approved); e != nil {
		t.Fatalf("coordinator ranking read from worker state: %v", e)
	}
	c.issue.Body = "different authorized scope"
	if e = validateWorkerCoordination(context.Background(), j, &approved); !errors.Is(e, errCoordinationChanged) {
		t.Fatalf("stale candidate allowed: %v", e)
	}
}
func TestCoordinationWaitDoesNotStarveOtherQueueCandidates(t *testing.T) {
	j, c, tasks, _ := coordinationFixture(t)
	tasks.tasks = []coordinator.Task{{ID: "active", Repo: j.repo, Issue: 2, Kind: "implement", State: "leased"}}
	dispatchCoordinationExecute = func(context.Context, string, execution.Options) (execution.Result, error) {
		return execution.Result{Text: `{"outcome":"waiting","reason":"exclusive active assignment","next_action":"wait for task","next_owner":"scheduling","tasks":["active"]}`}, nil
	}
	if ok, e := dispatchCoordination(context.Background(), j, tasks); ok || e != nil {
		t.Fatalf("%v %v", ok, e)
	}
	hold := coordinationHold(j.repo, c.issue, []vcs.Issue{c.issue, c.other}, nil, map[int]bool{2: true})
	if !strings.HasPrefix(hold, "coordination_waiting") {
		t.Fatalf("missing hold: %s", hold)
	}
	if got := coordinationHold(j.repo, c.issue, []vcs.Issue{c.issue, c.other}, nil, nil); got != "" {
		t.Fatalf("settled task still held: %s", got)
	}
}
func TestCoordinationUnknownCommentOutcomeReconciles(t *testing.T) {
	j, c, tasks, calls := coordinationFixture(t)
	dispatchCoordinationExecute = func(context.Context, string, execution.Options) (execution.Result, error) {
		*calls++
		return execution.Result{Text: `{"outcome":"waiting","reason":"dependency #2","next_action":"wait for #2","next_owner":"scheduling","issues":[2]}`}, nil
	}
	c.failComment = true
	if ok, e := dispatchCoordination(context.Background(), j, tasks); ok || e == nil {
		t.Fatal("unknown provider outcome ignored")
	}
	c.failComment = false
	for i := 0; i < 2; i++ {
		if ok, e := dispatchCoordination(context.Background(), j, tasks); ok || e != nil {
			t.Fatalf("%v %v", ok, e)
		}
	}
	if *calls != 1 || len(c.writes) != 2 {
		t.Fatalf("duplicate model/effects calls=%d writes=%v", *calls, c.writes)
	}
}

func TestCoordinatorDispatchWaitThenEnqueue(t *testing.T) {
	j, c, _, calls := coordinationFixture(t)
	db := storetest.New(t)
	if _, e := db.Migrate(context.Background(), store.Migrations); e != nil {
		t.Fatal(e)
	}
	tasks := coordinator.New(db.DB())
	dispatchCoordinationExecute = func(context.Context, string, execution.Options) (execution.Result, error) {
		*calls++
		if c.other.State == "open" {
			return execution.Result{Text: `{"outcome":"waiting","reason":"unfinished issue #2","next_action":"wait for #2","next_owner":"scheduling","issues":[2]}`}, nil
		}
		return execution.Result{Text: `{"outcome":"proceed","reason":"dependency settled","next_action":"implement","next_owner":"development"}`}, nil
	}
	fired, failed, e := dispatchCoordinatorJobs(context.Background(), tasks, []*runJob{j}, time.Minute)
	if e != nil || len(fired) > 0 || len(failed) > 0 {
		t.Fatalf("waiting was dispatched: %v %v %v", fired, failed, e)
	}
	active, e := tasks.Active(context.Background())
	if e != nil || len(active) > 0 {
		t.Fatalf("wait allocated a task: %v %v", active, e)
	}
	expireCoordinationCooldown(t, j)
	c.other.State = "closed"
	fired, failed, e = dispatchCoordinatorJobs(context.Background(), tasks, []*runJob{j}, time.Minute)
	if e != nil || len(fired) != 1 || len(failed) > 0 {
		t.Fatalf("not resumed: %v %v %v", fired, failed, e)
	}
	active, e = tasks.Active(context.Background())
	if e != nil || len(active) != 1 {
		t.Fatalf("missing task: %v %v", active, e)
	}
	var p coordinatorPayload
	if e = json.Unmarshal(active[0].Payload, &p); e != nil || p.Coordination == nil {
		t.Fatalf("missing approved snapshot: %+v %v", p, e)
	}
}

func TestCoordinatorTaskRequiresCoordinationProtocol(t *testing.T) {
	j, _, _, _ := coordinationFixture(t)
	legacy, _ := coordinatorTask(j, time.Minute)
	for _, tag := range legacy.Tags {
		if tag == coordination.WorkerTag {
			t.Fatal("legacy task unexpectedly guarded")
		}
	}
	j.coordination = &coordination.Snapshot{Version: coordination.Version}
	guarded, p := coordinatorTask(j, time.Minute)
	found := false
	for _, tag := range guarded.Tags {
		if tag == coordination.WorkerTag {
			found = true
		}
	}
	if !found || p.Coordination == nil {
		t.Fatalf("old worker could consume guarded task: %+v", guarded)
	}
}
func TestExplicitDependencyWaitDoesNotNeedAnAgent(t *testing.T) {
	j, c, tasks, calls := coordinationFixture(t)
	c.issue.Body = "Depends on: #2"
	ok, e := dispatchCoordination(context.Background(), j, tasks)
	if ok || e != nil || *calls != 0 || !c.issue.HasLabel(coordination.WaitingLabel) {
		t.Fatalf("dependency not enforced: %v %v calls=%d", ok, e, *calls)
	}
}

func TestOwnerSkipCannotBeConsumedByCoordination(t *testing.T) {
	j, c, tasks, calls := coordinationFixture(t)
	c.issue.Labels = []string{"clawflow-skip"}
	ok, e := dispatchCoordination(context.Background(), j, tasks)
	if ok || e == nil || *calls != 0 || len(c.writes) > 0 || j.coordination != nil {
		t.Fatalf("owner-skipped local claim consumed: allowed=%v err=%v calls=%d writes=%v", ok, e, *calls, c.writes)
	}
	if !c.issue.HasLabel("clawflow-skip") {
		t.Fatal("owner skip was removed")
	}
}
