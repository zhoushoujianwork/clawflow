package commands

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
)

func TestImplementationPlanReusedByAcceptanceAndRepairs(t *testing.T) {
	f, rc := acceptanceFixture(t)
	base := acceptanceNativeRun
	plans, reviews := 0, 0
	acceptanceNativeRun = func(ctx context.Context, ec execution.Config, req execution.Request, env []string, out io.Writer, observer func(execution.Observation)) (execution.Result, error) {
		if req.ToolsDisabled {
			plans++
		} else {
			reviews++
		}
		return base(ctx, ec, req, env, out, observer)
	}
	j := &runJob{repo: "o/r", repoCfg: rc, client: f, op: &operator.Operator{Name: "implement"}, sub: &operator.Subject{Number: f.issue.Number, Title: f.issue.Title, Body: f.issue.Body}}
	plan, err := prepareImplementationPlan(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	if plans != 1 || reviews != 0 {
		t.Fatal("expected plan before implementation")
	}
	// Exercise the actual operator prompt assembly, including the checklist contract.
	_, _, err = operator.Run(context.Background(), j.op, j.sub, f, operator.RunOptions{
		Repo: j.repo, ResumeContext: implementationChecklist(plan),
		RunFunc: func(_ context.Context, prompt, _ string, _ time.Duration, _ io.Writer, _ string, _ ...string) (string, error) {
			for _, text := range []string{plan.Text, "self-check every criterion", "PR body", "not acceptance evidence"} {
				if !strings.Contains(prompt, text) {
					t.Fatalf("missing checklist text %q", text)
				}
			}
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	record := produceAcceptance(t, f)
	if plans != 1 || reviews != 1 || record.Plan.Digest != plan.Digest || record.Plan.Planner != plan.Planner {
		t.Fatalf("acceptance replanned: plans=%d reviews=%d", plans, reviews)
	}
	j.op.Name = "repair-ci"
	for _, repair := range []*ciRepairContext{{PR: f.pr}, {PR: f.pr, Acceptance: &acceptanceFindings{Verdict: "failed"}}, {PR: f.pr, MergeConflict: true}} {
		j.repair = repair
		got, err := prepareImplementationPlan(context.Background(), j)
		if err != nil || got.Digest != plan.Digest || plans != 1 {
			t.Fatalf("repair lost criteria: %v", err)
		}
	}
	f.issue.Body += "\nNew requirement"
	j.sub.Body = f.issue.Body
	produceAcceptance(t, f) // Acceptance itself replans when the issue body changes.
	changed, err := prepareImplementationPlan(context.Background(), j)
	if err != nil || plans != 2 || changed.RequirementsDigest == plan.RequirementsDigest {
		t.Fatalf("body change: %v plans=%d", err, plans)
	}
	if plans != 2 || reviews != 2 {
		t.Fatalf("new body plan not reused: plans=%d reviews=%d", plans, reviews)
	}
}

func TestCoordinatorTransportsPreparedChecklist(t *testing.T) {
	f, rc := acceptanceFixture(t)
	j := &runJob{repo: "o/r", repoCfg: rc, client: f, op: &operator.Operator{Name: "implement"}, sub: &operator.Subject{Number: f.issue.Number, Title: f.issue.Title, Body: f.issue.Body}}
	plan, err := prepareImplementationPlan(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	j.acceptancePlan = plan
	_, p := coordinatorTask(j, time.Minute)
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var remote coordinatorPayload
	if err := json.Unmarshal(raw, &remote); err != nil {
		t.Fatal(err)
	}
	if remote.AcceptancePlan == nil || !remote.RepoPolicy.AcceptanceRequired {
		t.Fatal("worker payload lost acceptance contract")
	}
	// Worker mappings have different checkout paths and executable locations.
	j.acceptancePlan = remote.AcceptancePlan
	j.repoCfg.LocalPath = t.TempDir()
	acceptanceNativeRun = func(context.Context, execution.Config, execution.Request, []string, io.Writer, func(execution.Observation)) (execution.Result, error) {
		t.Fatal("worker replanned")
		return execution.Result{}, nil
	}
	got, err := prepareImplementationPlan(context.Background(), j)
	if err != nil || got.Digest != plan.Digest {
		t.Fatalf("remote plan: %v", err)
	}
}

func TestUnenrolledImplementationDoesNotPlan(t *testing.T) {
	j := &runJob{op: &operator.Operator{Name: "implement"}}
	plan, err := prepareImplementationPlan(context.Background(), j)
	if err != nil || plan != nil {
		t.Fatalf("unenrolled planning: %v", err)
	}
}

func TestSkippedImplementationDoesNotPlan(t *testing.T) {
	f, rc := acceptanceFixture(t)
	f.issue.Labels = []string{"clawflow-skip"}
	j := &runJob{repo: "o/r", repoCfg: rc, client: f, op: &operator.Operator{Name: "implement"}, sub: &operator.Subject{Number: 42}}
	acceptanceNativeRun = func(context.Context, execution.Config, execution.Request, []string, io.Writer, func(execution.Observation)) (execution.Result, error) {
		t.Fatal("skipped issue started planning")
		return execution.Result{}, nil
	}
	if _, err := prepareImplementationPlan(context.Background(), j); err == nil {
		t.Fatal("skip ignored")
	}
}
