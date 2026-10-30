package commands

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

// Readiness cache lifetimes. A blocked result expires sooner so installing
// Offload is noticed quickly; neither value causes a probe per poll.
const (
	readyTTL   = execution.StatusReadyTTL
	blockedTTL = execution.StatusBlockedTTL
	gateBudget = 20 * time.Second
)

// workerGate checks this process's own environment (the real launchd
// environment for a service) before each claim, through one shared cache.
type workerGate struct {
	cache *execution.Cache
	name  string

	mu   sync.Mutex
	last string
}

// workerGateFactory is a test seam so unrelated worker tests need no Offload.
var workerGateFactory = newWorkerGate

func newWorkerGate(name string, roles []string) *workerGate {
	return &workerGate{name: name, cache: newReadinessCache(func(ctx context.Context) execution.Readiness {
		opts := execution.ReadinessOptions{Worker: name, Environment: "service"}
		c, err := loadExecutionSettings(osLookup)
		if err != nil {
			return execution.ConfigBlockedReadiness(err.Error(), opts)
		}
		return execution.CheckReadiness(ctx, c, roles, opts)
	})}
}

func newReadinessCache(run func(context.Context) execution.Readiness) *execution.Cache {
	return &execution.Cache{ReadyTTL: readyTTL, BlockedTTL: blockedTTL, Run: func(ctx context.Context) execution.Readiness {
		ctx, cancel := context.WithTimeout(ctx, gateBudget)
		defer cancel()
		return run(ctx)
	}}
}

// ready reports whether claims may proceed. A changed condition is logged
// once, so a long outage does not repeat the same warning every poll.
func (g *workerGate) ready(ctx context.Context) bool {
	r := g.cache.Get(ctx, false)
	g.mu.Lock()
	key := r.Key()
	changed := key != g.last
	g.last = key
	g.mu.Unlock()
	if changed {
		if r.Blocked() {
			slog.Warn("worker/offload_unavailable", "worker", g.name, "reason", r.Reason, "summary", r.Summary, "recheck", execution.RecheckCommand)
		} else {
			slog.Info("worker/offload_ready", "worker", g.name)
		}
	}
	return !r.Blocked()
}

// localReadiness is the process-wide cache for in-process (non-worker) runs.
var (
	localReadinessOnce sync.Once
	localReadiness     *execution.Cache
)

// offloadReadyFn is a test seam. It returns blocked readiness when this
// process cannot run Offload; local runs consult it before any state changes.
var offloadReadyFn = func(ctx context.Context, role string) execution.Readiness {
	localReadinessOnce.Do(func() {
		localReadiness = newReadinessCache(func(ctx context.Context) execution.Readiness {
			opts := execution.ReadinessOptions{Worker: "this run", Environment: "interactive"}
			c, err := loadExecutionSettings(osLookup)
			if err != nil {
				return execution.ConfigBlockedReadiness(err.Error(), opts)
			}
			return execution.CheckReadiness(ctx, c, []string{config.RoleOperator, config.RoleLight, config.RoleEval}, opts)
		})
	})
	return localReadiness.Get(ctx, false)
}
