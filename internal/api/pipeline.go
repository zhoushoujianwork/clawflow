package api

import (
	"context"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/pilot"
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
	records, err := pilot.ReadActionRecords(filepath.Join(snapshot.DataDir(), "pilot-actions.jsonl"))
	actions := []pilot.ActionRecord{}
	if err == nil {
		// Pending and terminal records share an identity; show the latest state once.
		latest := map[string]int{}
		for _, record := range records {
			if !slices.ContainsFunc(board.Repos, func(repo pipeline.RepoBoard) bool { return repo.Repo == record.Action.Repo }) {
				continue
			}
			key := record.RunID + "/" + record.Action.ID
			if index, ok := latest[key]; ok {
				actions[index] = record
			} else {
				latest[key] = len(actions)
				actions = append(actions, record)
			}
		}
		slices.SortStableFunc(actions, func(a, b pilot.ActionRecord) int { return a.At.Compare(b.At) })
	}
	actionError := ""
	if err != nil {
		actionError = "Pilot action history unavailable"
	}
	writeJSON(w, http.StatusOK, struct {
		pipeline.Board
		PilotActions []pilot.ActionRecord `json:"pilot_actions"`
		PilotError   string               `json:"pilot_error,omitempty"`
	}{board, actions, actionError})
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
