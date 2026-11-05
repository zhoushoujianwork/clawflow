package commands

import (
	"context"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
)

const (
	watchdogInterval      = 30 * time.Second
	watchdogPassTimeout   = 10 * time.Second
	workerProgressTimeout = 5 * time.Second
)

// serveCoordinatorWatchdog owns its log: runLog is only set by `clawflow run`,
// so it is nil in the controller process and would silently drop every line.
func serveCoordinatorWatchdog(ctx context.Context, board func(context.Context) (coordinator.Board, error), interval time.Duration) {
	lg, _ := clog.Open("watchdog")
	defer lg.Close()
	runCoordinatorWatchdog(ctx, board, interval, func(event string, f coordinator.Finding) {
		kv := []any{"event", event, "task", f.TaskID, "repo", f.Repo, "issue", f.Issue, "pr", f.PR, "rule", f.Rule, "age_sec", f.AgeSec}
		if event == "open" {
			lg.Warn("coordinator/watchdog", kv...)
			return
		}
		lg.Info("coordinator/watchdog", kv...)
	}, func(err error) {
		lg.Warn("coordinator/watchdog", "err", err.Error())
	})
}

// runCoordinatorWatchdog is separate from result reconciliation, so a blocked
// reconcile pass cannot silence the check meant to notice it.
func runCoordinatorWatchdog(ctx context.Context, board func(context.Context) (coordinator.Board, error), interval time.Duration, report func(string, coordinator.Finding), fail func(error)) {
	tracker := &coordinator.FindingTracker{}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		passCtx, cancel := context.WithTimeout(ctx, watchdogPassTimeout)
		current, err := board(passCtx)
		cancel()
		switch {
		case err != nil && ctx.Err() == nil:
			fail(err)
		case err == nil:
			opened, resolved := tracker.Update(current.Findings)
			for _, finding := range opened {
				report("open", finding)
			}
			for _, finding := range resolved {
				report("resolved", finding)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
