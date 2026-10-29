package acceptance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

func TestRequirementsPlanBeforeImplementationReusedByReview(t *testing.T) {
	r, scratch, workspace := setupRunner(t)
	r.PlanStore = openTestStore(t)
	base := r.Execute
	plans, reviews := 0, 0
	r.Execute = func(ctx context.Context, prompt string, opts execution.Options) (execution.Result, error) {
		if opts.Workspace == scratch {
			plans++
		} else {
			reviews++
			if !strings.Contains(prompt, "git diff <base_sha>...HEAD") || !strings.Contains(prompt, "git log <base_sha>..HEAD") {
				t.Fatal("cached-plan review lacks offline base comparison instructions")
			}
		}
		return base(ctx, prompt, opts)
	}
	c := contract()
	// No implementation identity or PR revision exists when planning starts.
	pre := planningContract(c)
	plan, err := r.RequirementsPlan(context.Background(), pre, scratch)
	if err != nil {
		t.Fatal(err)
	}
	if plans != 1 || reviews != 0 {
		t.Fatalf("premature review: plans=%d reviews=%d", plans, reviews)
	}
	record, err := r.Run(context.Background(), c, scratch, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if plans != 1 || reviews != 1 || record.Plan.Digest != plan.Digest || record.Plan.Planner != plan.Planner || !record.Plan.StartedAt.Equal(plan.StartedAt) {
		t.Fatalf("plan was not reused: plans=%d reviews=%d", plans, reviews)
	}
	if err := Evaluate(c, record); err != nil {
		t.Fatal(err)
	}
	// A different native reviewer is still mandatory, even with a cached plan.
	record.Reviewer.Identity = record.Plan.Planner.Identity
	if Evaluate(c, record) == nil {
		t.Fatal("planner accepted its own plan")
	}
	record.Reviewer = native(reviewerResult(c.Development.SessionID, "new-run"))
	if Evaluate(c, record) == nil {
		t.Fatal("developer accepted its own implementation")
	}
}

func TestRequirementsPlanInvalidationAndTransport(t *testing.T) {
	r, scratch, _ := setupRunner(t)
	r.PlanStore = openTestStore(t)
	calls := 0
	r.Execute = func(_ context.Context, prompt string, opts execution.Options) (execution.Result, error) {
		calls++
		if !opts.ToolsDisabled || opts.Resume || opts.SessionKey != "" || opts.PermissionMode != "default" {
			t.Fatal("planner isolation changed")
		}
		result := successful("planner", "plan-run")
		result.Text = `[{"id":"C1","requirement":"Required behavior","proof":"execution"}]`
		return result, nil
	}
	c := contract()
	c.EvidenceSchema = 1
	first, err := r.RequirementsPlan(context.Background(), c, scratch)
	if err != nil {
		t.Fatal(err)
	}
	c.HeadSHA, c.BaseSHA, c.PRNumber, c.Development = "new-head", "new-base", 12, Identity{"new-session", "new-run"}
	if _, err := r.RequirementsPlan(context.Background(), c, scratch); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("revision change replanned: %d", calls)
	}
	// A remote worker receives the durable coordinator plan, without invoking a model.
	remote := Runner{SavedPlan: &first}
	transported, err := remote.RequirementsPlan(context.Background(), c, "")
	if err != nil || transported.Digest != first.Digest {
		t.Fatalf("transport: %v", err)
	}
	c.Requirements += "\nAdditional requirement"
	if _, err := remote.RequirementsPlan(context.Background(), c, ""); err == nil {
		t.Fatal("stale transported plan accepted")
	}
	if _, err := r.RequirementsPlan(context.Background(), c, scratch); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("body change did not replan: %d", calls)
	}
	c.PolicyDigest = "changed-policy"
	if _, err := r.RequirementsPlan(context.Background(), c, scratch); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatalf("policy change did not replan: %d", calls)
	}
}

func TestRequirementsPlanDoesNotCacheFailures(t *testing.T) {
	r, scratch, _ := setupRunner(t)
	r.PlanStore = openTestStore(t)
	base := r.Execute
	r.Execute = func(context.Context, string, execution.Options) (execution.Result, error) {
		return execution.Result{}, errors.New("planner unavailable")
	}
	if _, err := r.RequirementsPlan(context.Background(), contract(), scratch); err == nil {
		t.Fatal("planner failure ignored")
	}
	r.Execute = base
	plan, err := r.RequirementsPlan(context.Background(), contract(), scratch)
	if err != nil || !strings.Contains(plan.Text, "independent") {
		t.Fatalf("retry: %v", err)
	}
}
