package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// pipelineFake serves one repository's issues and PRs.
type pipelineFake struct {
	*sweepFake
	issues        []vcs.Issue
	subIssueCalls int
	open          []vcs.PR
	closed        []vcs.PR
	ci            map[int]vcs.CIStatus
	ciErr         map[int]error
	prLists       int
	detailReq     int
}

func (f *pipelineFake) ListOpenIssues(string) ([]vcs.Issue, error) { return f.issues, nil }
func (f *pipelineFake) ListSubIssues(string, int) ([]vcs.Issue, error) {
	f.subIssueCalls++
	return nil, errors.New("unexpected sub-issue request")
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
		acceptance: func(context.Context, vcs.Client, string, config.Repo, vcs.PR, time.Time) (*pipeline.AcceptanceInfo, error) {
			return nil, nil
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

func TestBuildPipelineEnabledOrAutoMergeRepos(t *testing.T) {
	f := newPipelineFake()
	d := pipelineTestDeps(f, map[string]config.Repo{
		"o/plain":    {Enabled: true},
		"o/merge":    {Enabled: false, AutoMerge: true},
		"o/queue":    {Enabled: true},
		"o/both":     {AutoMerge: true},
		"o/disabled": {},
	})
	snap := buildPipeline(context.Background(), d)
	var names []string
	for _, r := range snap.Repos {
		names = append(names, r.Repo)
	}
	if strings.Join(names, ",") != "o/both,o/merge,o/plain,o/queue" {
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
	f.issues = []vcs.Issue{
		{Number: 10, Title: "first", State: "open", Labels: []string{"agent-implemented", "clawflow"}},
		{Number: 20, Title: "second", State: "open", Labels: []string{"clawflow"}},
	}
	f.open = []vcs.PR{{Number: 31, Title: "fix: first", State: "open", HeadBranch: "fix/issue-10", HeadSHA: strings.Repeat("a", 40), URL: "https://github.com/o/r/pull/31", UpdatedAt: "2026-10-27T11:00:00Z"}}
	f.ci = map[int]vcs.CIStatus{31: vcs.CIStatusFailure}
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {Enabled: true, CIRequired: true}})
	d.globalQueue = func() []queue.ListEntry {
		return []queue.ListEntry{{Repo: "o/r", Number: 20, Position: 1}, {Repo: "o/r", Number: 10, Position: 2}}
	}

	snap := buildPipeline(context.Background(), d)
	b := snap.Repos[0]
	if f.subIssueCalls != 0 {
		t.Fatal("legacy sub-issues requested")
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

	if got, err := pipelineAcceptance(ctx, f, "o/r", rc, f.pr, time.Now()); err != nil || got == nil || got.Attempts != 0 || got.Error != "" || !got.Required {
		t.Fatalf("before any attempt: %+v", got)
	}

	restore := failProvider()
	harvest(t, "", 0)
	restore()
	got, _ := pipelineAcceptance(ctx, f, "o/r", rc, f.pr, time.Now())
	if got == nil || got.Attempts != 1 || got.InfraAttempts != 1 || !got.LatestIsInfra || got.NextRetryAt == "" || got.Verdict != "" || got.MaxInfraRetries != harvestMaxInfra {
		t.Fatalf("after infra attempt: %+v", got)
	}
	st, why, _ := pipeline.Derive(pipeline.Evidence{IssueState: "open", AcceptanceRequired: true, Now: time.Now(), PR: &pipeline.PRInfo{Number: 11, State: "open", CI: "success"}, Acceptance: got})
	if st != pipeline.StageInfraRetry {
		t.Fatalf("stage = %s (%s)", st, why)
	}

	offset = 16 * time.Minute
	harvest(t, "", 0)
	got, _ = pipelineAcceptance(ctx, f, "o/r", rc, f.pr, time.Now())
	if got == nil || got.Attempts != 2 || got.InfraAttempts != 0 || got.LatestIsInfra || got.Verdict != "passed" || !got.Valid {
		t.Fatalf("after pass: %+v", got)
	}
	if got.Summary != "Independent checks complete" || got.PlannerSession == "" || got.ReviewerSession == "" || got.PlannerSession == got.ReviewerSession || got.CompletedAt == "" {
		t.Fatalf("summary/sessions: %+v", got)
	}
}

func TestPipelineAcceptanceNilWhenNotRequiredAndNoStore(t *testing.T) {
	f, _, _ := harvestFixture(t)
	if got, err := pipelineAcceptance(context.Background(), f, "o/r", config.Repo{AutoMerge: true}, f.pr, time.Now()); err != nil || got != nil {
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

func TestBuildPipelineGlobalQueue(t *testing.T) {
	f := newPipelineFake()
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {Enabled: true, AutoMerge: true}, "g/sub/p": {Enabled: true}})
	d.globalQueue = func() []queue.ListEntry {
		return []queue.ListEntry{
			{Key: "g/sub/p#4", Repo: "g/sub/p", Number: 4, Title: "gitlab item", Tier: "P0", Rank: 1, State: "eligible"},
			{Key: "o/r#2", Repo: "o/r", Number: 2, Tier: "P2", State: "parked:ci_failed"},
			{Key: "gone/repo#1", Repo: "gone/repo", Number: 1, Tier: "P2", State: "eligible"},
		}
	}
	snap := buildPipeline(context.Background(), d)
	if len(snap.GlobalQueue) != 2 || snap.GlobalQueue[0].Key != "g/sub/p#4" || snap.GlobalQueue[1].Position != 2 || snap.GlobalQueue[1].State != "parked:ci_failed" {
		t.Fatalf("global queue = %+v", snap.GlobalQueue)
	}
	raw, _ := json.Marshal(snap)
	for _, want := range []string{`"global_queue":[`, `"repos":[`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("JSON missing %s: %s", want, raw)
		}
	}
	d.globalQueue = nil
	if snap = buildPipeline(context.Background(), d); snap.GlobalQueue == nil || len(snap.GlobalQueue) != 0 {
		t.Fatalf("empty queue must serialize as [], got %#v", snap.GlobalQueue)
	}
}

func TestUnifiedQueueItemsUseLiveStateAndAttention(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newPipelineFake()
	f.issues = []vcs.Issue{{Number: 1, Title: "live", State: "open", Labels: []string{"clawflow"}}, {Number: 2, Title: "blocked", State: "open", Labels: []string{"clawflow"}}, {Number: 3, Title: "review", State: "open"}}
	f.open = []vcs.PR{{Number: 30, State: "open", HeadBranch: "fix/issue-3"}}
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {Enabled: true, MaxInFlight: 2}})
	calls := 0
	d.globalQueue = func() []queue.ListEntry {
		calls++
		return []queue.ListEntry{{Key: "o/r#2", Repo: "o/r", Number: 2, State: "blocked:dependency"}, {Key: "o/r#1", Repo: "o/r", Number: 1, State: "eligible"}}
	}
	d.locked = func(_ string, n int) bool { return n == 1 }
	snap := buildPipeline(context.Background(), d)
	b := snap.Repos[0]
	if calls != 1 || len(b.Items) != 3 || b.MaxInFlight != 2 {
		t.Fatalf("calls=%d board=%+v", calls, b)
	}
	if b.Items[0].Issue != 2 || b.Items[1].Issue != 1 {
		t.Fatalf("queue order: %+v", b.Items)
	}
	live := itemFor(t, b, 1)
	if live.Stage != pipeline.StageImplementing || snap.GlobalQueue[1].State != "in_flight" {
		t.Fatalf("stale live state: %+v %+v", live, snap.GlobalQueue)
	}
	blocked := itemFor(t, b, 2)
	if blocked.Stage != pipeline.StageBlocked || len(blocked.Attention) == 0 {
		t.Fatalf("missing blocked attention: %+v", blocked)
	}
	review := itemFor(t, b, 3)
	if !slices.Contains(review.Attention, "waiting for review (manual merge)") {
		t.Fatalf("missing manual review: %+v", review)
	}
	raw, _ := json.Marshal(b)
	for _, field := range []string{`"max_in_flight":2`, `"attention":`, `"state":"open"`} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("missing %s: %s", field, raw)
		}
	}
}

func TestHarvestStartDoesNotOverrideFailedLatestAttempt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newPipelineFake()
	f.issues = []vcs.Issue{{Number: 381, State: "open"}}
	f.open = []vcs.PR{{Number: 400, State: "open", HeadBranch: "fix/issue-381"}}
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {Enabled: true, AcceptanceRequired: true}})
	d.events = func() ([]pipeline.Event, error) {
		return []pipeline.Event{
			{Time: pipelineT0.Add(-time.Minute).Format(time.RFC3339), Event: "run/harvest_start", Fields: map[string]string{"repo": "o/r", "pr": "400"}},
			{Time: pipelineT0.Format(time.RFC3339), Event: "run/harvest_end", Fields: map[string]string{"repo": "o/r", "pr": "400", "status": "error"}},
		}, nil
	}
	for _, tc := range []struct {
		a    pipeline.AcceptanceInfo
		want pipeline.Stage
	}{
		{pipeline.AcceptanceInfo{Attempts: 1, LatestIsInfra: true, InfraAttempts: 1, MaxInfraRetries: 3}, pipeline.StageInfraRetry},
		{pipeline.AcceptanceInfo{Attempts: 3, LatestIsInfra: true, InfraAttempts: 3, MaxInfraRetries: 3}, pipeline.StageBlocked},
		{pipeline.AcceptanceInfo{Attempts: 1, Verdict: "failed"}, pipeline.StageRejected},
		{pipeline.AcceptanceInfo{Attempts: 1, Verdict: "blocked"}, pipeline.StageBlocked},
	} {
		d.acceptance = func(context.Context, vcs.Client, string, config.Repo, vcs.PR, time.Time) (*pipeline.AcceptanceInfo, error) {
			return &tc.a, nil
		}
		item := itemFor(t, buildPipeline(context.Background(), d).Repos[0], 381)
		if item.Stage != tc.want {
			t.Fatalf("got %s want %s", item.Stage, tc.want)
		}
	}
}

func TestPipelineRepairBudgetAttention(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newPipelineFake()
	f.issues = []vcs.Issue{{Number: 1, State: "open"}}
	f.open = []vcs.PR{{Number: 2, State: "open", HeadBranch: "fix/issue-1"}}
	f.ci = map[int]vcs.CIStatus{2: vcs.CIStatusFailure}
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {Enabled: true, AutoMerge: true}})
	dir, path := ciRepairLedgerPath(snapshot.DataDir(), "o/r", 2)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ledger := ciRepairLedger{Attempts: make([]ciRepairAttempt, ciRepairMaxAttempts)}
	if err := saveCIRepairLedger(dir, path, ledger); err != nil {
		t.Fatal(err)
	}
	item := itemFor(t, buildPipeline(context.Background(), d).Repos[0], 1)
	if len(item.Attention) != 1 || !strings.Contains(item.Attention[0], "attempts") {
		t.Fatalf("missing budget attention: %+v", item)
	}
	// A historical exhausted ledger is not an active incident after CI recovers.
	f.ci[2] = vcs.CIStatusSuccess
	item = itemFor(t, buildPipeline(context.Background(), d).Repos[0], 1)
	if len(item.Attention) != 0 {
		t.Fatalf("resolved repair still needs attention: %+v", item)
	}
	// Unreadable CI remains unknown, alongside the existing repair-budget symptom.
	f.ciErr = map[int]error{2: errors.New("CI unavailable")}
	item = itemFor(t, buildPipeline(context.Background(), d).Repos[0], 1)
	if item.PR.CI != "unknown" || item.Stage == pipeline.StageCIFailed || len(item.Attention) != 1 {
		t.Fatalf("unknown CI confused with failure: %+v", item)
	}
}

func TestPipelineIncludesStandaloneFailedIssues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newPipelineFake()
	f.issues = []vcs.Issue{{Number: 91, Title: "unresolved", State: "open", Labels: []string{"agent-failed"}, UpdatedAt: pipelineT0.Add(-30 * 24 * time.Hour).Format(time.RFC3339)}, {Number: 92, State: "open"}}
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {Enabled: true}})
	d.runs = func() []snapshot.RunIndexEntry {
		return []snapshot.RunIndexEntry{{RunMeta: snapshot.RunMeta{Repo: "o/r", IssueNumber: 91, Operator: "implement", Status: "failed", StartedAt: pipelineT0.Add(-30 * 24 * time.Hour)}}}
	}
	b := buildPipeline(context.Background(), d).Repos[0]
	if len(b.Items) != 1 {
		t.Fatalf("want only unresolved failed issue, got %+v", b.Items)
	}
	it := itemFor(t, b, 91)
	if it.Stage != pipeline.StageFailed || it.InQueue || !strings.Contains(strings.Join(it.Attention, " "), "agent-failed") {
		t.Fatalf("missing standalone failure: %+v", it)
	}
	// Inclusion from both the queue and labels must still produce one row.
	d.globalQueue = func() []queue.ListEntry {
		return []queue.ListEntry{{Repo: "o/r", Number: 91, Key: "o/r#91", State: "blocked:label_agent-failed"}}
	}
	if got := buildPipeline(context.Background(), d).Repos[0].Items; len(got) != 1 || !got[0].InQueue {
		t.Fatalf("duplicate or lost queue membership: %+v", got)
	}
}

// A provider that observes the same request context as the real adapters.
type pipelineSlowFake struct {
	*pipelineFake
	ctx context.Context
}

func (f *pipelineSlowFake) SetRequestContext(ctx context.Context) { f.ctx = ctx }
func (f *pipelineSlowFake) ListOpenIssues(string) ([]vcs.Issue, error) {
	<-f.ctx.Done()
	return nil, f.ctx.Err()
}

func TestPipelineTimeoutRetainsRepoSnapshot(t *testing.T) {
	f := newPipelineFake()
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {Enabled: true}})
	d.newClient = func(config.Repo) (vcs.Client, error) { return &pipelineSlowFake{pipelineFake: newPipelineFake()}, nil }
	d.previous = map[string]pipeline.RepoBoard{"o/r": {Repo: "o/r", Items: []pipeline.Item{{Issue: 7, Title: "retained"}}}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	s := buildPipeline(ctx, d)
	if len(s.Repos) != 1 || !s.Repos[0].Stale || s.Repos[0].Error == "" || s.Repos[0].Items[0].Issue != 7 {
		t.Fatalf("snapshot=%+v", s)
	}
}

func TestPipelinePassSnapshotUsesLocalEvidence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": {Enabled: true}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.WriteIssues([]snapshot.IssueEntry{{Repo: "o/r", IssueNumber: 7, IssueTitle: "local issue", State: "open", CapturedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.WritePipelinePRs("o/r", []vcs.PR{{Number: 8, HeadBranch: "fix/issue-7", HeadSHA: "new-head", State: "open"}}); err != nil {
		t.Fatal(err)
	}
	if err := writePassPipeline(); err != nil {
		t.Fatal(err)
	}
	s, err := snapshot.ReadPipeline()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Repos) != 1 || len(s.Repos[0].Items) != 1 || s.Repos[0].Items[0].PR.Number != 8 || s.GeneratedAt == "" {
		t.Fatalf("snapshot=%+v", s)
	}
}

func TestRunPassWritesPipelineOnEarlyExit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Repos: map[string]config.Repo{}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	// No enabled repositories: the end-of-pass publication still runs.
	_ = runOnce(context.Background(), "", 0, time.Second)
	if _, err := snapshot.ReadPipeline(); err != nil {
		t.Fatal(err)
	}
}

type pipelineCountingFake struct {
	*pipelineFake
	active, maximum *atomic.Int32
}

func (f *pipelineCountingFake) ListOpenIssues(string) ([]vcs.Issue, error) {
	n := f.active.Add(1)
	for old := f.maximum.Load(); n > old; old = f.maximum.Load() {
		if f.maximum.CompareAndSwap(old, n) {
			break
		}
	}
	defer f.active.Add(-1)
	time.Sleep(20 * time.Millisecond)
	return nil, nil
}
func TestPipelineRefreshConcurrencyBound(t *testing.T) {
	repos := map[string]config.Repo{}
	for i := 0; i < 12; i++ {
		repos["o/r"+strconv.Itoa(i)] = config.Repo{Enabled: true}
	}
	d := pipelineTestDeps(newPipelineFake(), repos)
	var active, maximum atomic.Int32
	d.parallel = 4
	d.newClient = func(config.Repo) (vcs.Client, error) {
		return &pipelineCountingFake{pipelineFake: newPipelineFake(), active: &active, maximum: &maximum}, nil
	}
	s := buildPipeline(context.Background(), d)
	if len(s.Repos) != 12 || maximum.Load() > 4 || maximum.Load() < 2 {
		t.Fatalf("repos=%d concurrency=%d", len(s.Repos), maximum.Load())
	}
}

func TestPipelineSnapshotDoesNotReuseCIAtChangedHead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := snapshot.WritePipelinePRs("o/r", []vcs.PR{{Number: 8, HeadSHA: "new"}}); err != nil {
		t.Fatal(err)
	}
	c := &pipelineLocalClient{offline: true, board: pipeline.RepoBoard{Items: []pipeline.Item{{PR: &pipeline.PRInfo{Number: 8, HeadSHA: "old", CI: "success", Mergeable: "clean"}}}}}
	ci, err := c.GetCIStatus("o/r", 8)
	if err != nil || ci != "unknown" {
		t.Fatalf("ci=%s err=%v", ci, err)
	}
	merge, err := c.GetPRMergeability("o/r", 8)
	if err != nil || merge != "unknown" {
		t.Fatalf("merge=%s err=%v", merge, err)
	}
}

func TestPipelinePassMissingIssuesKeepsSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": {Enabled: true}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	old := pipeline.Snapshot{GeneratedAt: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), Repos: []pipeline.RepoBoard{{Repo: "o/r", Items: []pipeline.Item{{Issue: 7, Title: "retained"}}}}}
	if err := snapshot.WritePipeline(old); err != nil {
		t.Fatal(err)
	}
	if err := writePassPipeline(); err != nil {
		t.Fatal(err)
	}
	fresh, err := snapshot.ReadPipeline()
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Repos) != 1 || !fresh.Repos[0].Stale || fresh.Repos[0].Error == "" || len(fresh.Repos[0].Items) != 1 {
		t.Fatalf("snapshot=%+v", fresh)
	}
	// A delayed older writer must not replace the more recent pass.
	if err := snapshot.WritePipeline(old); err != nil {
		t.Fatal(err)
	}
	latest, err := snapshot.ReadPipeline()
	if err != nil || latest.GeneratedAt != fresh.GeneratedAt {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
}

type pipelineMergeErrorFake struct{ *pipelineFake }

func (f *pipelineMergeErrorFake) GetPRMergeability(string, int) (vcs.MergeStatus, error) {
	return vcs.MergeStatus("unknown"), errors.New("provider unavailable")
}
func TestPipelineMergeabilityFailureMarksRepoStale(t *testing.T) {
	f := newPipelineFake()
	f.issues = []vcs.Issue{{Number: 7, State: "open"}}
	f.open = []vcs.PR{{Number: 8, State: "open", HeadBranch: "fix/issue-7"}}
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {Enabled: true}})
	d.newClient = func(config.Repo) (vcs.Client, error) { return &pipelineMergeErrorFake{f}, nil }
	s := buildPipeline(context.Background(), d)
	if !s.Repos[0].Stale || s.Repos[0].Error == "" {
		t.Fatalf("repo=%+v", s.Repos[0])
	}
}

func TestPipelinePassReadsNewAcceptanceRecord(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": {Enabled: true, AcceptanceRequired: true}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.WriteIssues([]snapshot.IssueEntry{{Repo: "o/r", IssueNumber: 7, State: "open", CapturedAt: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.WritePipelinePRs("o/r", []vcs.PR{{Number: 8, HeadBranch: "fix/issue-7", HeadSHA: "head", State: "open"}}); err != nil {
		t.Fatal(err)
	}
	// First publish before any acceptance attempt exists.
	if err := writePassPipeline(); err != nil {
		t.Fatal(err)
	}
	c := acceptance.Contract{Version: acceptance.Version, TaskID: "o/r:issue:7", Generation: 1, Repository: "o/r", PRNumber: 8, HeadSHA: "head", BaseSHA: "base", PolicyDigest: "policy", Requirements: "requirement", Development: acceptance.Identity{SessionID: "dev", RunID: "run"}}
	digest, err := c.Digest()
	if err != nil {
		t.Fatal(err)
	}
	store, err := acceptance.OpenStore(acceptanceStorePath("o/r", 8))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	record := acceptance.Record{Version: acceptance.Version, Contract: c, ContractDigest: digest, AttemptID: "review-1", Role: "acceptance", Verdict: acceptance.Failed, Report: "new result", Evidence: []string{"test failed"}, Completed: true, StartedAt: now.Add(-time.Minute), CompletedAt: now}
	if err := store.SaveRecord(record); err != nil {
		t.Fatal(err)
	}
	if err := writePassPipeline(); err != nil {
		t.Fatal(err)
	}
	s, err := snapshot.ReadPipeline()
	if err != nil {
		t.Fatal(err)
	}
	info := s.Repos[0].Items[0].Acceptance
	if info == nil || info.Verdict != "failed" || info.Attempts != 1 || info.Summary != "new result" {
		t.Fatalf("pass did not read new acceptance record: %+v", info)
	}
	if info.Valid {
		t.Fatal("offline evidence must not claim current contract validation")
	}
	// A subsequent infrastructure attempt supersedes the displayed verdict.
	record.AttemptID = "review-2"
	record.StartedAt = now.Add(time.Minute)
	record.CompletedAt = now.Add(2 * time.Minute)
	record.Evidence = nil
	record.Verdict = acceptance.Blocked
	record.Report = "reviewer unavailable"
	if err := store.SaveRecord(record); err != nil {
		t.Fatal(err)
	}
	if err := writePassPipeline(); err != nil {
		t.Fatal(err)
	}
	s, err = snapshot.ReadPipeline()
	if err != nil {
		t.Fatal(err)
	}
	info = s.Repos[0].Items[0].Acceptance
	if info == nil || info.Verdict != "" || info.Attempts != 2 || info.InfraAttempts != 1 || !info.LatestIsInfra || info.NextRetryAt == "" {
		t.Fatalf("new infrastructure result absent: %+v", info)
	}
}

type pipelineMissingIssueFake struct {
	*pipelineFake
	issue      *vcs.Issue
	issueErr   error
	issueReads int
}

func (f *pipelineMissingIssueFake) GetIssue(string, int) (*vcs.Issue, error) {
	f.issueReads++
	return f.issue, f.issueErr
}

func TestPipelinePartialIssueSnapshotKeepsActivePR(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &pipelineMissingIssueFake{pipelineFake: newPipelineFake(), issue: &vcs.Issue{Number: 7, Title: "older open issue", State: "open"}}
	f.open = []vcs.PR{{Number: 8, HeadBranch: "fix/issue-7", State: "open"}}
	var issues []snapshot.IssueEntry
	for n := 100; n < 200; n++ {
		issues = append(issues, snapshot.IssueEntry{Repo: "o/r", IssueNumber: n, State: "closed"})
	}
	d := pipelineTestDeps(f.pipelineFake, map[string]config.Repo{"o/r": {Enabled: true}})
	d.newClient = func(config.Repo) (vcs.Client, error) { return &pipelineLocalClient{Client: f, issues: issues}, nil }
	s := buildPipeline(context.Background(), d)
	if len(s.Repos[0].Items) != 1 || s.Repos[0].Items[0].Issue != 7 || s.Repos[0].Items[0].PR.Number != 8 {
		t.Fatalf("active PR lost: %+v", s.Repos[0])
	}
	if f.issueReads != 1 {
		t.Fatalf("missing issue reads=%d", f.issueReads)
	}
	if err := snapshot.WriteIssues(issues); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.WritePipeline(s); err != nil {
		t.Fatal(err)
	}
	offline := buildPipeline(context.Background(), pipelineSnapshotDeps(d, true))
	if len(offline.Repos[0].Items) != 1 || offline.Repos[0].Items[0].PR.Number != 8 || f.issueReads != 1 {
		t.Fatalf("offline pass lost recovered PR or read VCS: %+v reads=%d", offline, f.issueReads)
	}
}

func TestPipelineMissingPRIssueReadResults(t *testing.T) {
	for _, tc := range []struct {
		name  string
		issue *vcs.Issue
		err   error
		stale bool
	}{
		{name: "closed", issue: &vcs.Issue{Number: 7, State: "closed"}},
		{name: "not found"},
		{name: "provider error", err: errors.New("provider unavailable"), stale: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			f := &pipelineMissingIssueFake{pipelineFake: newPipelineFake(), issue: tc.issue, issueErr: tc.err}
			f.open = []vcs.PR{{Number: 8, HeadBranch: "fix/issue-7", State: "open"}}
			d := pipelineTestDeps(f.pipelineFake, map[string]config.Repo{"o/r": {Enabled: true}})
			d.previous = map[string]pipeline.RepoBoard{"o/r": {Repo: "o/r", Items: []pipeline.Item{{Issue: 7, Title: "retained"}}}}
			d.newClient = func(config.Repo) (vcs.Client, error) {
				return &pipelineLocalClient{Client: f, issues: []snapshot.IssueEntry{{Repo: "o/r", IssueNumber: 100, State: "closed"}}}, nil
			}
			s := buildPipeline(context.Background(), d)
			b := s.Repos[0]
			if b.Stale != tc.stale || (tc.stale && (b.Error == "" || len(b.Items) != 1)) || (!tc.stale && len(b.Items) != 0) {
				t.Fatalf("repo=%+v", b)
			}
		})
	}
}

func TestPipelineMissingPRIssueReadBudget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &pipelineMissingIssueFake{pipelineFake: newPipelineFake(), issueErr: errors.New("unavailable")}
	for n := 1; n <= pipelineMaxPRDetails+5; n++ {
		f.open = append(f.open, vcs.PR{Number: n, HeadBranch: "fix/issue-" + strconv.Itoa(n), State: "open"})
	}
	d := pipelineTestDeps(f.pipelineFake, map[string]config.Repo{"o/r": {Enabled: true}})
	d.newClient = func(config.Repo) (vcs.Client, error) { return &pipelineLocalClient{Client: f}, nil }
	s := buildPipeline(context.Background(), d)
	if f.issueReads != pipelineMaxPRDetails || !s.Repos[0].Stale || s.Repos[0].Error == "" {
		t.Fatalf("reads=%d repo=%+v", f.issueReads, s.Repos[0])
	}
}

func TestPipelineKnownClosedSnapshotDoesNotFetchIssue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &pipelineMissingIssueFake{pipelineFake: newPipelineFake()}
	f.open = []vcs.PR{{Number: 8, HeadBranch: "fix/issue-7", State: "open"}}
	d := pipelineTestDeps(f.pipelineFake, map[string]config.Repo{"o/r": {Enabled: true}})
	d.newClient = func(config.Repo) (vcs.Client, error) {
		return &pipelineLocalClient{Client: f, issues: []snapshot.IssueEntry{{Repo: "o/r", IssueNumber: 7, State: "closed"}}}, nil
	}
	s := buildPipeline(context.Background(), d)
	if f.issueReads != 0 || len(s.Repos[0].Items) != 0 {
		t.Fatalf("reads=%d repo=%+v", f.issueReads, s.Repos[0])
	}
}

type pipelineMissingIssueSlowFake struct {
	*pipelineMissingIssueFake
	ctx context.Context
}

func (f *pipelineMissingIssueSlowFake) SetRequestContext(ctx context.Context) { f.ctx = ctx }
func (f *pipelineMissingIssueSlowFake) GetIssue(string, int) (*vcs.Issue, error) {
	<-f.ctx.Done()
	return nil, f.ctx.Err()
}
func TestPipelineMissingPRIssueTimeout(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &pipelineMissingIssueSlowFake{pipelineMissingIssueFake: &pipelineMissingIssueFake{pipelineFake: newPipelineFake()}}
	f.open = []vcs.PR{{Number: 8, HeadBranch: "fix/issue-7", State: "open"}}
	d := pipelineTestDeps(f.pipelineFake, map[string]config.Repo{"o/r": {Enabled: true}})
	d.previous = map[string]pipeline.RepoBoard{"o/r": {Repo: "o/r", Items: []pipeline.Item{{Issue: 7, Title: "retained"}}}}
	d.newClient = func(config.Repo) (vcs.Client, error) { return &pipelineLocalClient{Client: f}, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	s := buildPipeline(ctx, d)
	if !s.Repos[0].Stale || s.Repos[0].Error == "" || len(s.Repos[0].Items) != 1 {
		t.Fatalf("repo=%+v", s.Repos[0])
	}
}

// A failed acceptance read for one repository keeps that repository's previous
// entry as stale; other repositories update normally.
func TestPipelineAcceptanceReadFailureKeepsPreviousRepoEntry(t *testing.T) {
	f := newPipelineFake()
	f.issues = []vcs.Issue{{Number: 7, Title: "fresh", State: "open"}}
	f.open = []vcs.PR{{Number: 8, State: "open", HeadBranch: "fix/issue-7"}}
	d := pipelineTestDeps(f, map[string]config.Repo{"o/a": {Enabled: true}, "o/b": {Enabled: true}})
	d.acceptance = func(_ context.Context, _ vcs.Client, repo string, _ config.Repo, _ vcs.PR, _ time.Time) (*pipeline.AcceptanceInfo, error) {
		if repo == "o/a" {
			return &pipeline.AcceptanceInfo{Error: "acceptance PR unavailable"}, acceptanceReadError{"acceptance PR unavailable"}
		}
		return nil, nil
	}
	d.previous = map[string]pipeline.RepoBoard{
		"o/a": {Repo: "o/a", Items: []pipeline.Item{{Issue: 99, Title: "previous"}}},
		"o/b": {Repo: "o/b", Items: []pipeline.Item{{Issue: 99, Title: "previous"}}},
	}
	s := buildPipeline(context.Background(), d)
	if len(s.Repos) != 2 {
		t.Fatalf("repos=%+v", s.Repos)
	}
	a, b := s.Repos[0], s.Repos[1]
	if a.Repo != "o/a" || !a.Stale || !strings.Contains(a.Error, "acceptance") || len(a.Items) != 1 || a.Items[0].Issue != 99 {
		t.Fatalf("a=%+v", a)
	}
	if b.Stale || b.Error != "" || b.Items[0].Issue != 7 {
		t.Fatalf("b=%+v", b)
	}
}

func TestPipelineDecidedSnapshotEvidence(t *testing.T) {
	f := newPipelineFake()
	f.issues = []vcs.Issue{{Number: 20, State: "open", Labels: []string{"agent-failed"}, Body: "## Decided\nShip", UpdatedAt: pipelineT0.Add(-time.Hour).Format(time.RFC3339)}}
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {Enabled: true}})
	live := buildPipeline(context.Background(), d)
	item := itemFor(t, live.Repos[0], 20)
	if item.HasDecided == nil || !*item.HasDecided {
		t.Fatalf("live decision lost: %+v", item)
	}
	local := &pipelineLocalClient{Client: f, issues: []snapshot.IssueEntry{{Repo: "o/r", IssueNumber: 20, State: "open", Labels: []string{"agent-failed"}, HasDecided: pipeline.DecidedSection("## Decided"), UpdatedAt: f.issues[0].UpdatedAt, CapturedAt: pipelineT0}}}
	d.newClient = func(config.Repo) (vcs.Client, error) { return local, nil }
	persisted := buildPipeline(context.Background(), d)
	item = itemFor(t, persisted.Repos[0], 20)
	if item.HasDecided == nil || !*item.HasDecided || item.UpdatedAt != f.issues[0].UpdatedAt {
		t.Fatalf("snapshot evidence lost or refresh mistaken for change: %+v", item)
	}
	local.issues[0].HasDecided = nil
	legacy := buildPipeline(context.Background(), d)
	if itemFor(t, legacy.Repos[0], 20).HasDecided != nil {
		t.Fatal("legacy snapshot treated as inspected body")
	}
}

func TestPipelineIncludesSkippedIssues(t *testing.T) {
	f := newPipelineFake()
	f.issues = []vcs.Issue{{Number: 20, State: "open", Labels: []string{"clawflow-skip"}, UpdatedAt: pipelineT0.Format(time.RFC3339)}}
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {Enabled: true}})
	d.globalQueue = func() []queue.ListEntry {
		return []queue.ListEntry{{Repo: "o/r", Number: 20, State: "blocked:agent-failed"}}
	}
	snap := buildPipeline(context.Background(), d)
	if len(snap.GlobalQueue) != 0 {
		t.Fatal("stale queue retained skipped issue")
	}
	b := snap.Repos[0]
	if len(b.Items) != 0 || len(b.SkippedIssues) != 1 || b.SkippedIssues[0].Issue != 20 {
		t.Fatalf("missing skipped issue: %+v", b)
	}
}

func TestPipelineExecutionProjection(t *testing.T) {
	f := newPipelineFake()
	f.issues = []vcs.Issue{{Number: 1, Title: "implementation", State: "open"}, {Number: 10, Title: "number collision", State: "open"}, {Number: 2, Title: "stale lock", State: "open"}}
	f.open = []vcs.PR{{Number: 10, HeadBranch: "fix/issue-1", State: "open"}}
	f.ci = map[int]vcs.CIStatus{10: vcs.CIStatusPending}
	end := pipelineT0.Add(-time.Minute)
	zero := 0
	elapsed := int64(42)
	run := snapshot.RunIndexEntry{Path: "./data/runs/implement/", RunMeta: snapshot.RunMeta{
		Repo: "o/r", IssueNumber: 1, Operator: "implement", Machine: "local", Profile: "build", Model: "model-a",
		StartedAt: pipelineT0.Add(-time.Hour), EndedAt: &end, Status: "success",
		DevelopmentSession: snapshot.DevelopmentSession{Role: "operator"},
		ExecutionMetadata:  snapshot.ExecutionMetadata{OffloadRunID: "attempt-1", SessionID: "shared", ExecutionStatus: "ok", ExitCode: &zero, ElapsedMs: &elapsed},
		IssueBody:          "PRIVATE PROMPT", Summary: "PRIVATE RESULT",
	}}
	review := run
	review.Path, review.IssueNumber, review.Operator, review.OffloadRunID = "./data/runs/review/", 10, "acceptance-review", "attempt-2"
	review.DevelopmentSession.Role = "acceptance"
	review.EndedAt, review.ExecutionStatus, review.ExitCode, review.ElapsedMs = nil, "", nil, nil
	remote := review
	remote.Path, remote.Machine = "./data/runs/remote/", "remote"
	legacy := run
	legacy.Path, legacy.OffloadRunID, legacy.ExecutionStatus = "./data/runs/legacy/", "", ""
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {AutoMerge: true, CIRequired: true}})
	d.globalQueue = func() []queue.ListEntry {
		return []queue.ListEntry{{Repo: "o/r", Number: 1}, {Repo: "o/r", Number: 2}, {Repo: "o/r", Number: 10}}
	}
	replay := run
	replay.Path, replay.ExecutionStatus, replay.EndedAt = "./data/runs/replayed/", "", nil
	d.runs = func() []snapshot.RunIndexEntry {
		return []snapshot.RunIndexEntry{run, replay, run, review, remote, legacy}
	}
	d.locked = func(_ string, n int) bool { return n == 2 }
	board := buildPipeline(context.Background(), d).Repos[0]
	item := itemFor(t, board, 1)
	if item.Stage != pipeline.StageCIPending || item.Delivery != nil || len(item.Executions) != 4 {
		t.Fatalf("item = %+v", item)
	}
	var finals, unknown int
	for _, e := range item.Executions {
		if e.Status == "ok" {
			finals++
			if e.ExitCode == nil || *e.ExitCode != 0 || e.ElapsedMs == nil || *e.ElapsedMs != 42 || e.Provenance.Source != "offload_final" || e.Role != "operator" || e.Model != "model-a" {
				t.Fatalf("final = %+v", e)
			}
		} else if e.Status == "unknown" {
			unknown++
		}
	}
	if finals != 1 || unknown != 3 {
		t.Fatalf("finals=%d unknown=%d", finals, unknown)
	}
	if got := itemFor(t, board, 10); len(got.Executions) != 0 {
		t.Fatalf("PR run attached to issue with same number: %+v", got)
	}
	if got := itemFor(t, board, 2); len(got.Executions) != 0 {
		t.Fatalf("lock invented execution: %+v", got)
	}
	raw, err := json.Marshal(item.Executions)
	if err != nil || strings.Contains(string(raw), "PRIVATE") {
		t.Fatalf("projection exposed private payload: %s %v", raw, err)
	}
}
