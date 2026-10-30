package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/pilot"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/store"
)

const pipelineTTL = 20 * time.Second
const pipelineTimeout = 10 * time.Second

// PipelineSource supplements scheduler snapshots in the background.
var PipelineSource func(ctx context.Context) (pipeline.Snapshot, error)

// WorkerPoolSource is bound before the dashboard starts serving.
var WorkerPoolSource func(context.Context) (coordinator.Board, error)

var pipelineNow = time.Now
var pipelineWorkers sync.WaitGroup
var pipelineCache struct {
	mu         sync.Mutex
	at         time.Time
	snap       pipeline.Snapshot
	ok         bool
	refreshing bool
	err        string
	observed   map[string]time.Time
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
		rememberPipelineDelivery(disk)
		pipelineCache.snap, pipelineCache.ok = disk, true
		pipelineCache.err = ""
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
				pipelineCache.err = pipelineErrorCategory(err)
				return
			}
			pipelineCache.err = ""
			if !pipelineCache.ok || !pipeline.ParseTime(fresh.GeneratedAt).Before(pipeline.ParseTime(pipelineCache.snap.GeneratedAt)) {
				rememberPipelineDelivery(fresh)
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
	deliveryAt, deliveryCategory := pipeline.ParseTime(snap.GeneratedAt), pipelineCache.err
	allObserved := true
	sources := []pipelineSourceHealth{{}}
	for _, repo := range snap.Repos {
		category := pipelineCache.err
		if repo.Error != "" {
			category = pipelineErrorCategory(errors.New(repo.Error))
		} else if repo.Stale && category == "" {
			category = "stale"
		}
		at := pipelineCache.observed[repo.Repo]
		if at.IsZero() {
			allObserved = false
		} else if at.Before(deliveryAt) {
			deliveryAt = at
		}
		if deliveryCategory == "" {
			deliveryCategory = category
		}
		sources = append(sources, sourceHealth("delivery", repo.Repo, at, now, pipelineTTL, pipelineCache.refreshing, category))
	}
	if !allObserved {
		deliveryAt = time.Time{}
	}
	sources[0] = sourceHealth("delivery", "", deliveryAt, now, pipelineTTL, pipelineCache.refreshing, deliveryCategory)
	pipelineCache.mu.Unlock()
	board.Repos = append([]pipeline.RepoBoard{}, board.Repos...)
	if cfg, err := config.Load(); err == nil {
		platform := cfg.Settings.Platform.URL
		if platform == "" && cfg.Settings.Coordinator.Enabled {
			host, _, err := net.SplitHostPort(r.Host)
			if err != nil {
				host = r.Host
			}
			_, port, err := net.SplitHostPort(cfg.Settings.Coordinator.ListenAddress())
			if err == nil {
				platform = "https://" + net.JoinHostPort(host, port)
			}
		}
		for i := range board.Repos {
			board.Repos[i].HostingPlatform = platform
		}
	}
	board.Runtime = pipelineRuntime()
	liveHealth := projectPipelineExecutions(r.Context(), &board, now)
	sources = append(sources, liveHealth)
	for _, repo := range board.Repos {
		health := sourceHealth("execution", repo.Repo, time.Time{}, now, 30*time.Second, false, "no_data")
		for _, items := range [][]pipeline.Item{repo.Items, repo.SkippedIssues} {
			for _, item := range items {
				for _, e := range item.Executions {
					if e.Provenance.Source != "offload_final" && e.Machine != "" && e.Machine == liveHealth.machine {
						health = liveHealth
						health.Repo = repo.Repo
						if health.ErrorCategory != "" {
							health.IncidentKey = "execution/" + repo.Repo + "/" + health.ErrorCategory
						}
					}
				}
			}
		}
		sources = append(sources, health)
	}
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
	var pool *coordinator.Board
	poolError := ""
	if WorkerPoolSource != nil {
		ctx, cancel := context.WithTimeout(r.Context(), pipelineTimeout)
		defer cancel()
		fresh, err := WorkerPoolSource(ctx)
		if err != nil {
			poolError = "Worker pool unavailable"
		} else {
			pool = &fresh
		}
	}
	writeJSON(w, http.StatusOK, struct {
		pipeline.Board
		Sources         []pipelineSourceHealth `json:"sources"`
		PilotActions    []pilot.ActionRecord   `json:"pilot_actions"`
		PilotError      string                 `json:"pilot_error,omitempty"`
		WorkerPool      *coordinator.Board     `json:"worker_pool"`
		WorkerPoolError string                 `json:"worker_pool_error,omitempty"`
	}{board, sources, actions, actionError, pool, poolError})
}

// Source health describes data observation, never the time of the HTTP request.
// A nil age/last success means no successful observation is known.
type pipelineSourceHealth struct {
	Source             string     `json:"source"`
	Repo               string     `json:"repo,omitempty"`
	LastSuccess        *time.Time `json:"last_success"`
	ObservedAt         *time.Time `json:"observed_at"`
	ObservedAgeSeconds *float64   `json:"observed_age_seconds"`
	Refreshing         bool       `json:"refreshing"`
	Stale              bool       `json:"stale"`
	ErrorCategory      string     `json:"error_category,omitempty"`
	IncidentKey        string     `json:"incident_key,omitempty"`
	machine            string
}

func sourceHealth(source, repo string, at, now time.Time, ttl time.Duration, refreshing bool, category string) pipelineSourceHealth {
	h := pipelineSourceHealth{Source: source, Repo: repo, Refreshing: refreshing, Stale: true, ErrorCategory: category}
	if !at.IsZero() {
		age := now.Sub(at).Seconds()
		if age >= -5 {
			age = max(0, age)
			h.LastSuccess, h.ObservedAt, h.ObservedAgeSeconds = &at, &at, &age
			h.Stale = age >= ttl.Seconds() || category != ""
		}
	}
	if h.LastSuccess == nil && category == "" {
		h.ErrorCategory = "no_data"
	}
	if h.ErrorCategory != "" {
		h.IncidentKey = source + "/" + repo + "/" + h.ErrorCategory
	}
	return h
}

func pipelineErrorCategory(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(message, "timeout") || strings.Contains(message, "deadline exceeded") {
		return "timeout"
	}
	if strings.Contains(message, "deferred") {
		return "deferred"
	}
	return "unavailable"
}

// Called only when installing a newer snapshot, under pipelineCache.mu. Failed
// repositories retain their previous observation even if the envelope is newer.
func rememberPipelineDelivery(snap pipeline.Snapshot) {
	observed := make(map[string]time.Time, len(snap.Repos))
	at := pipeline.ParseTime(snap.GeneratedAt)
	for _, repo := range snap.Repos {
		observed[repo.Repo] = pipelineCache.observed[repo.Repo]
		if !repo.Stale && repo.Error == "" && !at.IsZero() {
			observed[repo.Repo] = at
		}
	}
	pipelineCache.observed = observed
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
	// ReadRunsIndex reads the database when a run store is bound (file fallback otherwise).
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

// The adapter has its own bounded single-flight cache. Keep one per resolved
// command, without coupling execution liveness to the slower VCS board cache.
var pipelineLive struct {
	sync.Mutex
	command string
	reader  *execution.LiveStatsReader
}

func readPipelineLive(ctx context.Context, rows []store.Run) execution.LiveStats {
	c, err := execution.LoadConfig()
	if err != nil {
		return execution.LiveStats{State: execution.LiveUnavailable}
	}
	pipelineLive.Lock()
	if pipelineLive.reader == nil || pipelineLive.command != c.Command {
		pipelineLive.command, pipelineLive.reader = c.Command, execution.NewLiveStatsReader(c)
	}
	reader := pipelineLive.reader
	pipelineLive.Unlock()
	return reader.Read(ctx, rows)
}

// One bounded background read shares the adapter's existing capability/cache
// contract. Requests never wait for Offload, including cold starts.
var pipelineExecutionCache struct {
	sync.Mutex
	at         time.Time
	refreshing bool
	live       execution.LiveStats
}

var pipelineLiveSource = readPipelineLive

func cachedPipelineLive(rows []store.Run, now time.Time) (execution.LiveStats, bool) {
	pipelineExecutionCache.Lock()
	defer pipelineExecutionCache.Unlock()
	if !pipelineExecutionCache.refreshing && (pipelineExecutionCache.at.IsZero() || now.Sub(pipelineExecutionCache.at) >= 5*time.Second) {
		pipelineExecutionCache.refreshing, pipelineExecutionCache.at = true, now
		source := pipelineLiveSource
		pipelineWorkers.Add(1)
		go func() {
			defer pipelineWorkers.Done()
			ctx, cancel := context.WithTimeout(context.Background(), pipelineTimeout)
			defer cancel()
			live := source(ctx, rows)
			pipelineExecutionCache.Lock()
			defer pipelineExecutionCache.Unlock()
			// The adapter already preserves last successful data on fetch failure.
			// Config failures happen before that adapter and need the same behavior.
			if live.State != execution.LiveAvailable && live.GeneratedAt == nil {
				live.GeneratedAt = pipelineExecutionCache.live.GeneratedAt
				live.ObservedAt = pipelineExecutionCache.live.ObservedAt
				live.Activities = pipelineExecutionCache.live.Activities
				live.SourceMachine = pipelineExecutionCache.live.SourceMachine
				live.Stale = live.GeneratedAt != nil
			}
			pipelineExecutionCache.live, pipelineExecutionCache.refreshing = live, false
		}()
	}
	live := pipelineExecutionCache.live
	if live.SourceMachine == "" {
		live.SourceMachine, _ = os.Hostname()
	}
	return live, pipelineExecutionCache.refreshing
}

func projectPipelineExecutions(_ context.Context, board *pipeline.Board, now time.Time) pipelineSourceHealth {
	// Copy nested slices before enrichment: cached snapshots are shared readers.
	var attempts []*pipeline.ItemExecution
	var rows []store.Run
	for i := range board.Repos {
		repo := &board.Repos[i]
		repo.Items = slices.Clone(repo.Items)
		repo.SkippedIssues = slices.Clone(repo.SkippedIssues)
		for _, items := range [][]pipeline.Item{repo.Items, repo.SkippedIssues} {
			for j := range items {
				items[j].Executions = slices.Clone(items[j].Executions)
				for k := range items[j].Executions {
					e := &items[j].Executions[k]
					attempts = append(attempts, e)
					rows = append(rows, store.Run{Machine: e.Machine, SessionID: e.SessionID, StartedAt: e.StartedAt, EndedAt: e.EndedAt})
				}
			}
		}
	}
	if len(attempts) == 0 {
		return sourceHealth("execution", "", time.Time{}, now, 30*time.Second, false, "no_data")
	}
	live, refreshing := cachedPipelineLive(rows, now)
	applyPipelineLive(attempts, live, now)
	category := ""
	at := time.Time{}
	if live.GeneratedAt != nil {
		at = *live.GeneratedAt
	}
	switch live.State {
	case execution.LiveAvailable:
		if live.Stale {
			category = "stale"
		}
	case execution.LiveUnsupported:
		category = "unsupported"
	case execution.LiveUnavailable:
		category = "unavailable"
	default:
		category = "no_data"
	}
	health := sourceHealth("execution", "", at, now, 30*time.Second, refreshing, category)
	if health.LastSuccess != nil && !live.ObservedAt.IsZero() {
		health.LastSuccess = &live.ObservedAt
	}
	if live.State == execution.LiveAvailable && (live.ObservedAt.IsZero() || now.Sub(live.ObservedAt) > 30*time.Second || live.ObservedAt.After(now.Add(5*time.Second))) {
		health.Stale = true
		health.ErrorCategory = "stale"
		health.IncidentKey = "execution//stale"
	}
	health.machine = live.SourceMachine
	return health
}

// A session can contain retries. A host/session/time match is usable only when
// exactly one persisted attempt owns it; never select by repo or issue alone.
func applyPipelineLive(attempts []*pipeline.ItemExecution, live execution.LiveStats, now time.Time) {
	fresh := func(at *time.Time) bool {
		return at != nil && !at.IsZero() && !at.After(now.Add(5*time.Second)) && now.Sub(*at) <= 30*time.Second
	}
	sourceFresh := live.State == execution.LiveAvailable && !live.Stale && fresh(&live.ObservedAt) && fresh(live.GeneratedAt)
	var observedAt *time.Time
	if !live.ObservedAt.IsZero() {
		observedAt = &live.ObservedAt
	}
	for _, e := range attempts {
		if e.Provenance.Source == "offload_final" {
			continue
		}
		e.Status, e.LastProgressAt = "unknown", nil
		e.Provenance = pipeline.ExecutionProvenance{Source: "unknown", LiveState: live.State,
			SourceMachine: live.SourceMachine, ObservedAt: observedAt, GeneratedAt: live.GeneratedAt, Stale: !sourceFresh}
	}
	// Count distinct attempt identities so replayed rows cannot create ambiguity.
	matches := map[string][]execution.LiveActivity{}
	for _, a := range live.Activities {
		owners := map[string]*pipeline.ItemExecution{}
		for _, e := range attempts {
			if e.OffloadRunID == "" || e.Machine == "" || e.Machine == "unknown" || e.Machine != live.SourceMachine || a.Machine != e.Machine || e.SessionID == "" || a.Session != e.SessionID || a.StartedAt == nil || e.StartedAt.IsZero() || a.StartedAt.Before(e.StartedAt) || (e.EndedAt != nil && a.StartedAt.After(*e.EndedAt)) {
				continue
			}
			owners[e.AttemptID] = e
		}
		if len(owners) == 1 {
			for id := range owners {
				matches[id] = append(matches[id], a)
			}
		}
	}
	for _, e := range attempts {
		if e.Provenance.Source == "offload_final" {
			continue
		}
		activities := matches[e.AttemptID]
		if len(activities) != 1 {
			continue
		}
		a := activities[0]
		e.LastProgressAt = a.LastEventAt
		if !sourceFresh {
			continue
		}
		if a.Stalled != nil && *a.Stalled {
			e.Status = "stalled"
		} else if fresh(a.UpdatedAt) && a.Status != "" {
			e.Status = "running"
		} else {
			continue
		}
		e.Provenance.Source = "offload_live"
		if a.Profile != "" {
			e.Profile = a.Profile
		}
		if a.Model != "" {
			e.Model = a.Model
		}
	}
}
