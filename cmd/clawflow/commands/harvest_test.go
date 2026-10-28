package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

// harvestFixture builds the acceptance fixture with the repo enabled, a run.log
// sink and a counting runAcceptanceFn that delegates to the real runner.
func harvestFixture(t *testing.T) (*acceptanceFake, func() string, *int) {
	t.Helper()
	f, _ := acceptanceFixture(t)
	setHarvestRepo(t, func(r *config.Repo) { r.Enabled = true })
	old := runLog
	lg, err := clog.Open("run")
	if err != nil {
		t.Fatalf("clog.Open: %v", err)
	}
	runLog = lg
	calls := 0
	oldFn := runAcceptanceFn
	runAcceptanceFn = func(ctx context.Context, c vcs.Client, repo string, pr int) (acceptance.Record, error) {
		calls++
		return runAcceptance(ctx, c, repo, pr)
	}
	t.Cleanup(func() {
		lg.Close()
		runLog = old
		runAcceptanceFn = oldFn
	})
	return f, func() string {
		b, err := os.ReadFile(filepath.Join(clog.LogsDir(), "run.log"))
		if err != nil {
			t.Fatalf("read run.log: %v", err)
		}
		return string(b)
	}, &calls
}

func setHarvestRepo(t *testing.T, mutate func(*config.Repo)) {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Repos["o/r"]
	mutate(&r)
	cfg.Repos["o/r"] = r
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
}

func harvest(t *testing.T, onlyRepo string, onlyIssue int) []firedKey {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return runHarvestPhase(context.Background(), cfg, onlyRepo, onlyIssue, 30*time.Minute, time.Now().Add(2*time.Hour))
}

func TestHarvestDispatchesWhenEligible(t *testing.T) {
	f, _, calls := harvestFixture(t)
	if failed := harvest(t, "", 0); len(failed) != 0 {
		t.Fatalf("unexpected failures: %v", failed)
	}
	if *calls != 1 {
		t.Fatalf("acceptance runs = %d, want 1", *calls)
	}
	// A passing verdict reaches the conditional merge with the accepted head.
	if !reflect.DeepEqual(f.expected, []string{f.pr.HeadSHA}) || len(f.merged) != 0 || len(f.comments) != 0 {
		t.Fatalf("conditional=%v legacy=%v comments=%v", f.expected, f.merged, f.comments)
	}
}

func TestHarvestNoDispatch(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		setup  func(t *testing.T, f *acceptanceFake)
		only   string
		issue  int
	}{
		{name: "evidence present", reason: "evidence_present", setup: func(t *testing.T, f *acceptanceFake) { produceAcceptance(t, f) }},
		{name: "contract construction fails", reason: "evidence_not_missing", setup: func(t *testing.T, f *acceptanceFake) {
			writeAcceptanceDevelopment(t, "running", time.Now(), nil)
		}},
		{name: "only issue", setup: func(*testing.T, *acceptanceFake) {}, issue: 42},
		{name: "other repo", setup: func(*testing.T, *acceptanceFake) {}, only: "o/other"},
		{name: "automerge off", setup: func(t *testing.T, _ *acceptanceFake) {
			setHarvestRepo(t, func(r *config.Repo) { r.AutoMerge = false })
		}},
		{name: "not enrolled", setup: func(t *testing.T, _ *acceptanceFake) {
			setHarvestRepo(t, func(r *config.Repo) { r.AcceptanceRequired = false })
		}},
		{name: "ci pending overlap disabled", reason: "ci_not_success", setup: func(t *testing.T, f *acceptanceFake) {
			f.ciStatus = vcs.CIStatusPending
			setHarvestRepo(t, func(r *config.Repo) { v := false; r.AcceptanceOverlapCI = &v })
		}},
		{name: "ci failed", reason: "ci_not_success", setup: func(_ *testing.T, f *acceptanceFake) { f.ciStatus = vcs.CIStatusFailure }},
		{name: "not mergeable", reason: "conflict", setup: func(_ *testing.T, f *acceptanceFake) {
			f.mergeability = map[int]vcs.MergeStatus{11: vcs.MergeStatusConflict}
		}},
		{name: "draft", reason: "draft", setup: func(_ *testing.T, f *acceptanceFake) {
			f.openPRs = []vcs.PR{{Number: 11, Title: "WIP: thing", Body: "Fixes #42"}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, readLog, calls := harvestFixture(t)
			tc.setup(t, f)
			if failed := harvest(t, tc.only, tc.issue); len(failed) != 0 {
				t.Fatalf("unexpected failures: %v", failed)
			}
			if *calls != 0 || len(f.expected) != 0 || len(f.comments) != 0 {
				t.Fatalf("runs=%d conditional=%v comments=%v", *calls, f.expected, f.comments)
			}
			if tc.reason != "" && !strings.Contains(readLog(), "reason="+tc.reason) {
				t.Fatalf("run.log lacks harvest_skip reason %q:\n%s", tc.reason, readLog())
			}
		})
	}
}

func TestHarvestRequiresConditionalMerger(t *testing.T) {
	f, readLog, calls := harvestFixture(t)
	// Embedding only the interface hides MergePRAtHead from the method set.
	acceptanceClientFactory = func(config.Repo) (vcs.Client, error) { return struct{ vcs.Client }{f}, nil }
	if failed := harvest(t, "", 0); len(failed) != 0 || *calls != 0 {
		t.Fatalf("failed=%v runs=%d", failed, *calls)
	}
	if !strings.Contains(readLog(), "reason=no_conditional_merge") {
		t.Fatalf("missing skip reason:\n%s", readLog())
	}
}

func TestHarvestInsufficientBudget(t *testing.T) {
	_, readLog, calls := harvestFixture(t)
	cfg, _ := config.Load()
	// 6 minutes left minus the 2-minute margin is under the 5-minute floor.
	if failed := runHarvestPhase(context.Background(), cfg, "", 0, 30*time.Minute, time.Now().Add(6*time.Minute)); len(failed) != 0 || *calls != 0 {
		t.Fatalf("failed=%v runs=%d", failed, *calls)
	}
	if !strings.Contains(readLog(), "reason=insufficient_budget") {
		t.Fatalf("missing skip reason:\n%s", readLog())
	}
}

func TestHarvestRunTimeoutClampedToWatchdog(t *testing.T) {
	harvestFixture(t)
	var got time.Duration
	runAcceptanceFn = func(ctx context.Context, _ vcs.Client, _ string, _ int) (acceptance.Record, error) {
		d, ok := ctx.Deadline()
		if !ok {
			t.Fatal("acceptance context has no deadline")
		}
		got = time.Until(d)
		return acceptance.Record{}, errors.New("stop")
	}
	cfg, _ := config.Load()
	runHarvestPhase(context.Background(), cfg, "", 0, 60*time.Minute, time.Now().Add(20*time.Minute))
	if got <= 17*time.Minute || got > 18*time.Minute {
		t.Fatalf("run timeout = %s, want about 18m (deadline minus 2m)", got)
	}
}

func TestHarvestOneRunPerRepoPerPass(t *testing.T) {
	f, _, _ := harvestFixture(t)
	f.openPRs = []vcs.PR{f.pr, {Number: 12, Title: "Second", Body: "Fixes #43"}}
	runs := 0
	runAcceptanceFn = func(context.Context, vcs.Client, string, int) (acceptance.Record, error) {
		runs++
		return acceptance.Record{}, errors.New("stop")
	}
	failed := harvest(t, "", 0)
	if runs != 1 || len(failed) != 1 {
		t.Fatalf("runs=%d failed=%v", runs, failed)
	}
}

func TestHarvestNonpassCommentsOncePerContract(t *testing.T) {
	for _, status := range []string{"failed", "blocked"} {
		t.Run(status, func(t *testing.T) {
			f, readLog, calls := harvestFixture(t)
			baseRun := acceptanceNativeRun
			acceptanceNativeRun = func(ctx context.Context, c execution.Config, req execution.Request, env []string, w io.Writer, observe func(execution.Observation)) (execution.Result, error) {
				r, err := baseRun(ctx, c, req, env, w, observe)
				if !req.ToolsDisabled {
					r.Text = `{"status":"` + status + `","summary":"Missing <!-- x --> behavior","evidence":["TestBehavior fails"],"findings":[{"kind":"requirement","summary":"missing behavior","requirement":"required behavior","evidence":"TestBehavior fails"}]}`
				}
				return r, err
			}
			if failed := harvest(t, "", 0); len(failed) != 0 {
				t.Fatalf("recorded verdict must not count as failure: %v", failed)
			}
			if len(f.comments) != 1 {
				t.Fatalf("comments after first pass = %v", f.comments)
			}
			c := f.comments[0]
			if !strings.Contains(c, status) || !strings.Contains(c, "Missing  x  behavior") || strings.Count(c, "<!--") != 1 || !strings.Contains(c, "<!-- clawflow:acceptance=") {
				t.Fatalf("unexpected comment: %q", c)
			}
			if len(f.expected) != 0 || len(f.merged) != 0 {
				t.Fatalf("non-pass merged: %v %v", f.expected, f.merged)
			}
			// Second pass: the non-pass record is the current evidence, so no new run.
			f.existingComments = append([]string(nil), f.comments...)
			if failed := harvest(t, "", 0); len(failed) != 0 {
				t.Fatalf("second pass failures: %v", failed)
			}
			if len(f.comments) != 1 || *calls != 1 {
				t.Fatalf("comments=%d runs=%d, want 1 and 1", len(f.comments), *calls)
			}
			if !strings.Contains(readLog(), "status=nonpass") {
				t.Fatalf("run.log lacks harvest_end nonpass:\n%s", readLog())
			}
		})
	}
}

func TestHarvestNotifyDedupesOnExistingMarker(t *testing.T) {
	f, _, _ := harvestFixture(t)
	rec := acceptance.Record{ContractDigest: strings.Repeat("ab", 32), Verdict: acceptance.Failed, Report: "bad"}
	f.existingComments = []string{harvestNonpassComment(rec)}
	runAcceptanceFn = func(context.Context, vcs.Client, string, int) (acceptance.Record, error) {
		return rec, recordedNonpassError{errors.New("nonpassing")}
	}
	if failed := harvest(t, "", 0); len(failed) != 0 || len(f.comments) != 0 {
		t.Fatalf("failed=%v comments=%v", failed, f.comments)
	}
}

func TestHarvestInfraErrorCountsAsFailureWithoutComment(t *testing.T) {
	f, readLog, _ := harvestFixture(t)
	runAcceptanceFn = func(context.Context, vcs.Client, string, int) (acceptance.Record, error) {
		return acceptance.Record{}, errors.New("provider unavailable")
	}
	failed := harvest(t, "", 0)
	want := []firedKey{{Repo: "o/r", IssueNumber: 42, Operator: "acceptance"}}
	if !reflect.DeepEqual(failed, want) {
		t.Fatalf("failed = %v, want %v", failed, want)
	}
	if len(f.comments) != 0 || len(f.expected) != 0 {
		t.Fatalf("comments=%v conditional=%v", f.comments, f.expected)
	}
	if !strings.Contains(readLog(), "run/harvest_end") || !strings.Contains(readLog(), "status=error") {
		t.Fatalf("run.log lacks harvest_end error:\n%s", readLog())
	}
	if err := failedRunsError(failed); err == nil || !strings.Contains(err.Error(), "o/r#42 acceptance") {
		t.Fatalf("exit error = %v", err)
	}
}

func TestHarvestRealInfraErrorIsNotAVerdict(t *testing.T) {
	f, _, _ := harvestFixture(t)
	acceptanceNativeRun = func(context.Context, execution.Config, execution.Request, []string, io.Writer, func(execution.Observation)) (execution.Result, error) {
		return execution.Result{}, errors.New("provider down")
	}
	failed := harvest(t, "", 0)
	if len(failed) != 1 || len(f.comments) != 0 {
		t.Fatalf("failed=%v comments=%v", failed, f.comments)
	}
}

// harvestClock advances harvestNow by *offset for the test's duration.
func harvestClock(t *testing.T, offset *time.Duration) {
	t.Helper()
	old := harvestNow
	harvestNow = func() time.Time { return time.Now().Add(*offset) }
	t.Cleanup(func() { harvestNow = old })
}

// failProvider makes every native acceptance call fail before any reviewer
// report, which the runner records as an infrastructure attempt.
func failProvider() (restore func()) {
	old := acceptanceNativeRun
	acceptanceNativeRun = func(context.Context, execution.Config, execution.Request, []string, io.Writer, func(execution.Observation)) (execution.Result, error) {
		return execution.Result{}, errors.New("provider down")
	}
	return func() { acceptanceNativeRun = old }
}

func TestHarvestRetriesInfrastructureAttemptAfterCooldown(t *testing.T) {
	f, readLog, calls := harvestFixture(t)
	var offset time.Duration
	harvestClock(t, &offset)
	restore := failProvider()
	if failed := harvest(t, "", 0); len(failed) != 1 || *calls != 1 {
		t.Fatalf("first pass failed=%v runs=%d", failed, *calls)
	}
	// Within the cooldown the blocked infrastructure record is not retried.
	offset = 14 * time.Minute
	if failed := harvest(t, "", 0); len(failed) != 0 || *calls != 1 {
		t.Fatalf("cooldown pass failed=%v runs=%d", failed, *calls)
	}
	if !strings.Contains(readLog(), "reason=infra_cooldown") {
		t.Fatalf("missing infra_cooldown skip:\n%s", readLog())
	}
	// After the cooldown a healthy provider gets a second attempt that can merge.
	restore()
	offset = 16 * time.Minute
	if failed := harvest(t, "", 0); len(failed) != 0 || *calls != 2 {
		t.Fatalf("retry pass failed=%v runs=%d", failed, *calls)
	}
	if !reflect.DeepEqual(f.expected, []string{f.pr.HeadSHA}) || len(f.comments) != 0 {
		t.Fatalf("conditional=%v comments=%v", f.expected, f.comments)
	}
}

func TestHarvestStopsAfterThreeInfrastructureAttempts(t *testing.T) {
	f, readLog, calls := harvestFixture(t)
	var offset time.Duration
	harvestClock(t, &offset)
	failProvider()
	for i := 1; i <= 3; i++ {
		offset = time.Duration(i) * 16 * time.Minute
		if failed := harvest(t, "", 0); len(failed) != 1 || *calls != i {
			t.Fatalf("attempt %d failed=%v runs=%d", i, failed, *calls)
		}
	}
	if len(f.comments) != 0 {
		t.Fatalf("comment before exhaustion: %v", f.comments)
	}
	// Exhaustion is reported once and is not a new run failure.
	offset = 10 * time.Hour
	for pass := 0; pass < 2; pass++ {
		if failed := harvest(t, "", 0); len(failed) != 0 || *calls != 3 {
			t.Fatalf("exhausted pass %d failed=%v runs=%d", pass, failed, *calls)
		}
		f.existingComments = append([]string(nil), f.comments...)
	}
	if len(f.comments) != 1 {
		t.Fatalf("comments = %v, want exactly one", f.comments)
	}
	c := f.comments[0]
	if !strings.Contains(c, "3 attempts") || !strings.Contains(c, "clawflow acceptance run --repo o/r --pr 11") || !strings.Contains(c, "<!-- clawflow:acceptance-infra=") {
		t.Fatalf("unexpected comment: %q", c)
	}
	if !strings.Contains(readLog(), "reason=infra_exhausted") {
		t.Fatalf("missing infra_exhausted skip:\n%s", readLog())
	}
	// The merge gate never accepts an infrastructure record.
	if attemptAutoMerge(autoMergeAttempt{ctx: context.Background(), client: f, repo: "o/r", repoCfg: config.Repo{AutoMerge: true, AcceptanceRequired: true}, prNum: 11, quiet: true}) || len(f.expected) != 0 {
		t.Fatal("infrastructure record merged")
	}
}

func TestHarvestNeverRetriesReviewerVerdict(t *testing.T) {
	f, _, calls := harvestFixture(t)
	var offset time.Duration
	harvestClock(t, &offset)
	baseRun := acceptanceNativeRun
	acceptanceNativeRun = func(ctx context.Context, c execution.Config, req execution.Request, env []string, w io.Writer, observe func(execution.Observation)) (execution.Result, error) {
		r, err := baseRun(ctx, c, req, env, w, observe)
		if !req.ToolsDisabled {
			r.Text = `{"status":"failed","summary":"Wrong","evidence":["proof"],"findings":[{"kind":"defect","summary":"broken behavior","defect":"incorrect return value","evidence":"TestBehavior fails"}]}`
		}
		return r, err
	}
	harvest(t, "", 0)
	offset = 48 * time.Hour
	if failed := harvest(t, "", 0); len(failed) != 0 || *calls != 1 || len(f.expected) != 0 {
		t.Fatalf("verdict retried: failed=%v runs=%d", failed, *calls)
	}
}

func TestHarvestDoesNotRetryGateRejectedPassReport(t *testing.T) {
	f, readLog, calls := harvestFixture(t)
	var offset time.Duration
	harvestClock(t, &offset)
	// A reviewer pass later rejected by the gate is saved Blocked but keeps its
	// evidence, which makes it a verdict rather than an infrastructure attempt.
	passed := produceAcceptance(t, f)
	rejected := passed
	rejected.AttemptID, rejected.Plan.AttemptID = "gate-rejected", "gate-rejected"
	shift := 10 * time.Minute
	rejected.StartedAt = rejected.StartedAt.Add(shift)
	rejected.Plan.StartedAt = rejected.Plan.StartedAt.Add(shift)
	rejected.Plan.CompletedAt = rejected.Plan.CompletedAt.Add(shift)
	rejected.ReviewStartedAt = rejected.ReviewStartedAt.Add(shift)
	rejected.CompletedAt = rejected.CompletedAt.Add(shift)
	rejected.Verdict, rejected.Report = acceptance.Blocked, "gate rejected the passing report"
	store, err := acceptance.OpenStore(acceptanceStorePath("o/r", 11))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRecord(rejected); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if acceptance.IsInfrastructure(rejected) {
		t.Fatal("gate-rejected record classified as infrastructure")
	}
	runsBefore := *calls
	offset = 48 * time.Hour
	if failed := harvest(t, "", 0); len(failed) != 0 || *calls != runsBefore || len(f.expected) != 0 {
		t.Fatalf("gate-rejected report retried: failed=%v runs=%d", failed, *calls-runsBefore)
	}
	if !strings.Contains(readLog(), "reason=evidence_not_missing") {
		t.Fatalf("missing evidence_not_missing skip:\n%s", readLog())
	}
}

func TestCurrentAcceptanceMissingSentinel(t *testing.T) {
	f, _ := acceptanceFixture(t)
	_, _, err := currentAcceptance(context.Background(), f, "o/r", 11)
	if !errors.Is(err, errAcceptanceMissing) || err.Error() != "no private acceptance evidence store" {
		t.Fatalf("no store: %v", err)
	}
	// Private empty store: no completed record.
	if err := os.MkdirAll(acceptanceStorePath("o/r", 11), 0700); err != nil {
		t.Fatal(err)
	}
	_, _, err = currentAcceptance(context.Background(), f, "o/r", 11)
	if !errors.Is(err, errAcceptanceMissing) || err.Error() != "no current completed acceptance evidence" {
		t.Fatalf("no record: %v", err)
	}
	// Contract-construction failures are not "missing".
	writeAcceptanceDevelopment(t, "running", time.Now(), nil)
	if _, _, err = currentAcceptance(context.Background(), f, "o/r", 11); err == nil || errors.Is(err, errAcceptanceMissing) {
		t.Fatalf("provenance error classified as missing: %v", err)
	}
}

func TestHarvestPostsPassingAdvisories(t *testing.T) {
	f, _, _ := harvestFixture(t)
	baseRun := acceptanceNativeRun
	acceptanceNativeRun = func(ctx context.Context, c execution.Config, req execution.Request, env []string, w io.Writer, observe func(execution.Observation)) (execution.Result, error) {
		r, err := baseRun(ctx, c, req, env, w, observe)
		if !req.ToolsDisabled {
			r.Text = `{"status":"failed","summary":"requirements met; plan gap only","evidence":["command: go test ./... => ok"],"findings":[{"kind":"advisory","summary":"optional multibyte boundary test"}]}`
		}
		return r, err
	}
	if failed := harvest(t, "", 0); len(failed) != 0 {
		t.Fatalf("harvest failed: %v", failed)
	}
	if len(f.comments) != 1 || !strings.Contains(f.comments[0], "advisory: optional multibyte boundary test") || !strings.Contains(f.comments[0], "passed") {
		t.Fatalf("comments=%v", f.comments)
	}
	if len(f.expected) != 1 {
		t.Fatal("advisory prevented merge gate")
	}
}

func TestHarvestOverlapsPendingCI(t *testing.T) {
	f, _, calls := harvestFixture(t)
	f.ciStatus = vcs.CIStatusPending
	if failed := harvest(t, "", 0); len(failed) != 0 || *calls != 1 {
		t.Fatalf("failures=%v calls=%d", failed, *calls)
	}
	if len(f.expected) != 0 {
		t.Fatal("merged while CI pending")
	}
	_, record, err := currentAcceptance(context.Background(), f, "o/r", 11)
	if err != nil || record.ReviewMode != acceptance.FullSuite {
		t.Fatalf("record=%+v err=%v", record, err)
	}
	f.ciStatus = vcs.CIStatusSuccess
	if failed := harvest(t, "", 0); len(failed) != 0 || *calls != 1 {
		t.Fatal("repeated acceptance")
	}
	if err := conditionalAcceptedMerge(context.Background(), f, "o/r", 11, record.Contract); err != nil {
		t.Fatal(err)
	}
	if len(f.expected) != 1 {
		t.Fatal("green CI did not permit merge")
	}
}

func TestPassingAcceptanceThenCIFailureRepairs(t *testing.T) {
	f, _, reg, cfg := repairAcceptanceFixture(t, "passed")
	f.ciStatus = vcs.CIStatusPending
	record := recordVerdict(t, f)
	f.ciStatus = vcs.CIStatusFailure
	if err := conditionalAcceptedMerge(context.Background(), f, "o/r", 11, record.Contract); err == nil || len(f.expected) != 0 {
		t.Fatal("merged despite failed CI")
	}
	jobs := scanRepair(t, reg, f, cfg, 0)
	if len(jobs) != 1 || jobs[0].repair.Acceptance != nil {
		t.Fatalf("CI repair not dispatched: %+v", jobs)
	}
	_, saved, err := currentAcceptance(context.Background(), f, "o/r", 11)
	if err != nil || saved.AttemptID != record.AttemptID {
		t.Fatal("passing evidence lost")
	}
}

func TestAcceptanceOverlapCLI(t *testing.T) {
	harvestFixture(t)
	for _, value := range []string{"off", "on"} {
		cmd := newRepoSetCmd()
		cmd.SetArgs([]string{"o/r", "--acceptance-overlap-ci", value})
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		cfg, err := config.Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Repos["o/r"].AcceptanceOverlapsCI() != (value == "on") {
			t.Fatal("overlap flag not persisted")
		}
	}
	cmd := newRepoSetCmd()
	cmd.SetArgs([]string{"o/r", "--acceptance-overlap-ci=invalid"})
	if cmd.Execute() == nil {
		t.Fatal("invalid overlap value accepted")
	}
}

type acceptanceCIFake struct{ *acceptanceFake }

func (f *acceptanceCIFake) GetCIEvidence(string, int) (vcs.CIEvidence, error) {
	return vcs.CIEvidence{HeadSHA: f.pr.HeadSHA, Status: f.ciStatus, CheckIDs: []int64{123}}, nil
}

func TestAcceptanceWiresExactCIEvidence(t *testing.T) {
	f, _ := acceptanceFixture(t)
	record, err := runAcceptance(context.Background(), &acceptanceCIFake{f}, "o/r", 11)
	if err != nil {
		t.Fatal(err)
	}
	if record.ReviewMode != acceptance.CIVerified || record.CI == nil || record.CI.HeadSHA != record.Contract.HeadSHA {
		t.Fatalf("CI evidence not wired: %+v", record)
	}
}

func TestHarvestGitLabConditionalMerge(t *testing.T) {
	f, readLog, calls := harvestFixture(t)
	var merges atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mr := map[string]any{
			"iid": 11, "title": f.pr.Title, "description": f.pr.Body, "state": "opened",
			"sha": f.pr.HeadSHA, "diff_refs": map[string]string{"base_sha": f.pr.BaseSHA},
			"source_branch": f.pr.HeadBranch, "target_branch": "main",
			"source_project_id": 1, "target_project_id": 1, "merge_status": "can_be_merged",
		}
		var result any
		switch r.Method + " " + r.URL.Path {
		case "DELETE /api/v4/projects/o/r/repository/branches/fix/issue-42":
			w.WriteHeader(http.StatusNoContent)
			return
		case "GET /api/v4/projects/o/r/merge_requests":
			result = []any{mr}
		case "GET /api/v4/projects/o/r/merge_requests/11":
			result = mr
		case "GET /api/v4/projects/o/r/issues/42":
			result = map[string]any{"iid": 42, "title": f.issue.Title, "description": f.issue.Body, "state": "opened"}
		case "GET /api/v4/projects/o/r/merge_requests/11/pipelines":
			result = []any{map[string]string{"status": "success"}}
		case "PUT /api/v4/projects/o/r/merge_requests/11/merge":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["sha"] != f.pr.HeadSHA || body["merge_when_pipeline_succeeds"] != false {
				t.Errorf("unexpected merge request: %v, err=%v", body, err)
			}
			merges.Add(1)
			result = map[string]string{"state": "merged", "merge_commit_sha": strings.Repeat("c", 40)}
		default:
			t.Errorf("unexpected GitLab request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer srv.Close()
	setHarvestRepo(t, func(r *config.Repo) { r.Platform = "gitlab"; r.BaseURL = srv.URL })
	oldGit := acceptanceGit
	acceptanceGit = func(ctx context.Context, dir string, args ...string) (string, error) {
		if args[0] == "config" {
			return srv.URL + "/o/r.git", nil
		}
		return oldGit(ctx, dir, args...)
	}
	acceptanceClientFactory = func(config.Repo) (vcs.Client, error) { return gitlab.New("test-token", srv.URL), nil }
	if failed := harvest(t, "", 0); len(failed) != 0 || *calls != 1 || merges.Load() != 1 {
		t.Fatalf("failed=%v calls=%d merges=%d log=%s", failed, *calls, merges.Load(), readLog())
	}
}
