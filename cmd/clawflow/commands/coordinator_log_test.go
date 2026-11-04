package commands

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clog "github.com/zhoushoujianwork/clawflow/internal/log"
)

func TestReconcileFailureWritesControllerLogWithoutRunLog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldRun, oldCtl := runLog, controllerLog
	t.Cleanup(func() { runLog, controllerLog = oldRun, oldCtl })
	runLog = nil
	lg, err := clog.Open("controller")
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()
	controllerLog = lg

	logReconcileFailure(errors.New("pending results: boom"))

	data, err := os.ReadFile(filepath.Join(clog.LogsDir(), "controller.log"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "coordinator/reconcile") || !strings.Contains(got, "boom") {
		t.Fatalf("log missing reconcile failure: %q", got)
	}
	if strings.Contains(got, "postgres://") || strings.Contains(got, "payload") {
		t.Fatalf("log leaked sensitive data: %q", got)
	}
}

func TestCoordLogPrefersRunLog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	oldRun, oldCtl := runLog, controllerLog
	t.Cleanup(func() { runLog, controllerLog = oldRun, oldCtl })
	r, _ := clog.Open("run")
	defer r.Close()
	runLog = r
	controllerLog = nil
	if coordLog() != r {
		t.Fatal("coordLog must keep using runLog under clawflow run")
	}
}
