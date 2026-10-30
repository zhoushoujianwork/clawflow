package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type repairFailureClient struct {
	repairClient
	comments            []string
	labels              int
	evidenceUnavailable bool
}

func (f *repairFailureClient) ListIssueComments(string, int) ([]string, error) {
	return f.comments, nil
}
func (f *repairFailureClient) PostIssueCommentIdempotent(_ string, _ int, body, _ string) error {
	f.comments = append(f.comments, body)
	return nil
}
func (f *repairFailureClient) AddLabel(_ string, _ int, labels ...string) error {
	f.labels++
	f.issue.Labels = append(f.issue.Labels, labels...)
	return nil
}
func (f *repairFailureClient) GetCIEvidence(string, int) (vcs.CIEvidence, error) {
	if f.evidenceUnavailable {
		return vcs.CIEvidence{}, errors.New("evidence unavailable after repair")
	}
	conclusion := "failure"
	return vcs.CIEvidence{HeadSHA: f.pr.HeadSHA, Status: vcs.CIStatusFailure, Checks: []vcs.CICheck{{Name: "Web render", Conclusion: &conclusion}}}, nil
}

func TestRepairInfrastructureThenRealFailures(t *testing.T) {
	readLog := withRunLog(t)
	pr, issue := repairFixture()
	client := &repairFailureClient{repairClient: repairClient{pr: pr, issue: issue, status: vcs.CIStatusFailure}}
	j := &runJob{repo: "o/r", client: client, op: &operator.Operator{Name: "repair-ci"}, sub: &operator.Subject{Number: issue.Number}, repair: &ciRepairContext{PR: pr}}
	reg := repairRegistry(t)
	cfg := j.repoCfg
	cfg.AutoMerge, cfg.CIRequired, cfg.LocalPath = true, true, "unused"
	old := time.Now().UTC().Add(-time.Hour)
	attempt, err := reserveRepair(snapshot.DataDir(), j.repo, pr, repairReasonCI, old)
	if err != nil {
		t.Fatal(err)
	}
	j.repair.Attempt = attempt
	events := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(events, []byte("{\"type\":\"start\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	kind, infra := classifyRepairResult(errors.New("cannot fetch"), "", events)
	if !infra {
		t.Fatal("pre-agent error consumed a repair")
	}
	task := &coordinator.Task{ID: "infra-task", Repo: j.repo, Issue: issue.Number, PR: pr.Number, Operator: "repair-ci", CreatedAt: old}
	report := &coordinatorReport{jobResult: jobResult{Status: "failed", InfraKind: kind, Error: "cannot fetch"}}
	if err := applyCoordinatorReport(context.Background(), task, j, report); err != nil {
		t.Fatal(err)
	}
	if client.labels != 0 || len(client.comments) != 0 {
		t.Fatal("infrastructure failure wrote terminal effects")
	}
	_, path := ciRepairLedgerPath(snapshot.DataDir(), j.repo, pr.Number)
	ledger, err := loadCIRepairLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	if consumed, infra := ledger.counts(); consumed != 0 || infra != 1 {
		t.Fatalf("counts=%d,%d", consumed, infra)
	}
	if len(scanCIRepairJobs(context.Background(), reg, client, j.repo, cfg, 0, nil)) != 0 {
		t.Fatal("backoff bypassed")
	}
	// Advance the existing ledger clock instead of sleeping through backoff.
	ledger.NotBefore = old
	ledger.Attempts[0].At = old
	if err := saveCIRepairLedger(filepath.Dir(path), path, ledger); err != nil {
		t.Fatal(err)
	}
	if len(scanCIRepairJobs(context.Background(), reg, client, j.repo, cfg, 0, nil)) != 1 {
		t.Fatal("CI failure did not retry next eligible round")
	}
	for n := 1; n <= 3; n++ {
		attempt, err := reserveRepair(snapshot.DataDir(), j.repo, pr, repairReasonCI, old.Add(time.Duration(n)*10*time.Minute))
		if err != nil || attempt != n {
			t.Fatalf("attempt=%d err=%v", attempt, err)
		}
		j.repair.Attempt = attempt
		task.ID = fmt.Sprintf("real-task-%d", n)
		task.CreatedAt = old.Add(time.Duration(n) * time.Minute)
		if n == 3 {
			client.evidenceUnavailable = true
		}
		report := &coordinatorReport{jobResult: jobResult{FailingCheck: "Web render", Status: "failed", Outcome: "agent-failed", Output: "repair attempted; tests still fail", Error: "could not update existing PR"}}
		if err := applyCoordinatorReport(context.Background(), task, j, report); err != nil {
			t.Fatal(err)
		}
		if n < 3 && client.labels != 0 {
			t.Fatal("premature failure label")
		}
	}
	if client.labels != 1 || len(client.comments) != 1 || !strings.Contains(client.comments[0], "Web render") || !strings.Contains(client.comments[0], "could not update existing PR") {
		t.Fatalf("labels=%d comments=%v", client.labels, client.comments)
	}
	if err := finishRepairFailure(context.Background(), j, "replay"); err != nil {
		t.Fatal(err)
	}
	if client.labels != 1 || len(client.comments) != 1 {
		t.Fatal("duplicate terminal effects")
	}
	if len(scanCIRepairJobs(context.Background(), reg, client, j.repo, cfg, 0, nil)) != 0 {
		t.Fatal("spent repair retried")
	}
	log := readLog()
	if !strings.Contains(log, "pr=11") || !strings.Contains(log, "remove agent-failed from issue #9") || !strings.Contains(log, "cannot fetch") {
		t.Fatalf("missing diagnostics: %s", log)
	}
}

func TestRepairResultWithoutOutputKeepsToolAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events")
	if err := os.WriteFile(path, []byte(`{"tool_category":"edit"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, infra := classifyRepairResult(errors.New("failed"), "", path); infra {
		t.Fatal("real edit was released")
	}
	if _, infra := classifyRepairResult(errors.New("failed"), "", path+"missing"); infra {
		t.Fatal("unknown activity was released")
	}
}

func (f *repairFailureClient) GetIssueLabels(string, int) ([]string, error) {
	return f.issue.Labels, nil
}
func (f *repairFailureClient) ListIssueCommentsDetail(string, int) ([]vcs.IssueComment, error) {
	return nil, nil
}

func TestRepairWorkdirFailureNeverRunsAgent(t *testing.T) {
	readLog := withRunLog(t)
	pr, issue := repairFixture()
	client := &repairFailureClient{repairClient: repairClient{pr: pr, issue: issue, status: vcs.CIStatusFailure}}
	original := runJobOperator
	t.Cleanup(func() { runJobOperator = original })
	runJobOperator = func(context.Context, *operator.Operator, *operator.Subject, operator.VCS, operator.RunOptions) (string, string, error) {
		t.Fatal("agent ran despite workdir setup failure")
		return "", "", nil
	}
	released := 0
	j := &runJob{repo: "o/r", client: client, sub: &operator.Subject{Number: issue.Number, State: "open"}, op: &operator.Operator{Name: "repair-ci"}, repair: &ciRepairContext{PR: pr, Attempt: 1}, hooks: &jobHooks{SkipIndex: true, ReleaseRepair: func(_ *runJob, kind string, _ time.Time) error {
		released++
		if kind != "setup" {
			t.Fatalf("kind=%s", kind)
		}
		return nil
	}}}
	_, _, failed := runOneOperator(context.Background(), j, time.Second)
	if !failed || released != 1 || j.result.InfraKind != "setup" || j.result.Error == "" || client.labels != 0 {
		t.Fatalf("result=%+v released=%d labels=%d", j.result, released, client.labels)
	}
	if !strings.Contains(readLog(), "run/ci_repair_infra") {
		t.Fatal("missing infrastructure run log")
	}
}
