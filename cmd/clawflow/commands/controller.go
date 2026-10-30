package commands

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
)

func controllerLock() (*os.File, error) {
	if info, ok := readWebPid(); ok && pidAlive(info.PID) && isClawflowProcess(info.PID) {
		return nil, fmt.Errorf("legacy web scheduler is running (pid %d); stop it before starting the controller", info.PID)
	}
	return filelock.Try(filepath.Join(config.StateDir(), "locks", "controller.lock"), map[string]any{"PID": os.Getpid()})
}

// NewControllerCmd runs scheduling and authenticated worker/admin APIs without
// a browser server. Native viewers can come and go independently.
func NewControllerCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "controller", Short: "Run the background controller without the Web UI", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if err := cfg.Settings.SchedulingError(); err != nil {
			return err
		}
		if !cfg.Settings.Coordinator.Enabled {
			return fmt.Errorf("enable settings.coordinator.enabled before starting the controller")
		}
		lock, err := controllerLock()
		if err != nil {
			return err
		}
		defer filelock.Release(lock)
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		release, err := gateStore(ctx)
		if err != nil {
			return err
		}
		defer release()
		lg, _ := clog.Open("controller")
		defer lg.Close()
		webLog = lg
		errors := make(chan error, 1)
		shutdown, err := startCoordinator(ctx, cfg.Settings.Coordinator, cmd.ErrOrStderr(), func(err error) {
			select {
			case errors <- err:
			default:
			}
			stop()
		})
		if err != nil {
			return err
		}
		defer shutdown()
		done := make(chan struct{})
		go func() { defer close(done); runPeriodicSchedulerContext(ctx) }()
		fmt.Fprintln(cmd.ErrOrStderr(), "Controller ready. Closing the native dashboard does not stop this service.")
		<-ctx.Done()
		<-done
		select {
		case err := <-errors:
			return err
		default:
			return nil
		}
	}}
	cmd.AddCommand(newServiceCmd(true))
	return cmd
}
