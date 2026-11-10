package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/api"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordination"
	"github.com/zhoushoujianwork/clawflow/internal/decisions"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// decisionsT0 is the fixed clock of the board builder in these tests.
var decisionsT0 = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

// decisionsFake serves one repository to the board builder. Only the read
// methods the board uses are implemented; every other vcs.Client method hits
// the nil embedded interface and panics, so an unexpected call is loud. The
// write methods are overridden to record the call instead.
type decisionsFake struct {
	vcs.Client
	issues []vcs.Issue
	open   []vcs.PR
	ci     map[int]vcs.CIStatus
	writes []string
}

func (f *decisionsFake) ListOpenIssues(string) ([]vcs.Issue, error) { return f.issues, nil }
func (f *decisionsFake) ListRecentPRs(_, state string, _ int) ([]vcs.PR, error) {
	if state == "open" {
		return f.open, nil
	}
	return nil, nil
}
func (f *decisionsFake) GetCIStatus(_ string, n int) (vcs.CIStatus, error) {
	if s, ok := f.ci[n]; ok {
		return s, nil
	}
	return vcs.CIStatusSuccess, nil
}
func (f *decisionsFake) GetPRMergeability(string, int) (vcs.MergeStatus, error) {
	return vcs.MergeStatusClean, nil
}
func (f *decisionsFake) PostIssueComment(string, int, string) error {
	f.writes = append(f.writes, "PostIssueComment")
	return nil
}
func (f *decisionsFake) PostPRComment(string, int, string) error {
	f.writes = append(f.writes, "PostPRComment")
	return nil
}
func (f *decisionsFake) AddLabel(string, int, ...string) error {
	f.writes = append(f.writes, "AddLabel")
	return nil
}
func (f *decisionsFake) RemoveLabel(string, int, ...string) error {
	f.writes = append(f.writes, "RemoveLabel")
	return nil
}
func (f *decisionsFake) MergePR(string, int) error {
	f.writes = append(f.writes, "MergePR")
	return nil
}
func (f *decisionsFake) ClosePR(string, int) error {
	f.writes = append(f.writes, "ClosePR")
	return nil
}
func (f *decisionsFake) CloseIssue(string, int) error {
	f.writes = append(f.writes, "CloseIssue")
	return nil
}

func decisionsTestDeps(f *decisionsFake, repos map[string]config.Repo) pipelineDeps {
	return pipelineDeps{
		cfg:       &config.Config{Repos: repos},
		newClient: func(config.Repo) (vcs.Client, error) { return f, nil },
		now:       func() time.Time { return decisionsT0 },
		runs:      func() []snapshot.RunIndexEntry { return nil },
		events:    func() ([]pipeline.Event, error) { return nil, nil },
		locked:    func(string, int) bool { return false },
		acceptance: func(context.Context, vcs.Client, string, config.Repo, vcs.PR, time.Time) (*pipeline.AcceptanceInfo, error) {
			return nil, nil
		},
	}
}

func decisionKinds(r decisions.Report) map[decisions.Kind]decisions.Decision {
	out := map[decisions.Kind]decisions.Decision{}
	for _, d := range r.Decisions {
		out[d.Kind] = d
	}
	return out
}

// One constructed item of each kind flows from VCS/local evidence through the
// real board builder into the list, with the right links.
func TestDecisionsListEveryKindFromBoard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &decisionsFake{}
	head := strings.Repeat("a", 40)
	f.issues = []vcs.Issue{
		{Number: 10, Title: "needs an answer", State: "open", Labels: []string{coordination.ConfirmationLabel}},
		{Number: 20, Title: "owner parked", State: "open", Labels: []string{"clawflow-skip"}},
		{Number: 30, Title: "blocked review", State: "open"},
		{Number: 40, Title: "repair spent", State: "open"},
	}
	f.open = []vcs.PR{
		{Number: 31, Title: "fix", State: "open", HeadBranch: "fix/issue-30", HeadSHA: head, URL: "https://github.com/o/r/pull/31"},
		{Number: 41, Title: "fix", State: "open", HeadBranch: "fix/issue-40", HeadSHA: head, URL: "https://github.com/o/r/pull/41"},
	}
	f.ci = map[int]vcs.CIStatus{41: vcs.CIStatusFailure}

	// Why dispatch coordination asked for confirmation.
	path := coordinationPath("o/r", 10)
	if err := saveCoordination(path, coordination.Record{Version: coordination.Version, Applied: true,
		Decision: coordination.Decision{Outcome: "needs-confirmation", Reason: "Duplicate of #5 or a new scope?", NextAction: "x", NextOwner: "maintainer"}}); err != nil {
		t.Fatal(err)
	}
	// Spent repair budget on PR 41.
	dir, lpath := ciRepairLedgerPath(snapshot.DataDir(), "o/r", 41)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveCIRepairLedger(dir, lpath, ciRepairLedger{Attempts: make([]ciRepairAttempt, ciRepairMaxAttempts)}); err != nil {
		t.Fatal(err)
	}

	d := decisionsTestDeps(f, map[string]config.Repo{"o/r": {Enabled: true, AcceptanceRequired: true, CIRequired: true}})
	d.acceptance = func(_ context.Context, _ vcs.Client, _ string, _ config.Repo, pr vcs.PR, _ time.Time) (*pipeline.AcceptanceInfo, error) {
		if pr.Number != 31 {
			return nil, nil
		}
		// Reviewer blocked it and handed it to the owner, not the implementer.
		return &pipeline.AcceptanceInfo{Required: true, Attempts: 1, Verdict: "blocked", NextOwner: "owner", NextAction: "Decide which requirement wins.", Summary: "Requirements conflict."}, nil
	}
	d.globalQueue = nil
	snap := buildPipeline(context.Background(), d)

	status := execution.Summarize(decisionsT0, []execution.Readiness{{State: execution.StateBlocked, Reason: execution.ReasonMissing, Worker: "mac",
		Summary: "Offload executable not found.", Guidance: &execution.Guidance{Steps: []string{"Install Offload."}, Recheck: execution.RecheckCommand}}})
	report := decisions.Build(snap, &status, decisionsT0)
	got := decisionKinds(report)

	for kind, link := range map[decisions.Kind]string{
		decisions.NeedsConfirmation: "https://github.com/o/r/issues/10",
		decisions.OwnerSkipped:      "https://github.com/o/r/issues/20",
		decisions.AcceptanceBlocked: "https://github.com/o/r/pull/31",
		decisions.RepairStopped:     "https://github.com/o/r/pull/41",
		decisions.ExecutionConfig:   decisions.ConfigDocsURL,
	} {
		item, ok := got[kind]
		if !ok {
			t.Fatalf("missing %s in %+v", kind, report.Decisions)
		}
		if item.URL != link {
			t.Errorf("%s link = %q, want %q", kind, item.URL, link)
		}
		if item.Reason == "" || item.Action == "" {
			t.Errorf("%s lacks reason or action: %+v", kind, item)
		}
	}
	if !strings.Contains(got[decisions.NeedsConfirmation].Reason, "Duplicate of #5") {
		t.Errorf("coordination reason not carried: %+v", got[decisions.NeedsConfirmation])
	}
	if !strings.Contains(got[decisions.AcceptanceBlocked].Action, "Decide which requirement wins") {
		t.Errorf("reviewer action not carried: %+v", got[decisions.AcceptanceBlocked])
	}
	if len(report.Decisions) != 5 {
		t.Errorf("decisions = %+v", report.Decisions)
	}
}

// Resolving through the existing route empties the list on the next snapshot.
func TestDecisionsClearAfterExistingRouteResolves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &decisionsFake{}
	f.issues = []vcs.Issue{
		{Number: 10, Title: "asked", State: "open", Labels: []string{coordination.ConfirmationLabel}},
		{Number: 20, Title: "parked", State: "open", Labels: []string{"clawflow-skip"}},
	}
	d := decisionsTestDeps(f, map[string]config.Repo{"o/r": {Enabled: true}})
	healthy := execution.Summarize(decisionsT0, []execution.Readiness{{State: execution.StateReady, Worker: "w"}})
	if n := len(decisions.Build(buildPipeline(context.Background(), d), &healthy, decisionsT0).Decisions); n != 2 {
		t.Fatalf("before = %d", n)
	}
	// The person removes both labels on the provider; nothing else is touched.
	f.issues[0].Labels, f.issues[1].Labels = nil, nil
	report := decisions.Build(buildPipeline(context.Background(), d), &healthy, decisionsT0)
	if len(report.Decisions) != 0 || len(report.Incomplete) != 0 {
		t.Fatalf("after = %+v", report)
	}
}

func TestDecisionsAreReadOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := &decisionsFake{}
	f.issues = []vcs.Issue{{Number: 10, Title: "asked", State: "open", Labels: []string{coordination.ConfirmationLabel}}}
	d := decisionsTestDeps(f, map[string]config.Repo{"o/r": {Enabled: true}})
	buildPipeline(context.Background(), d)
	if len(f.writes) != 0 {
		t.Fatalf("board build wrote to the provider: %v", f.writes)
	}
}

func TestDecisionsCommandOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_OFFLOAD_COMMAND", filepath.Join(t.TempDir(), "missing"))
	snap := pipeline.Snapshot{GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Repos: []pipeline.RepoBoard{{
		Repo: "o/r", Enabled: true, URL: "https://github.com/o/r",
		SkippedIssues: []pipeline.Item{{Issue: 20, Title: "parked", State: "open"}}}}}
	if err := snapshot.WritePipeline(snap); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := NewDecisionsCmd()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var report decisions.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if d := decisionKinds(report)[decisions.OwnerSkipped]; d.URL != "https://github.com/o/r/issues/20" || d.Repo != "o/r" || d.Issue != 20 {
		t.Fatalf("skipped = %+v", d)
	}

	out.Reset()
	cmd = NewDecisionsCmd()
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"need your decision", "[owner_skipped] o/r#20", "Why:", "Do:", "Link: https://github.com/o/r/issues/20"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("text output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestDecisionsCommandEmptyAndIncomplete(t *testing.T) {
	var out bytes.Buffer
	printDecisions(&out, decisions.Report{Decisions: []decisions.Decision{}})
	if strings.TrimSpace(out.String()) != "Nothing needs your decision." {
		t.Fatalf("empty output = %q", out.String())
	}
	out.Reset()
	printDecisions(&out, decisions.Report{Decisions: []decisions.Decision{}, Incomplete: []decisions.Gap{{Source: "o/r", Reason: "boom"}}})
	if !strings.Contains(out.String(), "Nothing needs your decision.") || !strings.Contains(out.String(), "o/r: boom") {
		t.Fatalf("incomplete output = %q", out.String())
	}
}

func TestDecisionsAPIEndpoint(t *testing.T) {
	old := api.DecisionsSource
	t.Cleanup(func() { api.DecisionsSource = old })
	api.DecisionsSource = func(context.Context) decisions.Report { return decisions.Report{} }
	rec := httptest.NewRecorder()
	api.HandleDecisions(rec, httptest.NewRequest(http.MethodGet, "/api/decisions", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"decisions":[]`) {
		t.Fatalf("empty list must be [] not null: %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache-control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestDecisionsAPIRejectsWritesAndUnboundSource(t *testing.T) {
	old := api.DecisionsSource
	t.Cleanup(func() { api.DecisionsSource = old })
	api.DecisionsSource = nil
	rec := httptest.NewRecorder()
	api.HandleDecisions(rec, httptest.NewRequest(http.MethodGet, "/api/decisions", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unbound status = %d", rec.Code)
	}
	api.DecisionsSource = func(context.Context) decisions.Report { return decisions.Report{} }
	rec = httptest.NewRecorder()
	api.HandleDecisions(rec, httptest.NewRequest(http.MethodPost, "/api/decisions", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", rec.Code)
	}
}

// A board written before repository URLs were recorded still links correctly.
func TestDecisionsFillMissingRepoURLFromConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Repos: map[string]config.Repo{
		"o/gh": {Enabled: true},
		"g/p":  {Enabled: true, Platform: "gitlab", BaseURL: "https://git.example.com"},
	}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	snap := pipeline.Snapshot{Repos: []pipeline.RepoBoard{
		{Repo: "o/gh", Enabled: true, SkippedIssues: []pipeline.Item{{Issue: 1}}},
		{Repo: "g/p", Enabled: true, SkippedIssues: []pipeline.Item{{Issue: 2}}},
		{Repo: "o/known", Enabled: true, URL: "https://keep.example/o/known"},
	}}
	fillRepoURLs(&snap)
	got := []string{snap.Repos[0].URL, snap.Repos[1].URL, snap.Repos[2].URL}
	want := []string{"https://github.com/o/gh", "https://git.example.com/g/p", "https://keep.example/o/known"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("url[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	snap.GeneratedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if d := decisionKinds(decisions.Build(snap, nil, time.Now()))[decisions.OwnerSkipped]; !strings.HasPrefix(d.URL, "https://") {
		t.Errorf("decision link = %q", d.URL)
	}
}
