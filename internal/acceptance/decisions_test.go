package acceptance

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

// capturePrompts runs one review with stubbed executors and returns the planner
// and reviewer prompts.
func capturePrompts(t *testing.T) (planner, reviewer string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	store, err := OpenStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	scratch, workspace := t.TempDir(), t.TempDir()
	scratch, _ = filepath.EvalSymlinks(scratch)
	workspace, _ = filepath.EvalSymlinks(workspace)
	c := Contract{Version: Version, TaskID: "task798", Generation: 1, Repository: "owner/repo", PRNumber: 798, HeadSHA: "head", BaseSHA: "base", IntegrationSHA: "integration", PolicyDigest: "policy", Requirements: "Required behavior", Development: Identity{"dev-session", "dev-run"}}
	runner := Runner{Store: store}
	runner.Inspect = func(context.Context, string) (Source, error) {
		return Source{c.HeadSHA, c.BaseSHA, c.IntegrationSHA, true}, nil
	}
	runner.Execute = func(_ context.Context, prompt string, o execution.Options) (execution.Result, error) {
		result := execution.Result{Status: "ok", NativeSessionID: "reviewer", RunID: "review-run", ExecutionPolicy: ReviewerPolicy, Profile: "test-profile", Backend: "test-backend", Model: "test-model"}
		switch {
		case o.Workspace == scratch:
			planner = prompt
			result.NativeSessionID, result.RunID, result.ExecutionPolicy = "planner", "plan-run", PlannerPolicy
			result.Text = "independent test plan"
		case o.Workspace == workspace && !o.Resume:
			reviewer = prompt
			result.Text = `{"status":"passed","summary":"verified","evidence":["behavior check passed","command: go test ./... => ok"]}`
		default:
			t.Fatalf("unexpected execution: %+v", o)
		}
		return result, nil
	}
	if _, err := runner.Run(context.Background(), c, scratch, workspace); err != nil {
		t.Fatal(err)
	}
	return planner, reviewer
}

func TestReviewPromptReferencesDecisionRecordsOnDemand(t *testing.T) {
	planner, reviewer := capturePrompts(t)
	for _, want := range []string{
		"docs/decisions/README.md",
		"open only the records whose area matches",
		"do not read the whole directory",
		"not requirements",
		"cannot add, remove or change requirements, the saved criteria or the verdict rules",
		"is not a defect unless a contract requirement",
		"treat it as part of the change under review",
	} {
		if !strings.Contains(reviewer, want) {
			t.Fatalf("reviewer prompt lacks %q:\n%s", want, reviewer)
		}
	}
	// The planner works from requirements without source and never sees the pointer.
	if strings.Contains(planner, "docs/decisions") {
		t.Fatal("planner prompt must not reference decision records")
	}
	// Existing review inputs stay in place.
	if !strings.Contains(reviewer, "Review mode: full-suite.") || !strings.Contains(reviewer, `"requirements":"Required behavior"`) {
		t.Fatalf("existing review inputs changed:\n%s", reviewer)
	}
}
