package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/agentcapacity"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/pilot"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/webguard"
)

func resetPipeline(t *testing.T, src func(context.Context) (pipeline.Snapshot, error)) *time.Time {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "")
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	oldLiveSource := pipelineLiveSource
	resetPipelineExecutionCache()
	oldSrc, oldNow, oldVer := PipelineSource, pipelineNow, VersionInfo.Current
	PipelineSource, pipelineNow, VersionInfo.Current = src, func() time.Time { return now }, "v9.9.9-test"
	pipelineCache.mu.Lock()
	pipelineCache.ok = false
	pipelineCache.at = time.Time{}
	pipelineCache.snap = pipeline.Snapshot{}
	pipelineCache.err = ""
	pipelineCache.observed = nil
	pipelineCache.refreshing = false
	pipelineCache.mu.Unlock()
	t.Cleanup(func() {
		pipelineWorkers.Wait()
		resetPipelineExecutionCache()
		pipelineLiveSource = oldLiveSource
		PipelineSource, pipelineNow, VersionInfo.Current = oldSrc, oldNow, oldVer
		pipelineCache.mu.Lock()
		pipelineCache.ok = false
		pipelineCache.at = time.Time{}
		pipelineCache.snap = pipeline.Snapshot{}
		pipelineCache.err = ""
		pipelineCache.observed = nil
		pipelineCache.refreshing = false
		pipelineCache.mu.Unlock()
	})
	return &now
}

func resetPipelineExecutionCache() {
	pipelineExecutionCache.Lock()
	pipelineExecutionCache.at = time.Time{}
	pipelineExecutionCache.live = execution.LiveStats{}
	pipelineExecutionCache.refreshing = false
	pipelineExecutionCache.Unlock()
}

func getPipeline(t *testing.T, method string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	HandlePipeline(rec, httptest.NewRequest(method, "/api/pipeline", nil))
	return rec
}

func fakeSnapshot() pipeline.Snapshot {
	return pipeline.Snapshot{
		GeneratedAt: "2026-10-27T12:00:00Z",
		Repos: []pipeline.RepoBoard{{
			Repo: "o/r", Enabled: true, Platform: "github", AutoMerge: true,
			Items: []pipeline.Item{{
				Issue: 4, Title: "first", Stage: pipeline.StageInfraRetry, Why: "acceptance hit an infrastructure error", Evidence: []string{"ci=success"},
				PR:         &pipeline.PRInfo{Number: 11, State: "open", HeadSHA: strings.Repeat("a", 40), CI: "success", Mergeable: "clean"},
				Acceptance: &pipeline.AcceptanceInfo{Required: true, Attempts: 1, InfraAttempts: 1, LatestIsInfra: true, NextRetryAt: "2026-10-27T12:15:00Z", PlannerSession: "plan-1", ReviewerSession: "rev-1"},
			}},
		}},
		Events: []pipeline.Event{{Time: "2026-10-27T11:59:00Z", Level: "info", Event: "queue/pick", Fields: map[string]string{"repo": "o/r", "issue": "4"}}},
	}
}

func TestHandlePipelineJSONShape(t *testing.T) {
	resetPipeline(t, func(context.Context) (pipeline.Snapshot, error) { return fakeSnapshot(), nil })
	if err := snapshot.WritePipeline(fakeSnapshot()); err != nil {
		t.Fatal(err)
	}
	rec := getPipeline(t, http.MethodGet)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("status=%d type=%q body=%s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"generated_at", "repos", "events", "runtime"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("missing top-level key %q: %s", k, rec.Body)
		}
	}
	rt := raw["runtime"].(map[string]any)
	if rt["version"] != "v9.9.9-test" {
		t.Fatalf("runtime = %v", rt)
	}
	if sched := rt["scheduler"].(map[string]any); sched["status"] != "idle" {
		t.Fatalf("scheduler = %v", sched)
	}
	if exec := rt["execution"].(map[string]any); exec["ready"] != false || exec["error"] == "" {
		t.Fatalf("execution = %v", exec)
	}

	var board pipeline.Board
	if err := json.Unmarshal(rec.Body.Bytes(), &board); err != nil {
		t.Fatal(err)
	}
	repo := board.Repos[0]
	item := repo.Items[0]
	if repo.Repo != "o/r" || item.Stage != pipeline.StageInfraRetry ||
		item.PR.Number != 11 || item.Acceptance.NextRetryAt != "2026-10-27T12:15:00Z" || item.Acceptance.ReviewerSession != "rev-1" {
		t.Fatalf("board = %+v", repo)
	}
	if len(board.Events) != 1 || board.Events[0].Fields["issue"] != "4" {
		t.Fatalf("events = %+v", board.Events)
	}
	// Exact wire names the frontend depends on.
	for _, want := range []string{`"stage":"infra_retry"`, `"next_retry_at"`, `"infra_attempts":1`, `"head_sha"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("body lacks %s: %s", want, rec.Body)
		}
	}
}

func TestHandlePipelineEmptyRepoListIsArray(t *testing.T) {
	resetPipeline(t, func(context.Context) (pipeline.Snapshot, error) {
		return pipeline.Snapshot{Repos: []pipeline.RepoBoard{}, Events: []pipeline.Event{}}, nil
	})
	body := getPipeline(t, http.MethodGet).Body.String()
	if !strings.Contains(body, `"repos":[]`) || !strings.Contains(body, `"events":[]`) {
		t.Fatalf("body = %s", body)
	}
}

func TestHandlePipelineStaleSingleFlightLatency(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	now := resetPipeline(t, func(ctx context.Context) (pipeline.Snapshot, error) {
		calls.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return fakeSnapshot(), nil
	})
	*now = now.Add(time.Minute)
	if err := snapshot.WritePipeline(fakeSnapshot()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			rec := getPipeline(t, http.MethodGet)
			if time.Since(start) >= 200*time.Millisecond {
				t.Error("snapshot response exceeded 200ms")
			}
			var b pipeline.Board
			if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil || !b.Stale || !b.Refreshing || len(b.Repos) != 1 {
				t.Errorf("response: %s", rec.Body)
			}
		}()
	}
	wg.Wait()
	close(release)
	pipelineWorkers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh calls=%d", calls.Load())
	}
}

func TestHandlePipelineRefreshFailurePreservesSnapshot(t *testing.T) {
	now := resetPipeline(t, func(context.Context) (pipeline.Snapshot, error) {
		return pipeline.Snapshot{}, errors.New("provider unavailable")
	})
	*now = now.Add(time.Minute)
	if err := snapshot.WritePipeline(fakeSnapshot()); err != nil {
		t.Fatal(err)
	}
	getPipeline(t, http.MethodGet)
	pipelineWorkers.Wait()
	rec := getPipeline(t, http.MethodGet)
	var b pipeline.Board
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil || !b.Stale || b.Error == "" || len(b.Repos) != 1 {
		t.Fatalf("response: %s", rec.Body)
	}
}

func TestHandlePipelineUsesNewerSchedulerSnapshot(t *testing.T) {
	resetPipeline(t, nil)
	if err := snapshot.WritePipeline(fakeSnapshot()); err != nil {
		t.Fatal(err)
	}
	getPipeline(t, http.MethodGet)
	fresh := fakeSnapshot()
	fresh.GeneratedAt = "2026-10-27T12:00:10Z"
	fresh.Repos[0].Repo = "new/repo"
	if err := snapshot.WritePipeline(fresh); err != nil {
		t.Fatal(err)
	}
	if rec := getPipeline(t, http.MethodGet); !strings.Contains(rec.Body.String(), "new/repo") {
		t.Fatal(rec.Body)
	}
}

func TestHandlePipelineIsReadOnlyAndNeedsSource(t *testing.T) {
	resetPipeline(t, func(context.Context) (pipeline.Snapshot, error) { return fakeSnapshot(), nil })
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if rec := getPipeline(t, m); rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s -> %d", m, rec.Code)
		}
	}
	PipelineSource = nil
	if rec := getPipeline(t, http.MethodGet); rec.Code != http.StatusOK {
		t.Fatalf("no source -> %d", rec.Code)
	}
}

func TestHandlePipelineAgentSlotsStayFresh(t *testing.T) {
	calls := 0
	resetPipeline(t, func(context.Context) (pipeline.Snapshot, error) {
		calls++
		return fakeSnapshot(), nil
	})
	cfg := &config.Config{Settings: config.Settings{MaxConcurrentAgents: 2}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	check := func(used int) {
		t.Helper()
		rec := getPipeline(t, http.MethodGet)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
		var board struct {
			Runtime struct {
				Scheduler map[string]json.RawMessage `json:"scheduler"`
			} `json:"runtime"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &board); err != nil {
			t.Fatal(err)
		}
		for key, want := range map[string]int{"agent_slots_used": used, "agent_slots_total": 2} {
			value, ok := board.Runtime.Scheduler[key]
			if !ok {
				t.Fatalf("missing %s: %s", key, rec.Body)
			}
			var got int
			if err := json.Unmarshal(value, &got); err != nil || got != want {
				t.Fatalf("%s=%s, want %d (decode error: %v)", key, value, want, err)
			}
		}
	}
	check(0)
	release, err := agentcapacity.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	check(1)
	release()
	check(0)
	if calls != 1 {
		t.Fatalf("snapshot calls=%d, want cached snapshot", calls)
	}
}

func TestPipelineRuntimePassTiming(t *testing.T) {
	resetPipeline(t, func(context.Context) (pipeline.Snapshot, error) { return fakeSnapshot(), nil })
	runMu.Lock()
	oldStarts, oldActive, oldResult, oldAt := activeRunStarts, activeRuns, lastPassResult, lastPassAt
	started := time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)
	activeRunStarts = map[string]time.Time{"o/r": started}
	activeRuns = map[string]bool{"o/r": true}
	lastPassResult, lastPassAt = "error", started.Add(-time.Minute)
	runMu.Unlock()
	t.Cleanup(func() {
		runMu.Lock()
		activeRunStarts, activeRuns, lastPassResult, lastPassAt = oldStarts, oldActive, oldResult, oldAt
		runMu.Unlock()
	})
	rt := pipelineRuntime()
	if rt.Scheduler.Status != "running" || rt.Scheduler.StartedAt != started.Format(time.RFC3339) || rt.Scheduler.LastResult != "error" || rt.Scheduler.LastEndedAt == "" {
		t.Fatalf("runtime=%+v", rt)
	}
}

// executionFixtures are run metadata records shared by the selection, file and
// PostgreSQL tests; each is the meta.json a run would persist.
var executionFixtures = []struct {
	name                          string
	records                       []string
	profile, backend, model, tier string
}{
	{"latest result", []string{
		`{"repo":"o/r","operator":"implement","started_at":"2026-10-28T10:00:00Z","ended_at":"2026-10-28T10:01:00Z","status":"success","development_session":{"profile":"configured-other","backend":"old"}}`,
		`{"repo":"o/r","operator":"implement","started_at":"2026-10-28T09:00:00Z","ended_at":"2026-10-28T11:00:00Z","status":"success","development_session":{"profile":"actual","backend":"codex"},"model":"recorded-model","service_tier":"priority"}`,
		`{"repo":"o/r","operator":"implement","started_at":"2026-10-28T12:00:00Z","status":"running","development_session":{"profile":"running","backend":"claude"}}`,
	}, "actual", "codex", "recorded-model", "priority"},
	{"missing optional fields do not inherit", []string{
		`{"repo":"o/r","operator":"implement","started_at":"2026-10-28T10:00:00Z","ended_at":"2026-10-28T10:30:00Z","status":"success","development_session":{"profile":"old","backend":"old"},"model":"old-model","service_tier":"priority"}`,
		`{"repo":"o/r","operator":"implement","started_at":"2026-10-28T11:00:00Z","ended_at":"2026-10-28T11:30:00Z","status":"success","development_session":{"profile":"new","backend":"claude"}}`,
	}, "new", "claude", "", ""},
	{"recorded model usage", []string{
		`{"repo":"o/r","operator":"implement","started_at":"2026-10-28T11:00:00Z","ended_at":"2026-10-28T11:10:00Z","status":"success","development_session":{"profile":"actual","backend":"codex"},"usage":{"model_usage":{"model-z":{},"model-a":{}}}}`,
	}, "actual", "codex", "model-a, model-z", ""},
	{"empty history", nil, "", "", "", ""},
}

func assertExecution(t *testing.T, ex pipeline.Execution, profile, backend, model, tier string) {
	t.Helper()
	raw, _ := json.Marshal(ex)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"profile": profile, "backend": backend, "model": model, "service_tier": tier} {
		if want == "" {
			if _, ok := got[key]; ok {
				t.Errorf("%s should be omitted: %s", key, raw)
			}
		} else if got[key] != want {
			t.Errorf("%s = %v, want %s", key, got[key], want)
		}
	}
}

func TestLatestExecutionSelection(t *testing.T) {
	for _, tc := range executionFixtures {
		t.Run(tc.name, func(t *testing.T) {
			var runs []snapshot.RunIndexEntry
			for _, rec := range tc.records {
				var e snapshot.RunIndexEntry
				if err := json.Unmarshal([]byte(rec), &e); err != nil {
					t.Fatal(err)
				}
				runs = append(runs, e)
			}
			assertExecution(t, latestExecution(runs), tc.profile, tc.backend, tc.model, tc.tier)
		})
	}
}

// Without a bound run store ReadRunsIndex reads data/runs/*/meta.json.
func TestPipelineRuntimeUsesLatestRecordedExecutionFromFiles(t *testing.T) {
	for _, tc := range executionFixtures {
		t.Run(tc.name, func(t *testing.T) {
			resetPipeline(t, func(context.Context) (pipeline.Snapshot, error) { return fakeSnapshot(), nil })
			t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "configured-other")
			snapshot.BindRunStore(nil)
			for i, rec := range tc.records {
				dir := filepath.Join(snapshot.DataDir(), "runs", "o__r", "issue-1", "run-"+string(rune('a'+i)))
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(rec), 0600); err != nil {
					t.Fatal(err)
				}
			}
			assertExecution(t, pipelineRuntime().Execution, tc.profile, tc.backend, tc.model, tc.tier)
		})
	}
}

// Same fixtures through the PostgreSQL runs table (skipped without a test DSN).
func TestPipelineRuntimeUsesLatestRecordedExecutionFromDB(t *testing.T) {
	for _, tc := range executionFixtures {
		t.Run(tc.name, func(t *testing.T) {
			s := historyDB(t)
			resetPipeline(t, func(context.Context) (pipeline.Snapshot, error) { return fakeSnapshot(), nil })
			t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "configured-other")
			for i, rec := range tc.records {
				dir := filepath.Join(snapshot.DataDir(), "runs", "o__r", "issue-1", "run-"+string(rune('a'+i)))
				if _, err := s.UpsertRun(context.Background(), dir, "run", []byte(rec)); err != nil {
					t.Fatal(err)
				}
			}
			assertExecution(t, pipelineRuntime().Execution, tc.profile, tc.backend, tc.model, tc.tier)
		})
	}
}

func TestPipelinePilotAudit(t *testing.T) {
	resetPipeline(t, nil)
	if err := snapshot.WritePipeline(fakeSnapshot()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(snapshot.DataDir(), "pilot-actions.jsonl")
	records := []pilot.ActionRecord{
		{RunID: "wake", Action: pilot.Action{ID: "question", Type: "ask_human", Repo: "o/r", Issue: 4, Body: "Which environment?"}, Outcome: "pending"},
		{RunID: "wake", Action: pilot.Action{ID: "question", Type: "ask_human", Repo: "o/r", Issue: 4, Body: "Which environment?"}, Outcome: "applied"},
		{RunID: "wake", Action: pilot.Action{ID: "merge", Type: "override_merge", Repo: "o/r", PR: 11, Head: strings.Repeat("a", 40)}, Outcome: "head_changed"},
		{RunID: "other", Action: pilot.Action{ID: "hidden", Repo: "other/repo"}, Outcome: "applied"},
	}
	var data []byte
	for _, r := range records {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, append(b, '\n')...)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var board struct {
		PilotActions []pilot.ActionRecord `json:"pilot_actions"`
		PilotError   string               `json:"pilot_error"`
	}
	response := getPipeline(t, http.MethodGet)
	if err := json.Unmarshal(response.Body.Bytes(), &board); err != nil {
		t.Fatal(err)
	}
	if len(board.PilotActions) != 2 || board.PilotActions[0].Outcome != "applied" || board.PilotActions[0].Action.Body != "Which environment?" || board.PilotActions[1].Action.PR != 11 || board.PilotActions[1].Outcome != "head_changed" {
		t.Fatalf("audit: %+v", board)
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	response = getPipeline(t, http.MethodGet)
	if err := json.Unmarshal(response.Body.Bytes(), &board); err != nil {
		t.Fatal(err)
	}
	if len(board.PilotActions) != 0 || board.PilotError == "" {
		t.Fatalf("unreadable audit: %+v", board)
	}
}

func TestPipelineWorkerPoolLANContract(t *testing.T) {
	now := resetPipeline(t, nil)
	pipelineCache.mu.Lock()
	pipelineCache.ok = true
	pipelineCache.snap = pipeline.Snapshot{GeneratedAt: now.Format(time.RFC3339), Repos: []pipeline.RepoBoard{}}
	pipelineCache.mu.Unlock()
	old := WorkerPoolSource
	t.Cleanup(func() { WorkerPoolSource = old })
	pool := coordinator.Board{GeneratedAt: *now, Workers: []coordinator.BoardWorker{{Name: "worker", Tags: []string{"macos"}, Roles: []string{"operator"}, Live: true, LastSeen: now, LastHeartbeat: now, RunningTasks: []string{"task"}}}, Tasks: []coordinator.BoardTask{{ID: "task", State: "leased", Stage: "running", Tags: []string{}, Priority: 2, Worker: "worker", LeaseExpiresAt: now, Attempt: 1}}, Throughput: coordinator.Throughput{Since: now.Add(-time.Hour), Done: 2, Failed: 1}}
	WorkerPoolSource = func(ctx context.Context) (coordinator.Board, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("missing request timeout")
		}
		return pool, nil
	}
	policy, err := webguard.NewRemotePolicy("readonly", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := policy.Middleware(http.HandlerFunc(HandlePipeline))
	request := func(method string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/pipeline", nil)
		r.RemoteAddr = "192.168.1.2:12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request("GET")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var decoded map[string]json.RawMessage
	if err = json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	expected, _ := json.Marshal(pool)
	if string(decoded["worker_pool"]) != string(expected) {
		t.Fatalf("pool: %s", decoded["worker_pool"])
	}
	for _, key := range []string{`"name"`, `"tags"`, `"roles"`, `"live"`, `"last_seen"`, `"last_heartbeat"`, `"running_tasks"`, `"state"`, `"stage"`, `"priority"`, `"worker"`, `"lease_expires_at"`, `"attempt"`, `"throughput"`, `"done"`, `"failed"`} {
		if !strings.Contains(string(decoded["worker_pool"]), key) {
			t.Fatalf("missing %s", key)
		}
	}
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		if got := request(method).Code; got != 403 {
			t.Fatalf("%s: %d", method, got)
		}
	}
	WorkerPoolSource = func(context.Context) (coordinator.Board, error) { return coordinator.Board{}, errors.New("secret DSN") }
	w = request("GET")
	if !strings.Contains(w.Body.String(), `"worker_pool_error":"Worker pool unavailable"`) || strings.Contains(w.Body.String(), "secret DSN") {
		t.Fatalf("error: %s", w.Body)
	}
	WorkerPoolSource = nil
	w = request("GET")
	if !strings.Contains(w.Body.String(), `"worker_pool":null`) || strings.Contains(w.Body.String(), "worker_pool_error") {
		t.Fatalf("disabled: %s", w.Body)
	}
}

func TestPipelineRepositoryHostingPlatform(t *testing.T) {
	resetPipeline(t, nil)
	if err := snapshot.WritePipeline(fakeSnapshot()); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Settings: config.Settings{Coordinator: config.CoordinatorSettings{Enabled: true}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://platform.example:8090/api/pipeline", nil)
	response := httptest.NewRecorder()
	HandlePipeline(response, request)
	var board pipeline.Board
	if err := json.Unmarshal(response.Body.Bytes(), &board); err != nil {
		t.Fatal(err)
	}
	if len(board.Repos) != 1 || board.Repos[0].HostingPlatform != "https://platform.example:8093" {
		t.Fatalf("missing platform: %s", response.Body.String())
	}
}

func TestPipelineLiveAttemptIdentityAndFreshness(t *testing.T) {
	now := time.Date(2026, 10, 29, 20, 0, 0, 0, time.UTC)
	start, old := now.Add(-time.Minute), now.Add(-time.Hour)
	activityStart := start.Add(time.Second)
	yes := true
	base := pipeline.ItemExecution{AttemptID: "local/1", OffloadRunID: "1", SessionID: "session", Machine: "local", StartedAt: start, Status: "unknown"}
	activity := execution.LiveActivity{Session: "session", Machine: "local", Status: "working", StartedAt: &activityStart, UpdatedAt: &now, LastEventAt: &now}
	for _, tc := range []struct {
		name string
		edit func(*execution.LiveStats, *[]pipeline.ItemExecution)
		want string
	}{
		{"fresh", func(_ *execution.LiveStats, _ *[]pipeline.ItemExecution) {}, "running"},
		{"stalled", func(l *execution.LiveStats, _ *[]pipeline.ItemExecution) {
			l.Activities[0].Stalled = &yes
			l.Activities[0].LastEventAt = &old
		}, "stalled"},
		{"stale cache", func(l *execution.LiveStats, _ *[]pipeline.ItemExecution) { l.Stale = true }, "unknown"},
		{"old source", func(l *execution.LiveStats, _ *[]pipeline.ItemExecution) { l.GeneratedAt = &old }, "unknown"},
		{"old activity", func(l *execution.LiveStats, _ *[]pipeline.ItemExecution) { l.Activities[0].UpdatedAt = &old }, "unknown"},
		{"missing Offload", func(l *execution.LiveStats, _ *[]pipeline.ItemExecution) {
			l.State = execution.LiveUnavailable
			l.Activities = nil
		}, "unknown"},
		{"unsupported Offload", func(l *execution.LiveStats, _ *[]pipeline.ItemExecution) { l.State = execution.LiveUnsupported }, "unknown"},
		{"remote worker", func(_ *execution.LiveStats, e *[]pipeline.ItemExecution) { (*e)[0].Machine = "remote" }, "unknown"},
		{"missing machine", func(_ *execution.LiveStats, e *[]pipeline.ItemExecution) { (*e)[0].Machine = "" }, "unknown"},
		{"legacy session only", func(_ *execution.LiveStats, e *[]pipeline.ItemExecution) { (*e)[0].OffloadRunID = "" }, "unknown"},
		{"same session different machines", func(_ *execution.LiveStats, e *[]pipeline.ItemExecution) {
			r := base
			r.Machine = "remote"
			r.AttemptID = "remote/1"
			*e = append(*e, r)
		}, "running"},
		{"concurrent roles", func(_ *execution.LiveStats, e *[]pipeline.ItemExecution) {
			r := base
			r.AttemptID = "local/2"
			r.OffloadRunID = "2"
			r.Role = "acceptance"
			*e = append(*e, r)
		}, "unknown"},
		{"session resumed outside interval", func(l *execution.LiveStats, e *[]pipeline.ItemExecution) {
			(*e)[0].EndedAt = &old
			l.Activities[0].StartedAt = &now
		}, "unknown"},
		{"duplicate activity", func(l *execution.LiveStats, _ *[]pipeline.ItemExecution) {
			l.Activities = append(l.Activities, activity)
		}, "unknown"},
		{"final wins", func(_ *execution.LiveStats, e *[]pipeline.ItemExecution) {
			(*e)[0].Status = "ok"
			(*e)[0].Provenance.Source = "offload_final"
		}, "ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live := execution.LiveStats{State: execution.LiveAvailable, SourceMachine: "local", ObservedAt: now, GeneratedAt: &now, Activities: []execution.LiveActivity{activity}}
			entries := []pipeline.ItemExecution{base}
			tc.edit(&live, &entries)
			ptrs := make([]*pipeline.ItemExecution, len(entries))
			for i := range entries {
				ptrs[i] = &entries[i]
			}
			applyPipelineLive(ptrs, live, now)
			if entries[0].Status != tc.want {
				t.Fatalf("got %+v want %s", entries[0], tc.want)
			}
			if len(entries) > 1 && entries[1].Status != "unknown" {
				t.Fatalf("arbitrary concurrent attempt selected: %+v", entries)
			}
			if entries[0].ElapsedMs != nil || entries[0].ExitCode != nil {
				t.Fatal("invented metrics")
			}
		})
	}
}

func TestPipelineLiveDistinctRoleSessions(t *testing.T) {
	now := time.Now().UTC()
	start := now.Add(-time.Minute)
	activeStart := start.Add(time.Second)
	attempts := []pipeline.ItemExecution{
		{AttemptID: "a", OffloadRunID: "a", SessionID: "dev", Machine: "local", Role: "operator", StartedAt: start},
		{AttemptID: "b", OffloadRunID: "b", SessionID: "review", Machine: "local", Role: "acceptance", StartedAt: start},
	}
	live := execution.LiveStats{State: execution.LiveAvailable, SourceMachine: "local", ObservedAt: now, GeneratedAt: &now}
	for _, e := range attempts {
		live.Activities = append(live.Activities, execution.LiveActivity{Machine: "local", Session: e.SessionID, Status: "working", StartedAt: &activeStart, UpdatedAt: &now})
	}
	applyPipelineLive([]*pipeline.ItemExecution{&attempts[0], &attempts[1]}, live, now)
	for _, e := range attempts {
		if e.Status != "running" || e.Provenance.Source != "offload_live" {
			t.Fatalf("%+v", attempts)
		}
	}
	// A subsequent failed read must clear live status, never reuse it as a final.
	live.State, live.Stale = execution.LiveUnavailable, true
	applyPipelineLive([]*pipeline.ItemExecution{&attempts[0], &attempts[1]}, live, now)
	for _, e := range attempts {
		if e.Status != "unknown" {
			t.Fatalf("stale active execution: %+v", e)
		}
	}
}

func TestProjectPipelineExecutionsUsesAdapterWithoutMutatingCache(t *testing.T) {
	resetPipeline(t, nil)
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "test")
	now := time.Now().UTC()
	start := now.Add(-time.Minute)
	activeStart := start.Add(time.Second)
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"schema_version": 1, "generated_at": now, "active": []map[string]any{
		{"phase": "working", "session": "managed", "started_at": activeStart, "updated_at": now, "last_event_at": now, "model": "model-live", "task": "PRIVATE PROMPT"},
		{"phase": "working", "session": "personal", "started_at": activeStart, "updated_at": now, "task": "PERSONAL TASK"},
	}}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(t.TempDir(), "offload")
	script := "#!/bin/sh\nif [ \"$1\" = -h ]; then printf 'usage:\\n  offload stats [--json]\\n'; exit 0; fi\nif [ \"$#\" = 2 ] && [ \"$1\" = stats ] && [ \"$2\" = --json ]; then cat <<'JSON'\n" + string(raw) + "\nJSON\nexit 0\nfi\nexit 91\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAWFLOW_OFFLOAD_COMMAND", command)
	cached := []pipeline.Item{{Issue: 1, Stage: pipeline.StageCIPending, Executions: []pipeline.ItemExecution{{AttemptID: "a", OffloadRunID: "a", SessionID: "managed", Machine: host, StartedAt: start, Status: "unknown", Provenance: pipeline.ExecutionProvenance{Source: "unknown"}}}}}
	board := pipeline.Board{Snapshot: pipeline.Snapshot{Repos: []pipeline.RepoBoard{{Repo: "o/r", Items: cached}}}}
	projectPipelineExecutions(context.Background(), &board, now)
	pipelineWorkers.Wait()
	projectPipelineExecutions(context.Background(), &board, now)
	got := board.Repos[0].Items[0]
	if got.Executions[0].Status != "running" || got.Executions[0].Model != "model-live" || got.Stage != pipeline.StageCIPending || got.Delivery != nil {
		t.Fatalf("got %+v", got)
	}
	if cached[0].Executions[0].Status != "unknown" || cached[0].Executions[0].Model != "" {
		t.Fatal("mutated cached snapshot")
	}
	encoded, err := json.Marshal(board)
	if err != nil || strings.Contains(string(encoded), "PRIVATE") || strings.Contains(string(encoded), "PERSONAL") || strings.Contains(string(encoded), "personal") {
		t.Fatalf("leaked task: %s %v", encoded, err)
	}
}

func pipelineResponse(t *testing.T) (pipeline.Board, []pipelineSourceHealth) {
	t.Helper()
	rec := getPipeline(t, http.MethodGet)
	var body struct {
		pipeline.Board
		Sources []pipelineSourceHealth `json:"sources"`
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Board, body.Sources
}

func findPipelineHealth(t *testing.T, sources []pipelineSourceHealth, source, repo string) pipelineSourceHealth {
	t.Helper()
	for _, h := range sources {
		if h.Source == source && h.Repo == repo {
			return h
		}
	}
	t.Fatalf("missing health %s/%s: %+v", source, repo, sources)
	return pipelineSourceHealth{}
}

func executionSnapshot(t *testing.T, now time.Time) (pipeline.Snapshot, execution.LiveStats) {
	t.Helper()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	start, activeStart := now.Add(-time.Minute), now.Add(-30*time.Second)
	snap := fakeSnapshot()
	snap.GeneratedAt = now.Format(time.RFC3339Nano)
	snap.Repos[0].Items[0].Executions = []pipeline.ItemExecution{{AttemptID: "attempt", OffloadRunID: "run", Machine: host, SessionID: "session", StartedAt: start, Provenance: pipeline.ExecutionProvenance{Source: "unknown"}}}
	live := execution.LiveStats{State: execution.LiveAvailable, SourceMachine: host, GeneratedAt: &now, ObservedAt: now, Activities: []execution.LiveActivity{{Machine: host, Session: "session", StartedAt: &activeStart, UpdatedAt: &now, Status: "working"}}}
	return snap, live
}

func TestPipelineIndependentSourcesAndRecovery(t *testing.T) {
	for _, failed := range []string{"delivery", "execution"} {
		t.Run(failed, func(t *testing.T) {
			now := resetPipeline(t, nil)
			snap, live := executionSnapshot(t, *now)
			original := *now
			if err := snapshot.WritePipeline(snap); err != nil {
				t.Fatal(err)
			}
			pipelineLiveSource = func(context.Context, []store.Run) execution.LiveStats { return live }
			pipelineResponse(t)
			pipelineWorkers.Wait()
			board, _ := pipelineResponse(t)
			if board.Repos[0].Items[0].Executions[0].Status != "running" {
				t.Fatal("warm execution not running")
			}

			*now = now.Add(21 * time.Second)
			failVCS := failed == "delivery"
			PipelineSource = func(context.Context) (pipeline.Snapshot, error) {
				if failVCS {
					return pipeline.Snapshot{}, context.DeadlineExceeded
				}
				fresh := snap
				fresh.GeneratedAt = now.Format(time.RFC3339Nano)
				return fresh, nil
			}
			if failed == "execution" {
				pipelineLiveSource = func(context.Context, []store.Run) execution.LiveStats {
					return execution.LiveStats{State: execution.LiveUnavailable}
				}
			} else {
				_, live = executionSnapshot(t, *now)
			}
			pipelineResponse(t)
			pipelineWorkers.Wait()
			board, sources := pipelineResponse(t)
			delivery := findPipelineHealth(t, sources, "delivery", "o/r")
			executor := findPipelineHealth(t, sources, "execution", "o/r")
			broken, healthy := delivery, executor
			if failed == "execution" {
				broken, healthy = executor, delivery
			}
			if broken.LastSuccess == nil || !broken.LastSuccess.Equal(original) || broken.ObservedAgeSeconds == nil || *broken.ObservedAgeSeconds != 21 || !broken.Stale || broken.Refreshing || broken.ErrorCategory == "" || broken.IncidentKey == "" {
				t.Fatalf("failed source metadata %+v", broken)
			}
			if healthy.Stale || healthy.ErrorCategory != "" || healthy.LastSuccess == nil || !healthy.LastSuccess.Equal(*now) {
				t.Fatalf("healthy source %+v", healthy)
			}
			if failed == "delivery" && board.Repos[0].Items[0].Executions[0].Status != "running" {
				t.Fatal("VCS timeout made live execution unknown")
			}
			if failed == "execution" && (board.Stale || board.Error != "" || board.Repos[0].Items[0].PR.Number != 11) {
				t.Fatal("Offload failure affected delivery")
			}
			incident := broken.IncidentKey
			*now = now.Add(21 * time.Second)
			pipelineResponse(t)
			pipelineWorkers.Wait()
			_, sources = pipelineResponse(t)
			broken = findPipelineHealth(t, sources, failed, "o/r")
			if broken.IncidentKey != incident || broken.LastSuccess == nil || !broken.LastSuccess.Equal(original) {
				t.Fatalf("outage changed identity/time %+v", broken)
			}

			// Recovery advances only to the source's new observation timestamp.
			failVCS = false
			*now = now.Add(21 * time.Second)
			_, live = executionSnapshot(t, *now)
			pipelineLiveSource = func(context.Context, []store.Run) execution.LiveStats { return live }
			pipelineResponse(t)
			pipelineWorkers.Wait()
			_, sources = pipelineResponse(t)
			for _, source := range []string{"delivery", "execution"} {
				h := findPipelineHealth(t, sources, source, "o/r")
				if h.Stale || h.ErrorCategory != "" || h.IncidentKey != "" || h.LastSuccess == nil || !h.LastSuccess.Equal(*now) {
					t.Fatalf("recovery %+v", h)
				}
			}
		})
	}
}

func TestPipelineBothRefreshesAreBoundedAndNonblocking(t *testing.T) {
	now := resetPipeline(t, nil)
	snap, live := executionSnapshot(t, *now)
	if err := snapshot.WritePipeline(snap); err != nil {
		t.Fatal(err)
	}
	pipelineLiveSource = func(context.Context, []store.Run) execution.LiveStats { return live }
	pipelineResponse(t)
	pipelineWorkers.Wait()
	*now = now.Add(21 * time.Second)
	_, live = executionSnapshot(t, *now)
	release := make(chan struct{})
	var vcsCalls, liveCalls atomic.Int32
	PipelineSource = func(ctx context.Context) (pipeline.Snapshot, error) {
		vcsCalls.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return pipeline.Snapshot{}, context.DeadlineExceeded
	}
	pipelineLiveSource = func(ctx context.Context, _ []store.Run) execution.LiveStats {
		liveCalls.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return live
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			_, sources := pipelineResponse(t)
			if time.Since(start) >= 200*time.Millisecond {
				t.Error("cached handler exceeded 200ms")
			}
			for _, source := range []string{"delivery", "execution"} {
				if !findPipelineHealth(t, sources, source, "o/r").Refreshing {
					t.Errorf("%s not refreshing", source)
				}
			}
		}()
	}
	wg.Wait()
	close(release)
	pipelineWorkers.Wait()
	if vcsCalls.Load() != 1 || liveCalls.Load() != 1 {
		t.Fatalf("vcs=%d offload=%d", vcsCalls.Load(), liveCalls.Load())
	}
}

func TestPipelineColdFailureMetadata(t *testing.T) {
	now := resetPipeline(t, nil)
	_, sources := pipelineResponse(t)
	for _, h := range sources {
		if h.LastSuccess != nil || h.ObservedAgeSeconds != nil || h.Refreshing || !h.Stale || h.ErrorCategory != "no_data" {
			t.Fatalf("cold metadata %+v", h)
		}
	}
	PipelineSource = func(context.Context) (pipeline.Snapshot, error) {
		return pipeline.Snapshot{}, errors.New("private provider diagnostics")
	}
	pipelineResponse(t)
	pipelineWorkers.Wait()
	_, sources = pipelineResponse(t)
	h := findPipelineHealth(t, sources, "delivery", "")
	if h.LastSuccess != nil || h.ObservedAgeSeconds != nil || h.ErrorCategory != "unavailable" || h.Refreshing {
		t.Fatalf("cold failure %+v", h)
	}
	*now = now.Add(21 * time.Second)
	PipelineSource = func(context.Context) (pipeline.Snapshot, error) { return fakeSnapshot(), nil }
	pipelineResponse(t)
	pipelineWorkers.Wait()
	_, sources = pipelineResponse(t)
	h = findPipelineHealth(t, sources, "delivery", "o/r")
	if h.LastSuccess == nil || *h.ObservedAgeSeconds != 21 || h.ErrorCategory != "" {
		t.Fatalf("observation replaced with request time %+v", h)
	}
}

func TestPipelinePartialRepoObservation(t *testing.T) {
	now := resetPipeline(t, nil)
	snap := fakeSnapshot()
	sibling := snap.Repos[0]
	sibling.Repo = "o/sibling"
	snap.Repos = append(snap.Repos, sibling)
	if err := snapshot.WritePipeline(snap); err != nil {
		t.Fatal(err)
	}
	pipelineResponse(t)
	*now = now.Add(21 * time.Second)
	snap.GeneratedAt = now.Format(time.RFC3339Nano)
	snap.Repos[0].Stale, snap.Repos[0].Error = true, "VCS refresh: context deadline exceeded"
	if err := snapshot.WritePipeline(snap); err != nil {
		t.Fatal(err)
	}
	_, sources := pipelineResponse(t)
	failed := findPipelineHealth(t, sources, "delivery", "o/r")
	healthy := findPipelineHealth(t, sources, "delivery", "o/sibling")
	global := findPipelineHealth(t, sources, "delivery", "")
	if !global.Stale || global.LastSuccess == nil || !global.LastSuccess.Equal(*failed.LastSuccess) || global.ErrorCategory != "timeout" {
		t.Fatalf("global delivery observation %+v", global)
	}
	if failed.ObservedAgeSeconds == nil || *failed.ObservedAgeSeconds != 21 || failed.ErrorCategory != "timeout" || !failed.Stale {
		t.Fatalf("failed repo %+v", failed)
	}
	if healthy.Stale || healthy.ObservedAgeSeconds == nil || *healthy.ObservedAgeSeconds != 0 {
		t.Fatalf("healthy repo %+v", healthy)
	}
}

func TestPipelineColdOffloadFailureAndObservationTime(t *testing.T) {
	now := resetPipeline(t, nil)
	snap, live := executionSnapshot(t, *now)
	if err := snapshot.WritePipeline(snap); err != nil {
		t.Fatal(err)
	}
	pipelineLiveSource = func(context.Context, []store.Run) execution.LiveStats {
		// A failed probe's timestamp is not a successful data observation.
		return execution.LiveStats{State: execution.LiveUnavailable, ObservedAt: *now}
	}
	_, sources := pipelineResponse(t)
	cold := findPipelineHealth(t, sources, "execution", "o/r")
	if !cold.Refreshing || cold.LastSuccess != nil || cold.ObservedAgeSeconds != nil || cold.ErrorCategory != "no_data" {
		t.Fatalf("cold %+v", cold)
	}
	pipelineWorkers.Wait()
	_, sources = pipelineResponse(t)
	failed := findPipelineHealth(t, sources, "execution", "o/r")
	if failed.LastSuccess != nil || failed.ObservedAt != nil || failed.ObservedAgeSeconds != nil || failed.ErrorCategory != "unavailable" || failed.Refreshing {
		t.Fatalf("failure %+v", failed)
	}
	*now = now.Add(6 * time.Second)
	live.ObservedAt = live.ObservedAt.Add(2 * time.Second)
	pipelineLiveSource = func(context.Context, []store.Run) execution.LiveStats { return live }
	pipelineResponse(t)
	pipelineWorkers.Wait()
	// Adapter receipt and data generation are distinct and neither is request time.
	*now = now.Add(time.Second)
	_, sources = pipelineResponse(t)
	recovered := findPipelineHealth(t, sources, "execution", "o/r")
	if recovered.Stale || recovered.ErrorCategory != "" || recovered.ObservedAgeSeconds == nil || *recovered.ObservedAgeSeconds != 7 || recovered.LastSuccess == nil || !recovered.LastSuccess.Equal(live.ObservedAt) || recovered.ObservedAt == nil || !recovered.ObservedAt.Equal(*live.GeneratedAt) {
		t.Fatalf("recovery %+v", recovered)
	}
}
