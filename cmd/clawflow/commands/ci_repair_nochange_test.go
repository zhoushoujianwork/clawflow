package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/repairledger"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// ciNoChangeClient records comments by number. It deliberately has no CI rerun
// capability: any rerun attempt would panic through the nil embedded client.
type ciNoChangeClient struct {
	repairClient
	comments map[int][]string
	prErr    error
	posts    int
}

func newCINoChangeClient() *ciNoChangeClient {
	pr, issue := repairFixture()
	return &ciNoChangeClient{repairClient: repairClient{pr: pr, issue: issue, status: vcs.CIStatusFailure}, comments: map[int][]string{}}
}

func (f *ciNoChangeClient) ListIssueComments(_ string, n int) ([]string, error) {
	return append([]string(nil), f.comments[n]...), nil
}
func (f *ciNoChangeClient) PostPRComment(_ string, n int, body string) error {
	if f.prErr != nil {
		return f.prErr
	}
	f.posts++
	f.comments[n] = append(f.comments[n], body)
	return nil
}
func (f *ciNoChangeClient) PostIssueCommentIdempotent(_ string, n int, body, _ string) error {
	f.comments[n] = append(f.comments[n], body)
	return nil
}
func (f *ciNoChangeClient) AddLabel(_ string, _ int, labels ...string) error {
	f.issue.Labels = append(f.issue.Labels, labels...)
	return nil
}

func ciNoChangeCfg() config.Repo {
	return config.Repo{AutoMerge: true, CIRequired: true, LocalPath: "unused"}
}

// concludeCINoChange records a finished CI repair that changed nothing, as the
// runner does after validating a clean unchanged head.
func concludeCINoChange(t *testing.T, pr vcs.PR, at time.Time) {
	t.Helper()
	n, err := reserveRepair(snapshot.DataDir(), "o/r", pr, repairReasonCI, at)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := releaseCIRepair(snapshot.DataDir(), "o/r", pr, n, "no-change", at); err != nil {
		t.Fatalf("release: %v", err)
	}
}

func headSHA(c byte) string { return strings.Repeat(string(c), 40) }

func TestCIRepairNoChangeStopsRedispatchOnSameHeadAndResumesOnNewHead(t *testing.T) {
	withRunLog(t)
	reg := repairRegistry(t)
	cfg := ciNoChangeCfg()
	f := newCINoChangeClient()
	scan := func() int { return len(scanCIRepairJobs(context.Background(), reg, f, "o/r", cfg, 0, nil)) }

	if got := scan(); got != 1 {
		t.Fatalf("first scan jobs=%d", got)
	}
	concludeCINoChange(t, f.pr, time.Now().UTC().Add(-time.Hour))

	for i := 0; i < 3; i++ {
		if got := scan(); got != 0 {
			t.Fatalf("scan %d redispatched CI repair on the same head (jobs=%d)", i, got)
		}
	}
	prComments := f.comments[f.pr.Number]
	if len(prComments) != 1 || f.posts != 1 {
		t.Fatalf("want exactly one PR comment for the head, got %d: %v", len(prComments), prComments)
	}
	c := prComments[0]
	for _, want := range []string{"PR #11", f.pr.HeadSHA, "unrelated", "rerun", "person", ciRepairNoChangeMarker(f.pr)} {
		if !strings.Contains(c, want) {
			t.Fatalf("comment lacks %q:\n%s", want, c)
		}
	}
	if len(f.comments[9]) != 0 {
		t.Fatalf("notice must go to the PR, not the issue: %v", f.comments[9])
	}

	// A new head restores the normal flow and does not comment again.
	f.pr.HeadSHA = headSHA('b')
	if got := scan(); got != 1 {
		t.Fatalf("new head did not resume CI repair (jobs=%d)", got)
	}
	if f.posts != 1 {
		t.Fatalf("new head commented: %v", f.comments)
	}

	// The ledger holds no CI rerun: nothing reruns CI on the user's behalf.
	_, path := ciRepairLedgerPath(snapshot.DataDir(), "o/r", f.pr.Number)
	l, err := loadCIRepairLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range l.Attempts {
		if a.Reason == "rerun_ci" {
			t.Fatalf("CI was rerun: %+v", a)
		}
	}
}

func TestCIRepairNoChangeNoticeIsIdempotent(t *testing.T) {
	reg := repairRegistry(t)
	cfg := ciNoChangeCfg()

	// Each subtest gets its own HOME and therefore its own repair ledger.
	t.Run("failed post is retried then not repeated", func(t *testing.T) {
		withRunLog(t)
		f := newCINoChangeClient()
		concludeCINoChange(t, f.pr, time.Now().UTC().Add(-time.Hour))
		f.prErr = errors.New("api down")
		if got := len(scanCIRepairJobs(context.Background(), reg, f, "o/r", cfg, 0, nil)); got != 0 {
			t.Fatalf("jobs=%d", got)
		}
		if len(f.comments[f.pr.Number]) != 0 {
			t.Fatal("failed post recorded a comment")
		}
		f.prErr = nil
		for i := 0; i < 2; i++ {
			scanCIRepairJobs(context.Background(), reg, f, "o/r", cfg, 0, nil)
		}
		if len(f.comments[f.pr.Number]) != 1 {
			t.Fatalf("want one retried comment, got %v", f.comments[f.pr.Number])
		}
	})

	t.Run("existing marker comment is adopted without posting", func(t *testing.T) {
		withRunLog(t)
		f := newCINoChangeClient()
		f.comments[f.pr.Number] = []string{"older text\n" + ciRepairNoChangeMarker(f.pr)}
		concludeCINoChange(t, f.pr, time.Now().UTC().Add(-2*time.Hour))
		scanCIRepairJobs(context.Background(), reg, f, "o/r", cfg, 0, nil)
		if f.posts != 0 {
			t.Fatalf("duplicate comment posted: %v", f.comments)
		}
		if repairledger.CINoChangeNeedsNotice(snapshot.DataDir(), "o/r", f.pr.Number, f.pr.HeadSHA) {
			t.Fatal("adopted marker was not recorded in the ledger")
		}
	})
}

func TestCIRepairNoChangeKeepsPerPRCap(t *testing.T) {
	withRunLog(t)
	reg := repairRegistry(t)
	cfg := ciNoChangeCfg()
	f := newCINoChangeClient()
	base := time.Now().UTC().Add(-24 * time.Hour)
	// Three heads each end with a CI no-change: every one spends an attempt.
	for i, c := range []byte{'a', 'b', 'c'} {
		f.pr.HeadSHA = headSHA(c)
		concludeCINoChange(t, f.pr, base.Add(time.Duration(i)*time.Hour))
	}
	_, path := ciRepairLedgerPath(snapshot.DataDir(), "o/r", f.pr.Number)
	l, _ := loadCIRepairLedger(path)
	if consumed, infra := l.counts(); consumed != 3 || infra != 0 {
		t.Fatalf("consumed=%d infra=%d", consumed, infra)
	}
	// A fourth head is not repaired: the existing cap holds across heads.
	f.pr.HeadSHA = headSHA('d')
	if got := len(scanCIRepairJobs(context.Background(), reg, f, "o/r", cfg, 0, nil)); got != 0 {
		t.Fatalf("cap bypassed by a new head (jobs=%d)", got)
	}
	if err := checkRepairBudget(snapshot.DataDir(), "o/r", f.pr.Number, time.Now().UTC()); !errors.Is(err, errRepairExhausted) {
		t.Fatalf("budget err=%v", err)
	}
}

func TestCIRepairNoChangeBlocksExecutionRaceAndMergeConflictStillRepairs(t *testing.T) {
	withRunLog(t)
	f := newCINoChangeClient()
	j := &runJob{repo: "o/r", repoCfg: ciNoChangeCfg(), client: f, sub: &operator.Subject{Number: 9}, repair: &ciRepairContext{PR: f.pr}}
	if err := checkCIRepair(context.Background(), j); err != nil {
		t.Fatalf("no conclusion yet: %v", err)
	}
	// The conclusion lands between scan and execution (another process).
	concludeCINoChange(t, f.pr, time.Now().UTC().Add(-time.Hour))
	if err := checkCIRepair(context.Background(), j); !errors.Is(err, errCIRepairNoChange) {
		t.Fatalf("race not closed: %v", err)
	}
	// Coordinator workers have no ledger; the controller's enqueue check owns it.
	j.hooks = &jobHooks{}
	if err := checkCIRepair(context.Background(), j); err != nil {
		t.Fatalf("worker path must not read a ledger: %v", err)
	}
}

func TestCIRepairNoChangeOutcomeFlowEndToEnd(t *testing.T) {
	withRunLog(t)
	reg := repairRegistry(t)
	cfg := ciNoChangeCfg()
	f := newCINoChangeClient()
	old := acceptanceGit
	t.Cleanup(func() { acceptanceGit = old })
	acceptanceGit = func(_ context.Context, _ string, args ...string) (string, error) {
		switch args[0] {
		case "rev-parse":
			return f.pr.HeadSHA, nil
		case "ls-remote":
			return f.pr.HeadSHA + "\trefs/heads/" + f.pr.HeadBranch, nil
		case "status":
			return "", nil
		}
		return "", errors.New("unexpected git call")
	}
	jobs := scanCIRepairJobs(context.Background(), reg, f, "o/r", cfg, 0, nil)
	if len(jobs) != 1 {
		t.Fatalf("jobs=%d", len(jobs))
	}
	j := jobs[0]
	j.repair.Attempt = 0
	attempt, err := reserveRepair(snapshot.DataDir(), "o/r", j.repair.PR, j.repair.reason(), time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	j.repair.Attempt = attempt
	if j.repair.reason() != repairReasonCI {
		t.Fatalf("reason=%s", j.repair.reason())
	}
	head, err := validateRepairOutcome(context.Background(), "unused", j, "no-change", "CI failure is a flaky test unrelated to this diff; passes locally.")
	if err != nil || head != f.pr.HeadSHA {
		t.Fatalf("head=%q err=%v", head, err)
	}
	if got := len(scanCIRepairJobs(context.Background(), reg, f, "o/r", cfg, 0, nil)); got != 0 {
		t.Fatalf("redispatched after a validated no-change (jobs=%d)", got)
	}
	if len(f.comments[f.pr.Number]) != 1 {
		t.Fatalf("comments=%v", f.comments)
	}
	// Unrelated, unchanged flows: the acceptance gate never sees a CI conclusion.
	if repairledger.NoChangeAtHead(snapshot.DataDir(), "o/r", f.pr.Number, f.pr.HeadSHA) {
		t.Fatal("CI no-change gated acceptance")
	}
}

func TestFlightBlockedReasonParksCINoChange(t *testing.T) {
	withRunLog(t)
	f := newCINoChangeClient()
	cfg := ciNoChangeCfg()
	now := time.Now().UTC()
	if got := flightBlockedReason(context.Background(), f, "o/r", cfg, &f.pr, now); got != "" {
		t.Fatalf("repairable CI failure must stay in flight, got %q", got)
	}
	concludeCINoChange(t, f.pr, now.Add(-time.Hour))
	if got := flightBlockedReason(context.Background(), f, "o/r", cfg, &f.pr, now); got != mergeReasonCIFailed {
		t.Fatalf("got %q", got)
	}
	f.pr.HeadSHA = headSHA('b')
	if got := flightBlockedReason(context.Background(), f, "o/r", cfg, &f.pr, now); got != "" {
		t.Fatalf("new head must resume, got %q", got)
	}
}
