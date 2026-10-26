package operator

import (
	"context"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/offloadtest"
	"testing"
	"time"
)

func TestOffloadHelper(t *testing.T) { offloadtest.Helper(t) }
func TestRunOffloadBudgetFailsBeforeLaunch(t *testing.T) {
	offloadtest.Setup(t)
	_, err := RunOffload(WithMaxBudgetUSD(context.Background(), 8.75), "prompt", t.TempDir(), time.Second, nil, "operator", "instructions")
	if !execution.IsKind(err, "unsupported_budget") {
		t.Fatal(err)
	}
}
func TestRunOffloadSemanticOutcome(t *testing.T) {
	offloadtest.Setup(t)
	t.Setenv("HOME", t.TempDir())
	text, err := RunOffload(context.Background(), "task", t.TempDir(), 3*time.Second, nil, "operator", "instructions")
	if err != nil {
		t.Fatal(err)
	}
	outcome, _ := parseOutcome(text)
	if outcome != "done" {
		t.Fatalf("text=%s", text)
	}
}
