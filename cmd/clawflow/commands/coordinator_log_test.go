package commands

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clog "github.com/zhoushoujianwork/clawflow/internal/log"
)

func isolateControllerLog(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	oldRun, oldCtl := runLog, controllerLog
	runLog, controllerLog = nil, nil
	t.Cleanup(func() {
		if controllerLog != nil {
			controllerLog.Close()
		}
		runLog, controllerLog = oldRun, oldCtl
	})
}

func failingStep(context.Context) error {
	return errors.New("pending results: boom postgres://svc:hunter2pw@db.example/prod?sslmode=disable Authorization: Bearer abcdefghijklmnop123456")
}

func TestReconcileFailureWritesRedactedControllerLog(t *testing.T) {
	isolateControllerLog(t, t.TempDir())
	openControllerLog(io.Discard)
	if controllerLog == nil {
		t.Fatal("controller log not opened")
	}
	runReconcilePass(context.Background(), failingStep)

	data, err := os.ReadFile(filepath.Join(clog.LogsDir(), "controller.log"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, "coordinator/reconcile") || !strings.Contains(got, "boom") {
		t.Fatalf("log missing reconcile failure: %q", got)
	}
	if strings.Contains(got, "hunter2pw") || strings.Contains(got, "abcdefghijklmnop123456") {
		t.Fatalf("log leaked sensitive data: %q", got)
	}
}

func TestReconcileCancelledContextWritesNothing(t *testing.T) {
	isolateControllerLog(t, t.TempDir())
	openControllerLog(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runReconcilePass(ctx, failingStep)

	data, _ := os.ReadFile(filepath.Join(clog.LogsDir(), "controller.log"))
	if strings.Contains(string(data), "coordinator/reconcile") {
		t.Fatalf("cancelled pass logged: %q", data)
	}
}

func TestOpenControllerLogFailureIsReported(t *testing.T) {
	file := filepath.Join(t.TempDir(), "home-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	isolateControllerLog(t, file)
	var buf bytes.Buffer
	openControllerLog(&buf)
	if !strings.Contains(buf.String(), "controller log") {
		t.Fatalf("missing notice: %q", buf.String())
	}
	if controllerLog != nil {
		t.Fatal("controllerLog must stay nil")
	}
	coordLog().Warn("coordinator/reconcile", "err", "x")
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
