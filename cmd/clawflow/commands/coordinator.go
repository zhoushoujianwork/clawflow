package commands

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/devmode"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"

	"github.com/zhoushoujianwork/clawflow/internal/actions"
	"github.com/zhoushoujianwork/clawflow/internal/api"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/upgrade"
)

// controllerLog is the controller process's own log (controller.log). runLog is
// only assigned by `clawflow run`, so controller-side code must go through
// coordLog instead of using runLog directly.
var controllerLog *clog.Logger

// coordLog prefers runLog when a run command installed one, otherwise the
// controller log. Both are nil-safe no-ops when unavailable.
func coordLog() *clog.Logger {
	if runLog != nil {
		return runLog
	}
	return controllerLog
}

// logReconcileFailure records only the redacted, clipped error text, never
// task payloads.
func logReconcileFailure(err error) {
	coordLog().Warn("coordinator/reconcile", "err", pipelineClip(upgrade.Redact(err.Error()), pipelineErrLimit))
}

// runReconcilePass runs each step and logs failures unless ctx is done.
func runReconcilePass(ctx context.Context, steps ...func(context.Context) error) {
	for _, step := range steps {
		if err := step(ctx); err != nil && ctx.Err() == nil {
			logReconcileFailure(err)
		}
	}
}

// openControllerLog opens controller.log when no run log is installed.
func openControllerLog(out io.Writer) {
	if runLog != nil {
		return
	}
	lg, err := clog.Open("controller")
	if err != nil {
		fmt.Fprintf(out, "controller log unavailable: %v\n", err)
		return
	}
	controllerLog = lg
}

// startCoordinator binds synchronously so startup failures reach the CLI.
func startCoordinator(ctx context.Context, settings config.CoordinatorSettings, out io.Writer, onError func(error)) (func(), error) {
	api.WorkerPoolSource = nil
	if !settings.Enabled {
		return func() {}, nil
	}
	openControllerLog(out)
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
	tasks.LeaseLimits = func() (int, map[string]int, error) {
		cfg, err := config.Load()
		if err != nil {
			return 0, nil, err
		}
		total := cfg.Settings.MaxConcurrentAgents
		if total <= 0 {
			total = 4
		}
		repos := map[string]int{}
		for name, rc := range cfg.Repos {
			n := rc.MaxInFlight
			if n <= 0 {
				n = queue.DefaultMaxInFlight
			}
			repos[name] = n
		}
		return total, repos, nil
	}
	if cfg, err := config.Load(); err == nil {
		w := cfg.Settings.Watchdog
		tasks.Watchdog = coordinator.WatchdogConfig{FailureThreshold: w.FailureThreshold, FailureWindow: time.Duration(w.FailureWindowMin) * time.Minute, OrphanAfter: time.Duration(w.OrphanedAfterMinute) * time.Minute}
	}
	tasks.OrphanedPRs = newOrphanedPRProvider()
	results := &coordinatorResults{WorkerStore: tasks, tasks: tasks, loadJob: loadCoordinatorJob, apply: applyCoordinatorReport}
	server := coordinator.NewServer(settings.ListenAddress(), results, cert)
	adminHandler := coordinator.NewPlatformHandler(results, &platformAdmin{board: tasks.Board, workers: tasks})
	joinHandler := coordinator.JoinHandler(tasks)
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/worker/v1/join" {
			joinHandler.ServeHTTP(w, r)
			return
		}
		adminHandler.ServeHTTP(w, r)
	})
	if devmode.Active() {
		if err := devmode.CheckListen(server.Addr); err != nil {
			db.Close()
			return nil, err
		}
	}
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		db.Close()
		return nil, err
	}
	api.WorkerPoolSource = tasks.Board
	fmt.Fprintf(out, "Coordinator HTTPS → %s\nSHA-256 fingerprint: %s\n", listener.Addr(), pin)
	reconcileCtx, reconcileCancel := context.WithCancel(ctx)
	reconcileDone := make(chan struct{})
	// VCS orphaned-PR lookups run on their own schedule, never from board reads.
	orphanDone := make(chan struct{})
	go func() {
		defer close(orphanDone)
		tasks.RunOrphanRefresh(reconcileCtx)
	}()
	go func() {
		defer close(reconcileDone)
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		var lastAuditPurge time.Time
		for {
			if time.Since(lastAuditPurge) >= 10*time.Minute {
				lastAuditPurge = time.Now()
				retention := coordinator.AuditRetention{
					Routine:  time.Duration(settings.AuditRetentionDays) * 24 * time.Hour,
					Security: time.Duration(settings.AuditSecurityRetentionDays) * 24 * time.Hour,
				}
				if _, err := tasks.PurgeAudit(reconcileCtx, time.Now(), retention); err != nil && reconcileCtx.Err() == nil {
					coordLog().Warn("coordinator/audit_retention", "err", err.Error())
				}
			}
			runReconcilePass(reconcileCtx,
				func(c context.Context) error { return results.recoverResults(c, tasks) },
				func(c context.Context) error { return endInactiveInterruptions(c, tasks) },
				func(c context.Context) error { return results.endExhaustedInterruptions(c, tasks) },
				func(c context.Context) error {
					return reconcileActionsObservations(c, actions.Ledger{DB: db.DB()})
				},
			)
			// Execution output of finished tasks expires after the Offload retention window.
			if _, err := tasks.PurgeLogs(reconcileCtx, time.Now().UTC().Add(-coordinator.LogRetention)); err != nil && reconcileCtx.Err() == nil {
				coordLog().Warn("coordinator/log_retention", "err", err.Error())
			}
			select {
			case <-reconcileCtx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		serveCoordinatorWatchdog(reconcileCtx, tasks.Board, watchdogInterval)
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.ServeTLS(listener, "", ""); err != nil && err != http.ErrServerClosed {
			onError(err)
		}
	}()
	stopWorker, err := startIndependentLocalWorker(reconcileCtx, settings.LocalWorker, listener.Addr(), pin, tasks, onError)
	if err != nil {
		reconcileCancel()
		_ = server.Close()
		<-done
		<-reconcileDone
		<-orphanDone
		<-watchdogDone
		db.Close()
		return nil, err
	}
	return func() {
		reconcileCancel()
		stopWorker()
		_ = server.Close()
		<-done
		<-reconcileDone
		<-orphanDone
		<-watchdogDone
		db.Close()
	}, nil
}

var localWorkerJob = buildWorkerJob

// startLocalWorker uses the same authenticated HTTPS API as remote workers.
// localWorkerPoll is how often the in-process worker asks for new work when
// idle; tests shrink it.
var localWorkerPoll = 5 * time.Second

func startLocalWorker(ctx context.Context, settings config.LocalWorkerSettings, addr net.Addr, pin string, tasks *coordinator.Store, onError func(error)) (func(), error) {
	if !settings.Enabled {
		return func() {}, nil
	}
	parallel := settings.MaxParallel
	if parallel == 0 {
		parallel = 1
	}
	if parallel < 1 {
		return nil, fmt.Errorf("local_worker.max_parallel 必须为正数")
	}
	reg, err := loadRegistry()
	if err != nil {
		return nil, err
	}
	tokenPath := filepath.Join(snapshot.DataDir(), "coordinator", "local-worker.token")
	if _, err := os.Stat(tokenPath); os.IsNotExist(err) {
		secret, err := tasks.CreateToken(ctx, "local-worker", "worker")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(tokenPath, []byte(secret), 0600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return nil, err
	}
	if host == "" || net.ParseIP(host).IsUnspecified() {
		host = "localhost"
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	o := workerOptions{Coordinator: "https://" + net.JoinHostPort(host, port), TokenFile: tokenPath, Fingerprint: pin, MaxParallel: parallel, Tags: append(append([]string{}, settings.Tags...), config.HostTag(hostname)), Roles: []string{"operator", "acceptance"}, RunUntagged: true}
	o.Gate = workerGateFactory(hostname, o.Roles)
	api, err := newWorkerAPI(o)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer api.client.CloseIdleConnections()
		if err := runWorker(ctx, api, o, 30*time.Second, localWorkerPoll, func(ctx context.Context, t *coordinator.Task) (*runJob, time.Duration, error) {
			return localWorkerJob(ctx, t, reg)
		}); err != nil && ctx.Err() == nil {
			coordLog().Warn("coordinator/local-worker", "err", err.Error())
		}
	}()
	return func() { cancel(); <-done }, nil
}

// Local execution has an independent process lifetime; controller shutdown only
// closes its listener. On recovery, the next manager generation reuses journals.
func startIndependentLocalWorker(ctx context.Context, settings config.LocalWorkerSettings, addr net.Addr, pin string, tasks *coordinator.Store, onError func(error)) (func(), error) {
	if !settings.Enabled {
		return func() {}, nil
	}
	parallel := settings.MaxParallel
	if parallel == 0 {
		parallel = 1
	}
	if parallel < 1 {
		return nil, fmt.Errorf("invalid local worker capacity")
	}
	tokenPath := filepath.Join(snapshot.DataDir(), "coordinator", "local-worker.token")
	if _, err := os.Stat(tokenPath); os.IsNotExist(err) {
		secret, err := tasks.CreateToken(ctx, "local-worker", "worker")
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(tokenPath, []byte(secret), 0600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return nil, err
	}
	if host == "" || net.ParseIP(host).IsUnspecified() {
		host = "localhost"
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	o := workerOptions{Coordinator: "https://" + net.JoinHostPort(host, port), TokenFile: tokenPath, Fingerprint: pin, MaxParallel: parallel, Tags: append(append([]string{}, settings.Tags...), config.HostTag(hostname)), Roles: []string{"operator", "acceptance"}, RunUntagged: true}
	o.Durable = true
	o.StateDir = filepath.Join(config.StateDir(), "local-worker")
	if err := startLocalWorkerProcess(o, ""); err != nil {
		return nil, err
	}
	return func() {}, nil
}

var startLocalWorkerProcess = startWorkerGeneration
