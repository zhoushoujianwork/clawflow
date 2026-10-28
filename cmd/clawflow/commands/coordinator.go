package commands

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// startCoordinator binds synchronously so startup failures reach the CLI.
func startCoordinator(ctx context.Context, settings config.CoordinatorSettings, out io.Writer, onError func(error)) (func(), error) {
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
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		db.Close()
		return nil, err
	}
	fmt.Fprintf(out, "Coordinator HTTPS → %s\nSHA-256 fingerprint: %s\n", listener.Addr(), pin)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.ServeTLS(listener, "", ""); err != nil && err != http.ErrServerClosed {
			onError(err)
		}
	}()
	return func() { _ = server.Close(); <-done; db.Close() }, nil
}
