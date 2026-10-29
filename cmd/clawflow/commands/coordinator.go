package commands

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/actions"
	"github.com/zhoushoujianwork/clawflow/internal/api"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// startCoordinator binds synchronously so startup failures reach the CLI.
func startCoordinator(ctx context.Context, settings config.CoordinatorSettings, out io.Writer, onError func(error)) (func(), error) {
	api.WorkerPoolSource = nil
	if !settings.Enabled {
		return func() {}, nil
	}
	db, err := requireStore(ctx)
	if err != nil {
		return nil, err
	}
	cert, pin, err := coordinator.LoadCertificate(filepath.Join(snapshot.DataDir(), "coordinator"))
	if err != nil {
		db.Close()
		return nil, err
	}
	tasks := coordinator.New(db.DB())
	results := &coordinatorResults{WorkerStore: tasks, tasks: tasks, loadJob: loadCoordinatorJob, apply: applyCoordinatorReport}
	server := coordinator.NewServer(settings.ListenAddress(), results, cert)
	server.Handler = coordinator.NewPlatformHandler(results, &platformAdmin{board: tasks.Board})
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		db.Close()
		return nil, err
	}
	api.WorkerPoolSource = tasks.Board
	fmt.Fprintf(out, "Coordinator HTTPS → %s\nSHA-256 fingerprint: %s\n", listener.Addr(), pin)
	reconcileCtx, reconcileCancel := context.WithCancel(ctx)
	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			if err := results.recoverResults(reconcileCtx, tasks); err != nil && reconcileCtx.Err() == nil {
				onError(err)
			}
			if err := reconcileActionsObservations(reconcileCtx, actions.Ledger{DB: db.DB()}); err != nil && reconcileCtx.Err() == nil {
				onError(err)
			}
			select {
			case <-reconcileCtx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.ServeTLS(listener, "", ""); err != nil && err != http.ErrServerClosed {
			onError(err)
		}
	}()
	return func() { reconcileCancel(); _ = server.Close(); <-done; <-reconcileDone; db.Close() }, nil
}
