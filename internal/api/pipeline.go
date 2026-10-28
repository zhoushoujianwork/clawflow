package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// pipelineTTL is how long a computed board is reused. The board is computed on
// request only (no background polling); the cache keeps a refreshing dashboard
// from turning every 30s tab refresh into a burst of VCS API calls.
const pipelineTTL = 30 * time.Second

// pipelineTimeout bounds one board computation, VCS requests included.
const pipelineTimeout = 90 * time.Second

// PipelineSource builds the repository part of the board. The web command wires
// it up; it lives outside this package because it reuses runner internals
// (acceptance contracts, queue policy) that import this package.
var PipelineSource func(ctx context.Context) (pipeline.Snapshot, error)

var pipelineNow = time.Now

var pipelineCache struct {
	mu   sync.Mutex // also serializes computation so concurrent requests share one
	at   time.Time
	snap pipeline.Snapshot
	ok   bool
}

// HandlePipeline handles GET /api/pipeline: a read-only delivery board.
func HandlePipeline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if PipelineSource == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "pipeline board unavailable"})
		return
	}

	pipelineCache.mu.Lock()
	if !pipelineCache.ok || pipelineNow().Sub(pipelineCache.at) >= pipelineTTL {
		ctx, cancel := context.WithTimeout(r.Context(), pipelineTimeout)
		snap, err := PipelineSource(ctx)
		cancel()
		if err != nil {
			pipelineCache.mu.Unlock()
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "pipeline board failed: " + err.Error()})
			return
		}
		pipelineCache.snap, pipelineCache.at, pipelineCache.ok = snap, pipelineNow(), true
	}
	snap := pipelineCache.snap
	pipelineCache.mu.Unlock()

	writeJSON(w, http.StatusOK, pipeline.Board{Snapshot: snap, Runtime: pipelineRuntime()})
}

// pipelineRuntime is cheap and always fresh: version, scheduler and Offload readiness.
func pipelineRuntime() pipeline.Runtime {
	rs := currentRunStatus()
	rt := pipeline.Runtime{
		Version: VersionInfo.Current,
		Scheduler: pipeline.Scheduler{
			AgentSlotsUsed:  rs.AgentSlotsUsed,
			AgentSlotsTotal: rs.AgentSlotsTotal,
			Status:          rs.Status,
			IntervalMinutes: rs.IntervalMinutes,
			Paused:          rs.Paused,
			NextFireUnixMs:  rs.NextFireUnixMs,
		},
	}
	runMu.Lock()
	var started time.Time
	for repo, at := range activeRunStarts {
		if activeRuns[repo] && (started.IsZero() || at.Before(started)) {
			started = at
		}
	}
	if !started.IsZero() {
		rt.Scheduler.StartedAt = started.UTC().Format(time.RFC3339)
	}
	rt.Scheduler.LastResult = lastPassResult
	if !lastPassAt.IsZero() {
		rt.Scheduler.LastEndedAt = lastPassAt.UTC().Format(time.RFC3339)
	}
	runMu.Unlock()
	// Display one recorded execution, independent of today's configured profile.
	// ReadRunsIndex reads PostgreSQL when a run store is bound (file fallback otherwise).
	rt.Execution = latestExecution(snapshot.ReadRunsIndex())
	if err := execution.Ready(); err != nil {
		rt.Execution.Error = err.Error()
	} else {
		rt.Execution.Ready = true
	}
	return rt
}

// recordedValue maps the "unknown" placeholder that the run store writes for
// missing profile/backend/model back to "not recorded".
func recordedValue(s string) string {
	if strings.TrimSpace(s) == "unknown" {
		return ""
	}
	return s
}

// latestExecution picks the execution identity of the most recently completed
// run that recorded a development session. Completion time is ended_at; legacy
// rows without it fall back to started_at unless the run is still in flight.
// Optional fields (model, service tier) come only from the winning run, never
// from older ones; the model falls back to the sorted model_usage keys and no
// tier is invented. Readiness is left for the caller to fill in.
func latestExecution(runs []snapshot.RunIndexEntry) pipeline.Execution {
	var latest time.Time
	var out pipeline.Execution
	for _, run := range runs {
		profile, backend := recordedValue(run.DevelopmentSession.Profile), recordedValue(run.DevelopmentSession.Backend)
		if profile == "" && backend == "" {
			continue
		}
		at := run.StartedAt
		if run.EndedAt != nil && !run.EndedAt.IsZero() {
			at = *run.EndedAt
		} else {
			switch run.Status {
			case "running", "finalizing", "pending", "queued":
				continue
			}
		}
		if !at.After(latest) {
			continue
		}
		out = pipeline.Execution{Profile: profile, Backend: backend, Model: recordedValue(run.Model), ServiceTier: run.ServiceTier}
		if out.Model == "" && run.Usage != nil {
			var models []string
			for model := range run.Usage.ModelUsage {
				models = append(models, model)
			}
			slices.Sort(models)
			out.Model = strings.Join(models, ", ")
		}
		latest = at
	}
	return out
}
