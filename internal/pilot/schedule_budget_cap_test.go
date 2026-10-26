package pilot

import (
	"context"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/offloadtest"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWakeBudgetRejectedBeforeOffloadLaunch(t *testing.T) {
	offloadtest.Setup(t)
	capture := filepath.Join(t.TempDir(), "calls")
	t.Setenv("OFFLOAD_TEST_CAPTURE", capture)
	_, err := operator.RunOffload(operator.WithMaxBudgetUSD(context.Background(), 8), "Pilot task", t.TempDir(), time.Second, nil, "operator")
	if !execution.IsKind(err, "unsupported_budget") {
		t.Fatal(err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("unsupported cap launched Offload")
	}
}
