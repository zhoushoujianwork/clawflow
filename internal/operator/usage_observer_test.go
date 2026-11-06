package operator

import (
	"context"
	"errors"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

// A failed prepare-fresh-session step used to return before the result
// observer ran, so the run kept no usage at all.
func TestRunDevelopmentObservesResultWhenFreshSessionResetFails(t *testing.T) {
	cost := 0.5
	var observed []execution.Result
	ctx := WithExecutionResultObserver(context.Background(), func(r execution.Result) { observed = append(observed, r) })
	opts := RunOptions{
		ResumePrompt:        "resume",
		SessionKey:          "test-session",
		PrepareFreshSession: func() error { return errors.New("reset failed") },
		ExecuteFunc: func(context.Context, string, execution.Options) (execution.Result, error) {
			return execution.Result{CostUSD: &cost}, &execution.Error{Kind: "execution"}
		},
	}
	_, _ = runDevelopment(ctx, "prompt", opts)
	if len(observed) == 0 {
		t.Fatal("reset-failure path skipped the result observer")
	}
}
