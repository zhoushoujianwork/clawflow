package commands

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

func TestLocalWorkerLeasesAndCompletesOverHTTPS(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	db := storetest.New(t)
	ctx := context.Background()
	if _, err := db.Migrate(ctx, store.Migrations); err != nil {
		t.Fatal(err)
	}
	tasks := coordinator.New(db.DB())
	cert, pin, err := coordinator.LoadCertificate(filepath.Join(snapshot.DataDir(), "coordinator"))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := coordinator.NewServer(listener.Addr().String(), tasks, cert)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		if err := server.ServeTLS(listener, "", ""); err != nil && err != http.ErrServerClosed {
			t.Error(err)
		}
	}()
	defer func() { _ = server.Close(); <-serverDone }()
	task, err := tasks.Enqueue(ctx, coordinator.Task{Repo: "o/r", Issue: 7, Kind: "implement", Operator: "implement", Tags: []string{"github"}})
	if err != nil {
		t.Fatal(err)
	}
	oldBuild, oldRun := localWorkerJob, runOneOperatorFn
	defer func() { localWorkerJob = oldBuild; runOneOperatorFn = oldRun }()
	localWorkerJob = func(context.Context, *coordinator.Task, *operator.Registry) (*runJob, time.Duration, error) {
		return &runJob{}, time.Minute, nil
	}
	runOneOperatorFn = func(_ context.Context, j *runJob, _ time.Duration) (bool, bool, bool) {
		j.hooks.AfterRun(&jobResult{Status: "success"})
		return true, false, false
	}
	stop, err := startLocalWorker(ctx, config.LocalWorkerSettings{Enabled: true, Tags: []string{"github"}}, listener.Addr(), pin, tasks, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		board, err := tasks.Board(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, got := range board.Tasks {
			if got.ID == task.ID && got.State == "done" {
				if got.Worker != "local-worker" {
					t.Fatalf("missing local worker lease: %+v", got)
				}
				if len(board.Workers) != 1 || !board.Workers[0].Live {
					t.Fatalf("worker not visible: %+v", board)
				}
				return
			}
		}
		select {
		case <-deadline:
			t.Fatal("local worker did not complete task")
		case <-tick.C:
		}
	}
}

func TestLocalWorkerDisabled(t *testing.T) {
	stop, err := startLocalWorker(context.Background(), config.LocalWorkerSettings{}, nil, "", nil, func(error) { t.Error("disabled worker started") })
	if err != nil {
		t.Fatal(err)
	}
	stop()
}
