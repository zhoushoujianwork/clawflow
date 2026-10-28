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
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

func resetPipeline(t *testing.T, src func(context.Context) (pipeline.Snapshot, error)) *time.Time {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "")
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	oldSrc, oldNow, oldVer := PipelineSource, pipelineNow, VersionInfo.Current
	PipelineSource, pipelineNow, VersionInfo.Current = src, func() time.Time { return now }, "v9.9.9-test"
	pipelineCache.mu.Lock()
	pipelineCache.ok = false
	pipelineCache.at = time.Time{}
	pipelineCache.snap = pipeline.Snapshot{}
	pipelineCache.err = ""
	pipelineCache.mu.Unlock()
	t.Cleanup(func() {
		pipelineWorkers.Wait()
		PipelineSource, pipelineNow, VersionInfo.Current = oldSrc, oldNow, oldVer
		pipelineCache.mu.Lock()
		pipelineCache.ok = false
		pipelineCache.at = time.Time{}
		pipelineCache.snap = pipeline.Snapshot{}
		pipelineCache.err = ""
		pipelineCache.mu.Unlock()
	})
	return &now
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
	if rec := getPipeline(t, http.MethodGet); rec.Code != http.StatusServiceUnavailable {
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
