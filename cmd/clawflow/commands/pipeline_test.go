package commands

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// pipelineFake serves one repository's issues, queue and PRs.
type pipelineFake struct {
	*sweepFake
	issues    []vcs.Issue
	queue     []vcs.Issue
	queueErr  error
	open      []vcs.PR
	closed    []vcs.PR
	ci        map[int]vcs.CIStatus
	ciErr     map[int]error
	prLists   int
	detailReq int
}

func (f *pipelineFake) ListOpenIssues(string) ([]vcs.Issue, error) { return f.issues, nil }
func (f *pipelineFake) ListSubIssues(string, int) ([]vcs.Issue, error) {
	return f.queue, f.queueErr
}
func (f *pipelineFake) ListRecentPRs(_, state string, _ int) ([]vcs.PR, error) {
	f.prLists++
	if state == "open" {
		return f.open, nil
	}
	return f.closed, nil
}
func (f *pipelineFake) GetCIStatus(_ string, n int) (vcs.CIStatus, error) {
	f.detailReq++
	if err := f.ciErr[n]; err != nil {
		return vcs.CIStatusNone, err
	}
	if s, ok := f.ci[n]; ok {
		return s, nil
	}
	return vcs.CIStatusSuccess, nil
}

var pipelineT0 = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func pipelineTestDeps(f *pipelineFake, repos map[string]config.Repo) pipelineDeps {
	return pipelineDeps{
		cfg:       &config.Config{Repos: repos},
		newClient: func(config.Repo) (vcs.Client, error) { return f, nil },
		now:       func() time.Time { return pipelineT0 },
		runs:      func() []snapshot.RunIndexEntry { return nil },
		events:    func() ([]pipeline.Event, error) { return nil, nil },
		locked:    func(string, int) bool { return false },
		acceptance: func(context.Context, vcs.Client, string, config.Repo, vcs.PR, time.Time) *pipeline.AcceptanceInfo {
			return nil
		},
	}
}

func newPipelineFake() *pipelineFake {
	return &pipelineFake{sweepFake: &sweepFake{}}
}

func itemFor(t *testing.T, b pipeline.RepoBoard, issue int) pipeline.Item {
	t.Helper()
	for _, it := range b.Items {
		if it.Issue == issue {
			return it
		}
	}
	t.Fatalf("no item for #%d in %+v", issue, b.Items)
	return pipeline.Item{}
}

func TestBuildPipelineOnlyAutoMergeOrQueueRepos(t *testing.T) {
	f := newPipelineFake()
	d := pipelineTestDeps(f, map[string]config.Repo{
		"o/plain":    {Enabled: true},
		"o/merge":    {Enabled: false, AutoMerge: true},
		"o/queue":    {Enabled: true, QueueIssue: 9},
		"o/both":     {AutoMerge: true, QueueIssue: 3},
		"o/disabled": {},
	})
	snap := buildPipeline(context.Background(), d)
	var names []string
	for _, r := range snap.Repos {
		names = append(names, r.Repo)
	}
	if strings.Join(names, ",") != "o/both,o/merge,o/queue" {
		t.Fatalf("repos = %v", names)
	}
	if snap.Repos[1].Enabled {
		t.Fatal("disabled auto_merge repo must still appear, marked disabled")
	}
	if snap.GeneratedAt != "2026-10-27T12:00:00Z" {
		t.Fatalf("generated_at = %q", snap.GeneratedAt)
	}
}

func TestBuildPipelineQueueAndItems(t *testing.T) {
	f := newPipelineFake()
	f.queue = []vcs.Issue{
		{Number: 20, Title: "second", State: "open"},
		{Number: 10, Title: "first", State: "open", Labels: []string{"agent-implemented"}},
		{Number: 5, Title: "old", State: "closed"},
	}
	f.issues = []vcs.Issue{
		{Number: 10, Title: "first", State: "open", Labels: []string{"agent-implemented"}},
		{Number: 20, Title: "second", State: "open"},
	}
	f.open = []vcs.PR{{Number: 31, Title: "fix: first", State: "open", HeadBranch: "fix/issue-10", HeadSHA: strings.Repeat("a", 40), URL: "https://github.com/o/r/pull/31", UpdatedAt: "2026-10-27T11:00:00Z"}}
	f.ci = map[int]vcs.CIStatus{31: vcs.CIStatusFailure}
	events := []pipeline.Event{{Time: "2026-10-27T11:59:00Z", Event: "run/queue_wait", Fields: map[string]string{"repo": "o/r", "issue": "10", "reason": "in_flight_open_pr #31"}}}
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {Enabled: true, QueueIssue: 9, CIRequired: true}})
	d.events = func() ([]pipeline.Event, error) { return events, nil }

	snap := buildPipeline(context.Background(), d)
	b := snap.Repos[0]
	if b.Queue == nil || b.Queue.Issue != 9 || len(b.Queue.Entries) != 3 {
		t.Fatalf("queue = %+v", b.Queue)
	}
	if e := b.Queue.Entries; e[0].Number != 20 || e[0].Position != 1 || e[1].Number != 10 || e[1].Position != 2 || e[2].Position != 0 {
		t.Fatalf("entries order/position: %+v", e)
	}
	if dec := b.Queue.Decision; dec == nil || dec.Action != "wait" || dec.Issue != 10 {
		t.Fatalf("decision = %+v", dec)
	}
	if b.Queue.Entries[1].Action != "wait" || b.Queue.Entries[1].Reason != "in_flight_open_pr #31" {
		t.Fatalf("entry decision = %+v", b.Queue.Entries[1])
	}
	if len(b.Items) != 2 || b.Items[0].Issue != 20 || b.Items[1].Issue != 10 {
		t.Fatalf("items must follow queue order: %+v", b.Items)
	}
	if got := itemFor(t, b, 10); got.Stage != pipeline.StageCIFailed || got.PR == nil || got.PR.Number != 31 || got.PR.CI != "failure" || got.PR.HeadSHA == "" {
		t.Fatalf("item 10 = %+v", got)
	}
	if got := itemFor(t, b, 20); got.Stage != pipeline.StageQueued || !got.InQueue || got.QueuePosition != 1 {
		t.Fatalf("item 20 = %+v", got)
	}
}

func TestBuildPipelineQueueReadFailureIsReportedNotFatal(t *testing.T) {
	f := newPipelineFake()
	f.queueErr = errors.New("HTTP 502")
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {QueueIssue: 9}})
	b := buildPipeline(context.Background(), d).Repos[0]
	if b.Queue == nil || !strings.Contains(b.Queue.Error, "502") || len(b.Queue.Entries) != 0 {
		t.Fatalf("queue = %+v", b.Queue)
	}
}

func TestBuildPipelineVCSClientFailureIsPerRepo(t *testing.T) {
	f := newPipelineFake()
	d := pipelineTestDeps(f, map[string]config.Repo{"o/a": {AutoMerge: true}, "o/b": {AutoMerge: true}})
	d.newClient = func(rc config.Repo) (vcs.Client, error) { return nil, errors.New("load credentials: boom") }
	snap := buildPipeline(context.Background(), d)
	if len(snap.Repos) != 2 || !strings.Contains(snap.Repos[0].Error, "boom") || snap.Repos[0].Items == nil {
		t.Fatalf("%+v", snap.Repos)
	}
}

func TestBuildPipelineMergedDelivery(t *testing.T) {
	f := newPipelineFake()
	f.issues = []vcs.Issue{{Number: 12, Title: "still open but merged", State: "open"}}
	f.closed = []vcs.PR{
		{Number: 40, Title: "feat: done", State: "merged", HeadBranch: "fix/issue-8", MergeCommitSHA: strings.Repeat("c", 40), MergedAt: "2026-10-27T10:00:00Z", URL: "https://github.com/o/r/pull/40"},
		{Number: 41, Title: "closed unmerged", State: "closed", HeadBranch: "fix/issue-9", ClosedAt: "2026-10-27T10:00:00Z"},
		{Number: 3, Title: "ancient", State: "merged", HeadBranch: "fix/issue-2", MergedAt: "2026-01-01T00:00:00Z"},
	}
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {AutoMerge: true}})
	b := buildPipeline(context.Background(), d).Repos[0]
	if len(b.Items) != 1 {
		t.Fatalf("items = %+v", b.Items)
	}
	it := b.Items[0]
	if it.Issue != 8 || it.Stage != pipeline.StageMerged || it.Delivery == nil || it.Delivery.MergeCommitSHA != strings.Repeat("c", 40) || it.Delivery.MergedAt != "2026-10-27T10:00:00Z" || it.Delivery.PR != 40 {
		t.Fatalf("item = %+v", it)
	}
}

func TestBuildPipelineRunsAndLocks(t *testing.T) {
	f := newPipelineFake()
	f.issues = []vcs.Issue{{Number: 1, Title: "running", State: "open"}, {Number: 2, Title: "failed", State: "open"}, {Number: 3, Title: "old run", State: "open"}, {Number: 4, Title: "reply only", State: "open"}}
	ended := pipelineT0.Add(-time.Hour)
	mk := func(issue int, op, status string, started time.Time, end *time.Time) snapshot.RunIndexEntry {
		return snapshot.RunIndexEntry{RunMeta: snapshot.RunMeta{Operator: op, Repo: "o/r", IssueNumber: issue, Status: status, StartedAt: started, EndedAt: end, IssueBody: "SECRET PROMPT BODY", Summary: "RAW MODEL OUTPUT"}}
	}
	runs := []snapshot.RunIndexEntry{
		mk(1, "implement", "running", pipelineT0.Add(-5*time.Minute), nil),
		mk(2, "implement", "failed", pipelineT0.Add(-2*time.Hour), &ended),
		mk(2, "implement", "success", pipelineT0.Add(-9*time.Hour), &ended),
		mk(3, "implement", "failed", pipelineT0.Add(-30*24*time.Hour), &ended),
		mk(4, "reply-question", "success", pipelineT0.Add(-time.Hour), &ended),
	}
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {AutoMerge: true}})
	d.runs = func() []snapshot.RunIndexEntry { return runs }
	d.locked = func(_ string, n int) bool { return n == 1 }
	b := buildPipeline(context.Background(), d).Repos[0]
	if len(b.Items) != 2 {
		t.Fatalf("items = %+v", b.Items)
	}
	if got := itemFor(t, b, 1); got.Stage != pipeline.StageImplementing || got.Run == nil || !got.Run.Live {
		t.Fatalf("item 1 = %+v", got)
	}
	if got := itemFor(t, b, 2); got.Stage != pipeline.StageFailed || got.Run.Status != "failed" {
		t.Fatalf("item 2 = %+v", got)
	}
}

func TestBuildPipelineLimitsPerPRDetailRequests(t *testing.T) {
	f := newPipelineFake()
	for i := 1; i <= pipelineMaxPRDetails+3; i++ {
		f.issues = append(f.issues, vcs.Issue{Number: i, Title: "t", State: "open"})
		f.open = append(f.open, vcs.PR{Number: 100 + i, State: "open", HeadBranch: "fix/issue-" + itoa(i)})
	}
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {AutoMerge: true, CIRequired: true}})
	b := buildPipeline(context.Background(), d).Repos[0]
	if f.detailReq != pipelineMaxPRDetails {
		t.Fatalf("CI requests = %d, want %d", f.detailReq, pipelineMaxPRDetails)
	}
	blocked := 0
	for _, it := range b.Items {
		if it.Stage == pipeline.StageBlocked && strings.Contains(it.Why, "detail limit") {
			blocked++
		}
	}
	if blocked != 3 {
		t.Fatalf("items over the limit must say so, got %d: %+v", blocked, b.Items)
	}
	if f.prLists != 2 {
		t.Fatalf("PR list requests = %d, want 2 (open+closed pages)", f.prLists)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestBuildPipelineEventsAreFilteredToTrackedRepos(t *testing.T) {
	f := newPipelineFake()
	var evs []pipeline.Event
	for i := 0; i < 60; i++ {
		evs = append(evs, pipeline.Event{Time: "t", Event: "run/automerge_sweep", Fields: map[string]string{"repo": "o/r"}})
	}
	evs = append(evs, pipeline.Event{Time: "t", Event: "run/automerge_sweep", Fields: map[string]string{"repo": "x/other"}})
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {AutoMerge: true}})
	d.events = func() ([]pipeline.Event, error) { return evs, nil }
	got := buildPipeline(context.Background(), d).Events
	if len(got) != pipeline.MaxEvents {
		t.Fatalf("events = %d", len(got))
	}
	for _, e := range got {
		if e.Fields["repo"] != "o/r" {
			t.Fatalf("leaked %v", e)
		}
	}
}

// The real acceptance reader against the harvest fixtures: an infrastructure
// attempt, then a recorded pass for the same contract.
func TestPipelineAcceptanceReadsRecordedAttempts(t *testing.T) {
	f, _, _ := harvestFixture(t)
	var offset time.Duration
	harvestClock(t, &offset)
	rc := config.Repo{Enabled: true, AutoMerge: true, AcceptanceRequired: true}
	ctx := context.Background()

	if got := pipelineAcceptance(ctx, f, "o/r", rc, f.pr, time.Now()); got == nil || got.Attempts != 0 || got.Error != "" || !got.Required {
		t.Fatalf("before any attempt: %+v", got)
	}

	restore := failProvider()
	harvest(t, "", 0)
	restore()
	got := pipelineAcceptance(ctx, f, "o/r", rc, f.pr, time.Now())
	if got == nil || got.Attempts != 1 || got.InfraAttempts != 1 || !got.LatestIsInfra || got.NextRetryAt == "" || got.Verdict != "" || got.MaxInfraRetries != harvestMaxInfra {
		t.Fatalf("after infra attempt: %+v", got)
	}
	st, why, _ := pipeline.Derive(pipeline.Evidence{IssueState: "open", AcceptanceRequired: true, Now: time.Now(), PR: &pipeline.PRInfo{Number: 11, State: "open", CI: "success"}, Acceptance: got})
	if st != pipeline.StageInfraRetry {
		t.Fatalf("stage = %s (%s)", st, why)
	}

	offset = 16 * time.Minute
	harvest(t, "", 0)
	got = pipelineAcceptance(ctx, f, "o/r", rc, f.pr, time.Now())
	if got == nil || got.Attempts != 2 || got.InfraAttempts != 1 || got.LatestIsInfra || got.Verdict != "passed" || !got.Valid {
		t.Fatalf("after pass: %+v", got)
	}
	if got.Summary != "Independent checks complete" || got.PlannerSession == "" || got.ReviewerSession == "" || got.PlannerSession == got.ReviewerSession || got.CompletedAt == "" {
		t.Fatalf("summary/sessions: %+v", got)
	}
}

func TestPipelineAcceptanceNilWhenNotRequiredAndNoStore(t *testing.T) {
	f, _, _ := harvestFixture(t)
	if got := pipelineAcceptance(context.Background(), f, "o/r", config.Repo{AutoMerge: true}, f.pr, time.Now()); got != nil {
		t.Fatalf("got %+v", got)
	}
}

func TestPipelineSummaryBoundsAndStripsMarkers(t *testing.T) {
	s := pipelineSummary("<!-- clawflow:outcome=agent-failed --> " + strings.Repeat("é", 2000))
	if strings.Contains(s, "<!--") || strings.Contains(s, "-->") {
		t.Fatalf("markers survived: %q", s[:60])
	}
	if n := len([]rune(s)); n != pipelineSummaryLimit+1 {
		t.Fatalf("len = %d", n)
	}
	if pipelineSessionID("bad id\nwith newline") != "" || pipelineSessionID("sess-123") != "sess-123" {
		t.Fatal("session id validation")
	}
}
