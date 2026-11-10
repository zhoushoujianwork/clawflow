package decisions

import (
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
)

var now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

func fresh() pipeline.Snapshot {
	return pipeline.Snapshot{GeneratedAt: now.Add(-time.Minute).Format(time.RFC3339Nano)}
}

func healthy() *execution.StatusDoc {
	doc := execution.Summarize(now, []execution.Readiness{{State: execution.StateReady, Worker: "w1"}})
	return &doc
}

func find(t *testing.T, r Report, kind Kind) Decision {
	t.Helper()
	for _, d := range r.Decisions {
		if d.Kind == kind {
			return d
		}
	}
	t.Fatalf("no %s decision in %+v", kind, r.Decisions)
	return Decision{}
}

func TestEmptyWhenNothingNeedsAPerson(t *testing.T) {
	snap := fresh()
	snap.Repos = []pipeline.RepoBoard{{Repo: "o/r", Enabled: true, URL: "https://github.com/o/r",
		Items: []pipeline.Item{{Issue: 1, Title: "ok", State: "open", Stage: pipeline.StageQueued}}}}
	r := Build(snap, healthy(), now)
	if len(r.Decisions) != 0 || len(r.Incomplete) != 0 {
		t.Fatalf("expected empty report, got %+v", r)
	}
}

func TestEveryKindIsListedWithLinks(t *testing.T) {
	snap := fresh()
	snap.Repos = []pipeline.RepoBoard{
		{Repo: "o/gh", Enabled: true, Platform: "github", AcceptanceRequired: true, URL: "https://github.com/o/gh",
			ConfirmationIssues: []pipeline.Item{{Issue: 11, Title: "ambiguous", State: "open", Why: "Two issues contradict each other."}},
			SkippedIssues:      []pipeline.Item{{Issue: 12, Title: "parked by owner", State: "open"}},
			Items: []pipeline.Item{
				{Issue: 13, Title: "blocked review", State: "open", Stage: pipeline.StageBlocked,
					PR:         &pipeline.PRInfo{Number: 130, State: "open", URL: "https://github.com/o/gh/pull/130"},
					Acceptance: &pipeline.AcceptanceInfo{Attempts: 1, Verdict: "blocked", NextOwner: "owner", NextAction: "Decide whether to drop requirement 3.", Summary: "Requirement 3 conflicts with the design."}},
				{Issue: 14, Title: "still failing", State: "open", Stage: pipeline.StageBlocked, RepairStop: "repair exhausted: 3 total attempts",
					PR: &pipeline.PRInfo{Number: 140, State: "open", URL: "https://github.com/o/gh/pull/140", CI: "failure"}},
			}},
	}
	doc := execution.Summarize(now, []execution.Readiness{{State: execution.StateBlocked, Reason: execution.ReasonProfileMissing, Worker: "mac-1",
		Summary: "No default profile is selected in Offload.", Guidance: &execution.Guidance{Steps: []string{"Run offload config and select a default profile."}, Recheck: execution.RecheckCommand}}})
	r := Build(snap, &doc, now)

	want := map[Kind]string{
		NeedsConfirmation: "https://github.com/o/gh/issues/11",
		OwnerSkipped:      "https://github.com/o/gh/issues/12",
		AcceptanceBlocked: "https://github.com/o/gh/pull/130",
		RepairStopped:     "https://github.com/o/gh/pull/140",
		ExecutionConfig:   ConfigDocsURL,
	}
	if len(r.Decisions) != len(want) {
		t.Fatalf("decisions = %+v", r.Decisions)
	}
	for kind, url := range want {
		d := find(t, r, kind)
		if d.URL != url {
			t.Errorf("%s url = %q, want %q", kind, d.URL, url)
		}
		if d.Reason == "" || d.Action == "" || d.ID == "" {
			t.Errorf("%s lacks reason/action/id: %+v", kind, d)
		}
	}
	if d := find(t, r, NeedsConfirmation); !strings.Contains(d.Reason, "contradict") || !strings.Contains(d.Action, "agent-needs-confirmation") {
		t.Errorf("confirmation = %+v", d)
	}
	if d := find(t, r, OwnerSkipped); !strings.Contains(d.Action, "clawflow-skip") {
		t.Errorf("skip action = %q", d.Action)
	}
	if d := find(t, r, AcceptanceBlocked); !strings.Contains(d.Reason, "owner") || d.Action != "Decide whether to drop requirement 3." || d.IssueURL != "https://github.com/o/gh/issues/13" {
		t.Errorf("acceptance = %+v", d)
	}
	if d := find(t, r, ExecutionConfig); !strings.Contains(d.Action, "offload config") || !strings.Contains(d.Action, execution.RecheckCommand) {
		t.Errorf("execution = %+v", d)
	}
	// Execution setup blocks all work, so it sorts first.
	if r.Decisions[0].Kind != ExecutionConfig {
		t.Errorf("order = %v", r.Decisions)
	}
}

func TestGitLabLinks(t *testing.T) {
	root := pipeline.RepoWebURL("g/p", "gitlab", "https://git.example.com/")
	snap := fresh()
	snap.Repos = []pipeline.RepoBoard{{Repo: "g/p", Enabled: true, Platform: "gitlab", URL: root,
		ConfirmationIssues: []pipeline.Item{{Issue: 5, Title: "t"}},
		Items:              []pipeline.Item{{Issue: 6, Title: "t", State: "open", RepairStop: "repair exhausted", PR: &pipeline.PRInfo{Number: 60, State: "open"}}}}}
	r := Build(snap, healthy(), now)
	if d := find(t, r, NeedsConfirmation); d.URL != "https://git.example.com/g/p/issues/5" {
		t.Errorf("issue url = %q", d.URL)
	}
	if d := find(t, r, RepairStopped); d.URL != "https://git.example.com/g/p/merge_requests/60" {
		t.Errorf("mr url = %q", d.URL)
	}
}

func TestRepoWebURL(t *testing.T) {
	for _, tc := range []struct{ platform, base, want string }{
		{"github", "", "https://github.com/o/r"},
		{"github", "https://api.github.com", "https://github.com/o/r"},
		{"github", "https://ghe.example.com/api/v3", "https://ghe.example.com/o/r"},
		{"gitlab", "", "https://gitlab.com/o/r"},
		{"gitlab", "http://git.corp/", "http://git.corp/o/r"},
	} {
		if got := pipeline.RepoWebURL("o/r", tc.platform, tc.base); got != tc.want {
			t.Errorf("%s %q = %q, want %q", tc.platform, tc.base, got, tc.want)
		}
	}
}

func TestNotListedWhenAutomationHandlesIt(t *testing.T) {
	pr := &pipeline.PRInfo{Number: 9, State: "open"}
	snap := fresh()
	snap.Repos = []pipeline.RepoBoard{
		{Repo: "o/auto", Enabled: true, AcceptanceRequired: true, URL: "https://github.com/o/auto", Items: []pipeline.Item{
			// Blocked but handed to the implementer: repair-ci takes it.
			{Issue: 1, State: "open", PR: pr, Acceptance: &pipeline.AcceptanceInfo{Attempts: 1, Verdict: "blocked", BlockedRepairable: true, NextOwner: "implementer"}},
			// Failed (not blocked) verdicts are repaired automatically too.
			{Issue: 2, State: "open", PR: pr, Acceptance: &pipeline.AcceptanceInfo{Attempts: 1, Verdict: "failed"}},
			// Infrastructure retries are scheduled, not a decision.
			{Issue: 3, State: "open", PR: pr, Acceptance: &pipeline.AcceptanceInfo{Attempts: 1, LatestIsInfra: true}},
			// A repair that is running right now is not stopped.
			{Issue: 4, State: "open", PR: pr, RepairStop: "repair exhausted", Run: &pipeline.RunInfo{Operator: "repair-ci", Live: true}},
			// Closed PRs and closed issues are history.
			{Issue: 5, State: "open", PR: &pipeline.PRInfo{Number: 10, State: "merged"}, RepairStop: "repair exhausted"},
			{Issue: 6, State: "closed", PR: pr, RepairStop: "repair exhausted"},
		}},
		// A blocked verdict is only a decision where acceptance is required.
		{Repo: "o/noacc", Enabled: true, URL: "https://github.com/o/noacc", Items: []pipeline.Item{
			{Issue: 7, State: "open", PR: pr, Acceptance: &pipeline.AcceptanceInfo{Attempts: 1, Verdict: "blocked"}},
		}},
		// Disabled repositories do not dispatch work.
		{Repo: "o/off", Enabled: false, URL: "https://github.com/o/off", SkippedIssues: []pipeline.Item{{Issue: 8}}, ConfirmationIssues: []pipeline.Item{{Issue: 9}}},
	}
	if r := Build(snap, healthy(), now); len(r.Decisions) != 0 {
		t.Fatalf("automation-handled items listed: %+v", r.Decisions)
	}
}

func TestAuthErrorIsOneExecutionDecisionPerRepo(t *testing.T) {
	failed := func(n int, at string) pipeline.Item {
		return pipeline.Item{Issue: n, Title: "t", State: "open", Stage: pipeline.StageBlocked, Run: &pipeline.RunInfo{Operator: "implement", Status: "auth-error", StartedAt: at}}
	}
	snap := fresh()
	snap.Repos = []pipeline.RepoBoard{{Repo: "o/r", Enabled: true, URL: "https://github.com/o/r",
		Items: []pipeline.Item{failed(1, "2026-11-10T08:00:00Z"), failed(2, "2026-11-10T09:00:00Z")}}}
	r := Build(snap, healthy(), now)
	if len(r.Decisions) != 1 || r.Decisions[0].Kind != ExecutionConfig || r.Decisions[0].URL != "https://github.com/o/r/issues/2" || !strings.Contains(r.Decisions[0].Reason, "#1, #2") {
		t.Fatalf("decisions = %+v", r.Decisions)
	}
}

func TestMissingEvidenceIsReportedNotHidden(t *testing.T) {
	// No snapshot and no execution status: empty list must say it is incomplete.
	r := Build(pipeline.Snapshot{}, nil, now)
	if len(r.Decisions) != 0 || len(r.Incomplete) != 2 {
		t.Fatalf("report = %+v", r)
	}
	old := pipeline.Snapshot{GeneratedAt: now.Add(-time.Hour).Format(time.RFC3339Nano),
		Repos: []pipeline.RepoBoard{{Repo: "o/bad", Enabled: true, Error: "list open issues: boom"}, {Repo: "o/stale", Enabled: true, Stale: true}}}
	r = Build(old, healthy(), now)
	if len(r.Incomplete) != 3 {
		t.Fatalf("incomplete = %+v", r.Incomplete)
	}
}

func TestUnreportedWorkerNeedsAPerson(t *testing.T) {
	doc := execution.Summarize(now, []execution.Readiness{{State: execution.StateUnreported, Worker: "w2", Summary: "Service definition could not be read."}})
	r := Build(fresh(), &doc, now)
	d := find(t, r, ExecutionConfig)
	if !strings.Contains(d.Action, execution.RecheckCommand) || d.URL != ConfigDocsURL {
		t.Fatalf("decision = %+v", d)
	}
	// A machine that needs no local Offload is not a problem.
	doc = execution.Summarize(now, []execution.Readiness{{State: execution.StateNotRequired}})
	if r := Build(fresh(), &doc, now); len(r.Decisions) != 0 {
		t.Fatalf("not_required listed: %+v", r.Decisions)
	}
}
