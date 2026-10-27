package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
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
	pipelineCache.mu.Unlock()
	t.Cleanup(func() {
		PipelineSource, pipelineNow, VersionInfo.Current = oldSrc, oldNow, oldVer
		pipelineCache.mu.Lock()
		pipelineCache.ok = false
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
			Repo: "o/r", Enabled: true, Platform: "github", AutoMerge: true, QueueIssue: 9,
			Queue: &pipeline.Queue{Issue: 9, Entries: []pipeline.QueueEntry{{Position: 1, Number: 4, Title: "first", State: "open", Action: "pick", Reason: "head_eligible"}},
				Decision: &pipeline.QueueDecision{Action: "pick", Issue: 4, Reason: "head_eligible", At: "2026-10-27T11:59:00Z"}},
			Items: []pipeline.Item{{
				Issue: 4, Title: "first", Stage: pipeline.StageInfraRetry, Why: "acceptance hit an infrastructure error", Evidence: []string{"ci=success"},
				PR:         &pipeline.PRInfo{Number: 11, State: "open", HeadSHA: strings.Repeat("a", 40), CI: "success", Mergeable: "clean"},
				Acceptance: &pipeline.AcceptanceInfo{Required: true, Attempts: 1, InfraAttempts: 1, LatestIsInfra: true, NextRetryAt: "2026-10-27T12:15:00Z", PlannerSession: "plan-1", ReviewerSession: "rev-1"},
			}},
		}},
		Events: []pipeline.Event{{Time: "2026-10-27T11:59:00Z", Level: "info", Event: "run/queue_pick", Fields: map[string]string{"repo": "o/r", "issue": "4"}}},
	}
}

func TestHandlePipelineJSONShape(t *testing.T) {
	resetPipeline(t, func(context.Context) (pipeline.Snapshot, error) { return fakeSnapshot(), nil })
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
	if repo.Repo != "o/r" || repo.Queue.Decision.Action != "pick" || item.Stage != pipeline.StageInfraRetry ||
		item.PR.Number != 11 || item.Acceptance.NextRetryAt != "2026-10-27T12:15:00Z" || item.Acceptance.ReviewerSession != "rev-1" {
		t.Fatalf("board = %+v", repo)
	}
	if len(board.Events) != 1 || board.Events[0].Fields["issue"] != "4" {
		t.Fatalf("events = %+v", board.Events)
	}
	// Exact wire names the frontend depends on.
	for _, want := range []string{`"stage":"infra_retry"`, `"next_retry_at"`, `"infra_attempts":1`, `"head_sha"`, `"queue_issue":9`} {
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

func TestHandlePipelineCachesFor30Seconds(t *testing.T) {
	calls := 0
	now := resetPipeline(t, func(context.Context) (pipeline.Snapshot, error) { calls++; return fakeSnapshot(), nil })
	for i := 0; i < 3; i++ {
		getPipeline(t, http.MethodGet)
	}
	if calls != 1 {
		t.Fatalf("calls within TTL = %d, want 1", calls)
	}
	*now = now.Add(pipelineTTL - time.Second)
	getPipeline(t, http.MethodGet)
	if calls != 1 {
		t.Fatalf("calls just inside TTL = %d", calls)
	}
	*now = now.Add(2 * time.Second)
	getPipeline(t, http.MethodGet)
	if calls != 2 {
		t.Fatalf("calls after TTL = %d, want 2", calls)
	}
}

func TestHandlePipelineErrorsAreNotCached(t *testing.T) {
	calls := 0
	resetPipeline(t, func(context.Context) (pipeline.Snapshot, error) {
		calls++
		if calls == 1 {
			return pipeline.Snapshot{}, errors.New("config unreadable")
		}
		return fakeSnapshot(), nil
	})
	if rec := getPipeline(t, http.MethodGet); rec.Code != 500 || !strings.Contains(rec.Body.String(), "config unreadable") {
		t.Fatalf("first: %d %s", rec.Code, rec.Body)
	}
	if rec := getPipeline(t, http.MethodGet); rec.Code != 200 || calls != 2 {
		t.Fatalf("second: %d calls=%d", rec.Code, calls)
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
