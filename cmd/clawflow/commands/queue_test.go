package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func queueVCS(t *testing.T, issues []map[string]any, subs map[int][]int, prs []map[string]any, subErr map[int]bool) {
	t.Helper()
	t.Setenv("GH_TOKEN", "queue-test-only")
	t.Setenv("GITLAB_TOKEN", "queue-test-only")
	t.Setenv("CLAWFLOW_PILOT_BUDGET_PATH", "")
	byNum := map[int]map[string]any{}
	for _, is := range issues {
		byNum[is["number"].(int)] = is
	}
	original := http.DefaultTransport
	http.DefaultTransport = reopenedLifecycleTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "vcs.invalid" {
			return nil, fmt.Errorf("unexpected external request: %s", r.URL.Host)
		}
		status := 200
		var body any = []any{}
		var n int
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/o/r":
			body = map[string]int{"id": 1}
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/issues":
			body = issues
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/labels"):
			fmt.Sscanf(r.URL.Path, "/repos/o/r/issues/%d/labels", &n)
			byNum[n]["labels"] = []map[string]string{{"name": "clawflow"}}
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls":
			body = prs
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/events"):
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/sub_issues"):
			if _, err := fmt.Sscanf(r.URL.Path, "/repos/o/r/issues/%d/sub_issues", &n); err != nil {
				return nil, err
			}
			if subErr[n] {
				status = 502
				break
			}
			var list []map[string]any
			for _, c := range subs[n] {
				list = append(list, byNum[c])
			}
			body = list
		default:
			return nil, fmt.Errorf("unexpected fake VCS call: %s %s", r.Method, r.URL.Path)
		}
		data, _ := json.Marshal(body)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
}

func queueParent(n, total int) map[string]any {
	is := targetedIssue(n)
	is["sub_issues_summary"] = map[string]int{"total": total, "completed": 0}
	return is
}

func queueConfig(t *testing.T) {
	t.Helper()
	targetedConfig(t, func(r *config.Repo) { r.Enabled = true })
}

func runQueueOnce(t *testing.T, onlyIssue int) []int {
	t.Helper()
	dispatched := recordDispatch(t)
	if err := runOnce(context.Background(), "o/r", onlyIssue, time.Minute); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	return dispatched()
}

func plannerIssue(n int, created string, labels ...string) vcs.Issue {
	return vcs.Issue{Number: n, State: "open", Labels: labels, CreatedAt: created, Title: fmt.Sprintf("Issue %d", n)}
}

func plannerInput(repo string, issues ...vcs.Issue) repoQueueInput {
	state := map[int]string{}
	jobs := map[int]bool{}
	for _, is := range issues {
		state[is.Number] = ""
		jobs[is.Number] = true
	}
	return repoQueueInput{
		Repo: repo, Issues: issues, ImplementState: state, ImplementJobs: jobs,
		Locked:      func(int) bool { return false },
		OpenPRs:     func() ([]vcs.PR, error) { return nil, nil },
		MaxInFlight: 2,
		ParkAfter:   45 * time.Minute,
		Now:         time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		DepOpen:     func(queue.Key) bool { return true },
	}
}

func pickedNumbers(p repoQueuePlan) []int {
	var out []int
	for _, e := range p.Result.Entries {
		if e.Pick {
			out = append(out, e.Key.Number)
		}
	}
	return out
}

func TestPlanRepoQueueOrderAndCaps(t *testing.T) {
	in := plannerInput("o/r",
		plannerIssue(1, "2026-01-01T00:00:00Z"),
		plannerIssue(2, "2026-01-02T00:00:00Z", "P1"),
		plannerIssue(3, "2026-01-03T00:00:00Z"),
	)
	if got := pickedNumbers(planRepoQueue(in)); len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Fatalf("picked = %v, want [2 1]", got)
	}
	in.Rank = []queue.Key{{Repo: "o/r", Number: 3}}
	if got := pickedNumbers(planRepoQueue(in)); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("ranked picked = %v, want [2 3]", got)
	}
	in.MaxInFlight = 1
	if got := pickedNumbers(planRepoQueue(in)); len(got) != 1 || got[0] != 2 {
		t.Fatalf("cap 1 picked = %v", got)
	}
}

func TestPlanRepoQueueBlockedDoesNotStall(t *testing.T) {
	in := plannerInput("o/r", plannerIssue(1, "2026-01-01T00:00:00Z", "agent-failed"), plannerIssue(2, "2026-01-02T00:00:00Z"))
	in.ImplementState[1] = `excluded label "agent-failed" present`
	delete(in.ImplementJobs, 1)
	p := planRepoQueue(in)
	if got := pickedNumbers(p); len(got) != 1 || got[0] != 2 {
		t.Fatalf("picked = %v", got)
	}
	if p.Result.Entries[0].Label() != "blocked:label_agent-failed" {
		t.Fatalf("state = %s", p.Result.Entries[0].Label())
	}
}

func TestPlanRepoQueueInFlightLockAndPR(t *testing.T) {
	in := plannerInput("o/r",
		plannerIssue(1, "2026-01-01T00:00:00Z"),
		plannerIssue(2, "2026-01-02T00:00:00Z", "agent-implemented"),
		plannerIssue(3, "2026-01-03T00:00:00Z"),
		plannerIssue(4, "2026-01-04T00:00:00Z", "agent-implemented"), // no open PR: delivered
	)
	in.Locked = func(n int) bool { return n == 1 }
	in.OpenPRs = func() ([]vcs.PR, error) {
		return []vcs.PR{{Number: 90, State: "open", HeadBranch: "x", Body: "Fixes #2"}}, nil
	}
	p := planRepoQueue(in)
	if p.Result.InFlight != 2 || len(pickedNumbers(p)) != 0 {
		t.Fatalf("inflight=%d picked=%v", p.Result.InFlight, pickedNumbers(p))
	}
	for _, e := range p.Result.Entries {
		if e.Key.Number == 4 {
			t.Fatal("delivered issue must not be a candidate")
		}
	}
}

func TestPlanRepoQueueFailsClosedOnUnreadablePRs(t *testing.T) {
	logs := withRunLog(t)
	in := plannerInput("o/r", plannerIssue(1, "2026-01-01T00:00:00Z", "agent-implemented"), plannerIssue(2, "2026-01-02T00:00:00Z"))
	in.OpenPRs = func() ([]vcs.PR, error) { return nil, errors.New("HTTP 502") }
	p := planRepoQueue(in)
	if !strings.Contains(p.Err, "open_prs_unreadable") {
		t.Fatalf("err = %q", p.Err)
	}
	jobs := []*runJob{{op: &operator.Operator{Name: "implement"}, sub: &operator.Subject{Number: 2}}, {op: &operator.Operator{Name: "track-progress"}, sub: &operator.Subject{Number: 9}}}
	kept := applyUnifiedQueue("o/r", jobs, p)
	if len(kept) != 1 || kept[0].op.Name != "track-progress" {
		t.Fatalf("kept = %+v", kept)
	}
	if !strings.Contains(logs(), "open_prs_unreadable") {
		t.Fatal("fail-closed reason must be logged")
	}
}

func TestPlanRepoQueueLabelsAndParents(t *testing.T) {
	in := plannerInput("o/r", plannerIssue(1, "2026-01-01T00:00:00Z"), plannerIssue(2, "2026-01-02T00:00:00Z", "clawflow"), plannerIssue(3, "2026-01-03T00:00:00Z", "clawflow"))
	in.Issues[0].Labels = nil
	in.HasChildren = map[int]bool{3: true}
	if got := pickedNumbers(planRepoQueue(in)); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("picked = %v, want [1 2]", got)
	}
}

func TestPlanRepoQueueDependencies(t *testing.T) {
	a := plannerIssue(1, "2026-01-01T00:00:00Z")
	a.Body = "Depends on: #2, other/repo#7"
	in := plannerInput("o/r", a, plannerIssue(2, "2026-01-02T00:00:00Z"), plannerIssue(3, "2026-01-03T00:00:00Z"))
	in.DepOpen = func(k queue.Key) bool { return k.Repo == "other/repo" && k.Number == 7 }
	p := planRepoQueue(in)
	if got := pickedNumbers(p); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("picked = %v", got)
	}
	if lbl := p.Result.Entries[0].Label(); lbl != "waiting:dependency" {
		t.Fatalf("dep state = %s", lbl)
	}
	// #2 closed (absent from the open scan) and the cross-repo dep closed.
	in = plannerInput("o/r", a, plannerIssue(3, "2026-01-03T00:00:00Z"))
	in.DepOpen = func(queue.Key) bool { return false }
	if got := pickedNumbers(planRepoQueue(in)); len(got) != 2 || got[0] != 1 {
		t.Fatalf("closed deps must release: %v", got)
	}
}

func TestPlanRepoQueueGitLabRepoParticipates(t *testing.T) {
	in := plannerInput("group/sub/project", plannerIssue(5, "2026-01-01T00:00:00Z", "P0"), plannerIssue(6, "2026-01-02T00:00:00Z"))
	p := planRepoQueue(in)
	if got := pickedNumbers(p); len(got) != 2 || got[0] != 5 {
		t.Fatalf("picked = %v", got)
	}
	if p.Result.Entries[0].Key.String() != "group/sub/project#5" {
		t.Fatalf("key = %s", p.Result.Entries[0].Key)
	}
}

func TestPlanRepoQueueParkAndUnparkLifecycle(t *testing.T) {
	logs := withRunLog(t)
	in := plannerInput("o/r",
		plannerIssue(1, "2026-01-01T00:00:00Z", "agent-implemented"),
		plannerIssue(2, "2026-01-02T00:00:00Z"),
		plannerIssue(3, "2026-01-03T00:00:00Z"),
	)
	in.MaxInFlight = 1
	in.OpenPRs = func() ([]vcs.PR, error) {
		return []vcs.PR{{Number: 90, State: "open", HeadBranch: "x", Body: "Fixes #1"}}, nil
	}
	blocked := "ci_failed"
	in.FlightBlocked = func(vcs.Issue, *vcs.PR) string { return blocked }

	p := planRepoQueue(in)
	if len(pickedNumbers(p)) != 0 {
		t.Fatalf("fresh block must hold the slot: %v", pickedNumbers(p))
	}
	applyUnifiedQueue("o/r", nil, p)

	in.State, _ = queue.LoadState("o/r")
	in.Now = in.Now.Add(46 * time.Minute)
	p = planRepoQueue(in)
	if got := pickedNumbers(p); len(got) != 1 || got[0] != 2 {
		t.Fatalf("parked item must free its slot: %v", got)
	}
	applyUnifiedQueue("o/r", nil, p)

	blocked = ""
	in.State, _ = queue.LoadState("o/r")
	p = planRepoQueue(in)
	if len(pickedNumbers(p)) != 0 || p.Result.Entries[0].State != queue.StateInFlight {
		t.Fatalf("unparked item holds its slot again: %v %v", pickedNumbers(p), p.Result.Entries[0])
	}
	applyUnifiedQueue("o/r", nil, p)
	out := logs()
	for _, want := range []string{"queue/park", "queue/unpark"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in log:\n%s", want, out)
		}
	}
}

func TestPlanRepoQueueFormatWarningsOnce(t *testing.T) {
	logs := withRunLog(t)
	bad := plannerIssue(1, "2026-01-01T00:00:00Z", "p1")
	bad.Body = "Depends on: nonsense"
	in := plannerInput("o/r", bad)
	for i := 0; i < 2; i++ {
		p := planRepoQueue(in)
		applyUnifiedQueue("o/r", nil, p)
		in.State, _ = queue.LoadState("o/r")
	}
	if n := strings.Count(logs(), "queue/format_warning"); n != 2 {
		t.Fatalf("want 2 warnings (one per reason, once each), got %d", n)
	}
}

func TestRunOnceUnifiedOptOutQueue(t *testing.T) {
	logs := withRunLog(t)
	queueConfig(t)
	queueVCS(t,
		[]map[string]any{targetedIssue(9, "clawflow"), targetedIssue(7, "clawflow"), targetedIssue(5, "clawflow", "P0"), targetedIssue(3, "clawflow", "agent-failed"), targetedIssue(1, "P0", "clawflow-skip")},
		nil, nil, nil)
	if _, err := queue.UpdateRank(func(order []queue.Key) ([]queue.Key, error) {
		return append(order, queue.Key{Repo: "o/r", Number: 1}), nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	got := runQueueOnce(t, 0)
	for _, n := range got {
		if n == 1 {
			t.Fatal("owner-skipped ranked issue dispatched")
		}
	}
	st, err := queue.LoadState("o/r")
	if err != nil || st.Skipped != 1 {
		t.Fatalf("state=%+v err=%v", st, err)
	}
	if len(got) != 2 {
		t.Fatalf("dispatched = %v, want 2 (default max_in_flight)", got)
	}
	if !strings.Contains(logs(), "queue/pick") {
		t.Fatal("missing queue/pick log")
	}
}

func TestRepoSetMaxInFlight(t *testing.T) {
	writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{"o/gh": {Platform: "github"}}})
	run := func(args ...string) error {
		cmd := newRepoSetCmd()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		return cmd.Execute()
	}
	if err := run("o/gh", "--max-in-flight", "0"); err == nil {
		t.Fatal("0 must be rejected")
	}
	if err := run("o/gh", "--max-in-flight", "3"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.Load(); cfg.Repos["o/gh"].MaxInFlight != 3 {
		t.Fatalf("max_in_flight = %d", cfg.Repos["o/gh"].MaxInFlight)
	}
}

func TestQueueRepoParamsDefaults(t *testing.T) {
	m, p := queueRepoParams(config.Repo{}, config.QueueSettings{})
	if m != 2 || p != 45*time.Minute {
		t.Fatalf("defaults = %d %s", m, p)
	}
	m, p = queueRepoParams(config.Repo{MaxInFlight: 5}, config.QueueSettings{ParkAfter: "10m"})
	if m != 5 || p != 10*time.Minute {
		t.Fatalf("custom = %d %s", m, p)
	}
	if _, p = queueRepoParams(config.Repo{}, config.QueueSettings{ParkAfter: "junk"}); p != 45*time.Minute {
		t.Fatalf("bad park_after must fall back, got %s", p)
	}
}

func TestSkipCountsWithoutDispatch(t *testing.T) {
	in := plannerInput("o/r", plannerIssue(1, "", "clawflow"), plannerIssue(2, ""), plannerIssue(3, ""))
	in.Issues[1].Labels = []string{"clawflow-skip"}
	in.Issues[2].State = "closed"
	in.Rank = []queue.Key{{Repo: "o/r", Number: 2}}
	p := planRepoQueue(in)
	if len(p.Picks) != 1 || !p.Picks[1] || p.State.Skipped != 1 {
		t.Fatalf("picks=%v skipped=%d", p.Picks, p.State.Skipped)
	}
}

func TestOptOutQueueSelection(t *testing.T) {
	for _, labels := range [][]string{nil, {"clawflow"}} {
		p := planRepoQueue(plannerInput("o/r", plannerIssue(1, "", labels...)))
		if !p.Picks[1] {
			t.Fatalf("labels %v excluded: %+v", labels, p)
		}
	}
	in := plannerInput("o/r", plannerIssue(1, "", "clawflow-skip"), plannerIssue(2, ""), plannerIssue(3, "", "P0"))
	in.Issues[1].SubIssuesTotal = 1
	p := planRepoQueue(in)
	if len(p.Picks) != 1 || !p.Picks[3] || p.State.Skipped != 1 {
		t.Fatalf("unexpected picks: %+v", p)
	}
}

func TestRunOptOutPreservesExclusions(t *testing.T) {
	withRunLog(t)
	queueConfig(t)
	queueVCS(t, []map[string]any{
		targetedIssue(1), targetedIssue(2, "clawflow-skip"), targetedIssue(3, "agent-skipped"), targetedIssue(4, "agent-failed"), targetedIssue(5, "agent-running"), targetedIssue(6, "tracking"), targetedIssue(7, "agent-implemented"),
	}, nil, nil, nil)
	got := runQueueOnce(t, 0)
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("dispatched %v", got)
	}
}

func TestPlanRepoQueueSkippedRunKeepsSlotUntilUnlocked(t *testing.T) {
	in := plannerInput("o/r",
		plannerIssue(1, "2026-01-01T00:00:00Z", "clawflow-skip", "agent-implemented"),
		plannerIssue(2, "2026-01-02T00:00:00Z"),
	)
	in.MaxInFlight = 1
	in.Locked = func(n int) bool { return n == 1 }
	in.OpenPRs = func() ([]vcs.PR, error) {
		t.Fatal("skipped issue must not inspect PR delivery")
		return nil, nil
	}
	in.FlightBlocked = func(vcs.Issue, *vcs.PR) string {
		t.Fatal("skipped active run must not be parked")
		return "blocked"
	}
	in.State.BlockedSince = map[int]time.Time{1: in.Now.Add(-2 * in.ParkAfter)}
	in.State.Parked = []int{1}
	p := planRepoQueue(in)
	if p.Result.InFlight != 1 || len(p.Picks) != 0 || p.State.Skipped != 1 {
		t.Fatalf("locked skipped run: inflight=%d picks=%v skipped=%d", p.Result.InFlight, p.Picks, p.State.Skipped)
	}
	in.MaxInFlight = 2
	p = planRepoQueue(in)
	if p.Result.InFlight != 1 || len(p.Picks) != 1 || !p.Picks[2] {
		t.Fatalf("spare slot: inflight=%d picks=%v", p.Result.InFlight, p.Picks)
	}
	in.MaxInFlight = 1
	in.Locked = func(int) bool { return false }
	p = planRepoQueue(in)
	if p.Result.InFlight != 0 || len(p.Picks) != 1 || !p.Picks[2] {
		t.Fatalf("unlocked skipped run: inflight=%d picks=%v", p.Result.InFlight, p.Picks)
	}
}
