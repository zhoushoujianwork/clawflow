package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

const pipelineTTL = 20 * time.Second
const pipelineTimeout = 10 * time.Second

// PipelineSource supplements scheduler snapshots in the background.
var PipelineSource func(ctx context.Context) (pipeline.Snapshot, error)
var pipelineNow = time.Now
var pipelineWorkers sync.WaitGroup
var pipelineCache struct {
	mu         sync.Mutex
	at         time.Time
	snap       pipeline.Snapshot
	ok         bool
	refreshing bool
	err        string
}

// HandlePipeline never waits for a VCS request, including on a cold start.
func HandlePipeline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	disk, diskErr := snapshot.ReadPipeline()
	now := pipelineNow()
	pipelineCache.mu.Lock()
	if diskErr == nil && (!pipelineCache.ok || pipeline.ParseTime(disk.GeneratedAt).After(pipeline.ParseTime(pipelineCache.snap.GeneratedAt))) {
		pipelineCache.snap, pipelineCache.ok = disk, true
	}
	if !pipelineCache.ok && PipelineSource == nil {
		pipelineCache.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "pipeline board unavailable"})
		return
	}
	stale := !pipelineCache.ok || now.Sub(pipeline.ParseTime(pipelineCache.snap.GeneratedAt)) >= pipelineTTL
	if stale && !pipelineCache.refreshing && (pipelineCache.at.IsZero() || now.Sub(pipelineCache.at) >= pipelineTTL) && PipelineSource != nil {
		pipelineCache.refreshing, pipelineCache.at = true, now
		source := PipelineSource
		pipelineWorkers.Add(1)
		go func() {
			defer pipelineWorkers.Done()
			ctx, cancel := context.WithTimeout(context.Background(), pipelineTimeout)
			defer cancel()
			fresh, err := source(ctx)
			pipelineCache.mu.Lock()
			defer pipelineCache.mu.Unlock()
			pipelineCache.refreshing = false
			if err != nil {
				pipelineCache.err = "pipeline refresh failed: " + err.Error()
				return
			}
			pipelineCache.err = ""
			if !pipelineCache.ok || !pipeline.ParseTime(fresh.GeneratedAt).Before(pipeline.ParseTime(pipelineCache.snap.GeneratedAt)) {
				pipelineCache.snap, pipelineCache.ok = fresh, true
			}
		}()
	}
	snap := pipelineCache.snap
	if !pipelineCache.ok {
		snap = pipeline.Snapshot{Repos: []pipeline.RepoBoard{}, Events: []pipeline.Event{}, GlobalQueue: []pipeline.GlobalQueueEntry{}}
	}
	for _, repo := range snap.Repos {
		stale = stale || repo.Stale
	}
	board := pipeline.Board{Snapshot: snap, Stale: stale, Refreshing: pipelineCache.refreshing, Error: pipelineCache.err}
	pipelineCache.mu.Unlock()
	board.Runtime = pipelineRuntime()
	writeJSON(w, http.StatusOK, board)
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
	if err := execution.Ready(); err != nil {
		rt.Execution.Error = err.Error()
	} else {
		rt.Execution.Ready = true
	}
	return rt
}
