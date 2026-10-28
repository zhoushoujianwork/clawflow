package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestValidateQueueIssue(t *testing.T) {
	if err := validateQueueIssue(config.Repo{Platform: "github"}, 12); err != nil {
		t.Fatal(err)
	}
	if err := validateQueueIssue(config.Repo{Platform: "gitlab"}, 0); err != nil {
		t.Fatalf("clearing must be allowed on GitLab: %v", err)
	}
	if err := validateQueueIssue(config.Repo{Platform: "gitlab"}, 12); err == nil || !strings.Contains(err.Error(), "GitLab") {
		t.Fatalf("gitlab err = %v", err)
	}
	if err := validateQueueIssue(config.Repo{}, -1); err == nil {
		t.Fatal("negative must be rejected")
	}
}

func TestRepoSetQueueIssueCommand(t *testing.T) {
	writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{
		"o/gh": {Platform: "github"},
		"o/gl": {Platform: "gitlab", BaseURL: "https://gitlab.invalid"},
	}})
	run := func(args ...string) error {
		cmd := newRepoSetCmd()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		return cmd.Execute()
	}
	if err := run("o/gh", "--queue-issue", "42"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load()
	if cfg.Repos["o/gh"].QueueIssue != 42 {
		t.Fatalf("queue_issue = %d", cfg.Repos["o/gh"].QueueIssue)
	}
	if err := run("o/gh", "--auto-merge", "on"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = config.Load(); cfg.Repos["o/gh"].QueueIssue != 42 {
		t.Fatal("unrelated set must keep queue_issue")
	}
	if err := run("o/gh", "--queue-issue", "0"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = config.Load(); cfg.Repos["o/gh"].QueueIssue != 0 {
		t.Fatal("--queue-issue 0 must clear")
	}
	if err := run("o/gl", "--queue-issue", "5"); err == nil || !strings.Contains(err.Error(), "GitLab") {
		t.Fatalf("gitlab err = %v", err)
	}
	if cfg, _ = config.Load(); cfg.Repos["o/gl"].QueueIssue != 0 {
		t.Fatal("rejected value must not be saved")
	}
}

// queueVCS is an in-memory GitHub for runOnce queue tests. Queue issue 100 has
// the given sub-issue numbers (in order); every number in `issues` is open.
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

func queueConfig(t *testing.T, queue int) {
	t.Helper()
	targetedConfig(t, func(r *config.Repo) { r.Enabled = true; r.QueueIssue = queue })
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
		Scope:       "all",
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

func TestPlanRepoQueueScopeLabeledAndParents(t *testing.T) {
	in := plannerInput("o/r", plannerIssue(1, "2026-01-01T00:00:00Z"), plannerIssue(2, "2026-01-02T00:00:00Z", "clawflow"), plannerIssue(3, "2026-01-03T00:00:00Z", "clawflow"))
	in.Scope = "labeled"
	in.HasChildren = map[int]bool{3: true}
	if got := pickedNumbers(planRepoQueue(in)); len(got) != 1 || got[0] != 2 {
		t.Fatalf("picked = %v, want [2]", got)
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

func TestRunOnceUnifiedQueueWithoutQueueIssue(t *testing.T) {
	logs := withRunLog(t)
	queueConfig(t, 0)
	queueVCS(t,
		[]map[string]any{targetedIssue(9, "clawflow"), targetedIssue(7, "clawflow"), targetedIssue(5, "clawflow", "P0"), targetedIssue(3, "clawflow", "agent-failed")},
		nil, nil, nil)
	got := runQueueOnce(t, 0)
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
	m, p, s := queueRepoParams(config.Repo{}, config.QueueSettings{})
	if m != 2 || p != 45*time.Minute || s != "labeled" {
		t.Fatalf("defaults = %d %s %s", m, p, s)
	}
	m, p, s = queueRepoParams(config.Repo{MaxInFlight: 5}, config.QueueSettings{Scope: "labeled", ParkAfter: "10m"})
	if m != 5 || p != 10*time.Minute || s != "labeled" {
		t.Fatalf("custom = %d %s %s", m, p, s)
	}
	if _, p, _ = queueRepoParams(config.Repo{}, config.QueueSettings{ParkAfter: "junk"}); p != 45*time.Minute {
		t.Fatalf("bad park_after must fall back, got %s", p)
	}
}

type fakeQueueLister struct {
	subs     []vcs.Issue
	err      error
	calls    int
	labels   []int
	labelErr error
}

func (f *fakeQueueLister) ListSubIssues(string, int) ([]vcs.Issue, error) {
	f.calls++
	return f.subs, f.err
}

func (f *fakeQueueLister) AddLabel(_ string, n int, labels ...string) error {
	f.labels = append(f.labels, n)
	return f.labelErr
}

func noScopeWrite() error { return nil }

func TestMigrateQueueIssueImportsOnceInOrder(t *testing.T) {
	logs := withRunLog(t)
	if err := queue.SaveState(queue.RepoState{Repo: "o/r"}); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.UpdateRank(func([]queue.Key) ([]queue.Key, error) {
		return []queue.Key{{Repo: "o/other", Number: 4}, {Repo: "o/r", Number: 7}}, nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	lister := &fakeQueueLister{subs: []vcs.Issue{{Number: 9, State: "open"}, {Number: 7, State: "open"}, {Number: 8, State: "closed"}, {Number: 5, State: "open"}}}
	st, _ := queue.LoadState("o/r")
	if !migrateQueueIssue(lister, "o/r", 100, false, noScopeWrite, &st) {
		t.Fatal("migration should succeed")
	}
	order, _ := queue.LoadRank()
	want := []string{"o/other#4", "o/r#7", "o/r#9", "o/r#5"}
	var got []string
	for _, k := range order {
		got = append(got, k.String())
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("rank = %v, want %v", got, want)
	}
	if !st.Imported || !st.DeprecationShown {
		t.Fatalf("state = %+v", st)
	}
	// Second pass: no import, no second notice.
	if !migrateQueueIssue(lister, "o/r", 100, false, noScopeWrite, &st) || lister.calls != 1 {
		t.Fatalf("import must happen once, calls=%d", lister.calls)
	}
	out := logs()
	if strings.Count(out, "queue/import") != 1 || strings.Count(out, "queue/deprecated") != 1 {
		t.Fatalf("log:\n%s", out)
	}
	// No queue_issue: nothing to do.
	if !migrateQueueIssue(lister, "o/r", 0, false, noScopeWrite, &queue.RepoState{}) || lister.calls != 1 {
		t.Fatal("repos without queue_issue are untouched")
	}
}

func TestMigrateQueueIssueTransientFailureRetries(t *testing.T) {
	withRunLog(t)
	lister := &fakeQueueLister{err: errors.New("HTTP 502")}
	st := queue.RepoState{Repo: "o/r"}
	if migrateQueueIssue(lister, "o/r", 100, false, noScopeWrite, &st) || st.Imported {
		t.Fatal("transient failure must not mark the import done")
	}
	lister.err = vcs.ErrNotSupported
	if !migrateQueueIssue(lister, "o/r", 100, false, noScopeWrite, &st) || !st.Imported {
		t.Fatal("unsupported platform has nothing to preserve")
	}
}

func TestRunOnceQueueIssueIgnoredAfterImport(t *testing.T) {
	logs := withRunLog(t)
	queueConfig(t, 100)
	queueVCS(t,
		[]map[string]any{queueParent(100, 2), targetedIssue(9), targetedIssue(7), targetedIssue(5)},
		map[int][]int{100: {9, 7}}, nil, nil)
	got := runQueueOnce(t, 0)
	// Ranked 9,7 first; max_in_flight 2 -> only those two run, 5 waits.
	if len(got) != 2 || got[0] != 7 || got[1] != 9 {
		t.Fatalf("dispatched = %v, want [7 9]", got)
	}
	order, _ := queue.LoadRank()
	if len(order) != 2 || order[0].Number != 9 || order[1].Number != 7 {
		t.Fatalf("rank = %v", order)
	}
	if out := logs(); !strings.Contains(out, "queue/import") || !strings.Contains(out, "queue/deprecated") {
		t.Fatalf("missing import log:\n%s", out)
	}
}

func TestMigrateQueueIssueSetsLabeledScopeOrFailsClosed(t *testing.T) {
	logs := withRunLog(t)
	lister := &fakeQueueLister{subs: []vcs.Issue{{Number: 1, State: "open"}}}
	writes := 0
	st := queue.RepoState{Repo: "o/r"}
	failing := func() error { writes++; return errors.New("disk full") }
	if migrateQueueIssue(lister, "o/r", 100, false, failing, &st) {
		t.Fatal("a failed scope write must hold back implement")
	}
	if !st.Imported || st.DeprecationShown {
		t.Fatalf("import is kept, notice waits for success: %+v", st)
	}
	ok := func() error { writes++; return nil }
	if !migrateQueueIssue(lister, "o/r", 100, false, ok, &st) || writes != 2 || lister.calls != 1 {
		t.Fatalf("retry: writes=%d calls=%d", writes, lister.calls)
	}
	// An explicit queue_scope is never overwritten.
	writes = 0
	if !migrateQueueIssue(lister, "o/r", 100, true, failing, &queue.RepoState{Repo: "o/r", Imported: true}) || writes != 0 {
		t.Fatal("explicit scope must not be touched")
	}
	if !strings.Contains(logs(), "queue/scope") {
		t.Fatal("scope change must be logged")
	}
}

func TestPlanRepoQueueRankedScopeAndEnroll(t *testing.T) {
	logs := withRunLog(t)
	in := plannerInput("o/r",
		plannerIssue(1, "2026-01-01T00:00:00Z"),             // unlisted
		plannerIssue(2, "2026-01-02T00:00:00Z"),             // listed
		plannerIssue(3, "2026-01-03T00:00:00Z", "clawflow"), // enrolled on sight
	)
	in.Scope = queue.ScopeRanked
	in.MaxInFlight = 5
	in.Rank = []queue.Key{{Repo: "o/r", Number: 2}}
	p := planRepoQueue(in)
	if got := pickedNumbers(p); len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Fatalf("picked = %v, want [2 3]", got)
	}
	if len(p.Enroll) != 1 || p.Enroll[0].Number != 3 {
		t.Fatalf("enroll = %v", p.Enroll)
	}
	for _, e := range p.Result.Entries {
		if e.Key.Number == 1 {
			t.Fatal("unlisted issue must not be a candidate")
		}
	}
	if kept := applyUnifiedQueue("o/r", nil, p); kept != nil && len(kept) != 0 {
		t.Fatal("no jobs expected")
	}
	if order, _ := queue.LoadRank(); len(order) != 1 || order[0].Number != 3 {
		t.Fatalf("rank = %v (rank file starts empty in this test)", order)
	}
	if !strings.Contains(logs(), "queue/enroll") {
		t.Fatal("missing queue/enroll")
	}
	// Labeled scope ignores the rank.
	in.Scope = queue.ScopeLabeled
	if got := pickedNumbers(planRepoQueue(in)); len(got) != 1 || got[0] != 3 {
		t.Fatalf("labeled picked = %v", got)
	}
}

func TestEffectiveScopeFailsClosedForMigratedRepos(t *testing.T) {
	cases := []struct {
		repo, def string
		had       bool
		want      string
		fell      bool
	}{
		{"", "", false, "labeled", false},
		{"", "labeled", false, "labeled", false},
		{"ranked", "labeled", false, "ranked", false},
		{"bogus", "", true, "labeled", true},
		{"bogus", "", false, "labeled", true},
		{"", "bogus", true, "labeled", true},
	}
	for _, c := range cases {
		got, fell := queue.EffectiveScope(c.repo, c.def, c.had)
		if got != c.want || fell != c.fell {
			t.Errorf("%+v = %s,%v", c, got, fell)
		}
	}
}

func TestRepoSetQueueScope(t *testing.T) {
	writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{"o/gh": {Platform: "github"}}})
	run := func(args ...string) error {
		cmd := newRepoSetCmd()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		return cmd.Execute()
	}
	if err := run("o/gh", "--queue-scope", "weird"); err == nil {
		t.Fatal("unknown scope must be rejected")
	}
	if err := run("o/gh", "--queue-scope", "ranked"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.Load(); cfg.Repos["o/gh"].QueueScope != "ranked" {
		t.Fatalf("scope = %q", cfg.Repos["o/gh"].QueueScope)
	}
	if err := run("o/gh", "--queue-scope", ""); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := config.Load(); cfg.Repos["o/gh"].QueueScope != "" {
		t.Fatal("empty must clear")
	}
}

// End to end: a migrated repo becomes labeled; unlisted work is not dispatched,
// listed work is, and a clawflow-labeled issue is enrolled and dispatched.
func TestRunOnceMigratedRepoIsLabeled(t *testing.T) {
	logs := withRunLog(t)
	queueConfig(t, 100)
	queueVCS(t,
		[]map[string]any{queueParent(100, 1), targetedIssue(9), targetedIssue(5), targetedIssue(4), targetedIssue(3, "clawflow")},
		map[int][]int{100: {9}}, nil, nil)
	got := runQueueOnce(t, 0)
	if len(got) != 2 || got[0] != 3 || got[1] != 9 {
		t.Fatalf("dispatched = %v, want [3 9] (5 and 4 are unlisted)", got)
	}
	cfg, _ := config.Load()
	if cfg.Repos["o/r"].QueueScope != "labeled" {
		t.Fatalf("queue_scope = %q, want labeled", cfg.Repos["o/r"].QueueScope)
	}
	order, _ := queue.LoadRank()
	var keys []string
	for _, k := range order {
		keys = append(keys, k.String())
	}
	if strings.Join(keys, ",") != "o/r#9" {
		t.Fatalf("rank = %v", keys)
	}
	if out := logs(); !strings.Contains(out, "queue/scope") {
		t.Fatalf("missing logs:\n%s", out)
	}
}

func TestRunOnceMigrationHoldsBackWhenScopeWriteFails(t *testing.T) {
	withRunLog(t)
	queueConfig(t, 100)
	queueVCS(t,
		[]map[string]any{queueParent(100, 1), targetedIssue(9), targetedIssue(5)},
		map[int][]int{100: {9}}, nil, nil)
	// Read-only config file: loading works, saving the scope fails.
	cfgPath := filepath.Join(os.Getenv("HOME"), ".clawflow", "config", "config.yaml")
	if err := os.Chmod(cfgPath, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfgPath, 0o644) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if got := runQueueOnce(t, 0); len(got) != 0 {
		t.Fatalf("dispatched = %v, want none while the scope write fails", got)
	}
	// Writable again: the next pass retries, sets labeled and dispatches the listed item.
	if err := os.Chmod(cfgPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := runQueueOnce(t, 0); len(got) != 1 || got[0] != 9 {
		t.Fatalf("retry dispatched = %v, want [9]", got)
	}
}

func TestDefaultLabelScopeCountsWithoutDispatch(t *testing.T) {
	in := plannerInput("o/r", plannerIssue(1, "", "clawflow"), plannerIssue(2, ""), plannerIssue(3, ""))
	_, _, in.Scope = queueRepoParams(config.Repo{}, config.QueueSettings{})
	in.Issues[2].State = "closed"
	in.Rank = []queue.Key{{Repo: "o/r", Number: 2}}
	p := planRepoQueue(in)
	if len(p.Picks) != 1 || !p.Picks[1] || p.State.NotEnrolled != 1 {
		t.Fatalf("picks=%v not enrolled=%d", p.Picks, p.State.NotEnrolled)
	}
}

func TestMigrationLabelFailureRetries(t *testing.T) {
	withRunLog(t)
	queueCmdSetup(t)
	f := &fakeQueueLister{subs: []vcs.Issue{{Number: 1, State: "open"}, {Number: 2, State: "closed"}}, labelErr: errors.New("label failed")}
	st := queue.RepoState{Repo: "o/r"}
	writes := 0
	persist := func() error { writes++; return nil }
	if migrateQueueIssue(f, "o/r", 100, false, persist, &st) || st.Imported || writes != 0 || rankStrings(t) != "" {
		t.Fatal("failed enrollment must not finish migration")
	}
	f.labelErr = nil
	if !migrateQueueIssue(f, "o/r", 100, false, persist, &st) || !st.Imported || writes != 1 || rankStrings(t) != "o/r#1" {
		t.Fatal("migration did not retry")
	}
	if fmt.Sprint(f.labels) != "[1 1]" {
		t.Fatalf("labels=%v", f.labels)
	}
}
