package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

func historyDB(t *testing.T) *store.Store {
	t.Helper()
	s := storetest.New(t)
	if _, err := s.Migrate(context.Background(), store.Migrations); err != nil {
		t.Fatal(err)
	}
	snapshot.BindRunStore(s)
	t.Cleanup(func() { snapshot.BindRunStore(nil) })
	return s
}
func TestHistoryEndpointsUseSQL(t *testing.T) {
	s := historyDB(t)
	t.Setenv("HOME", t.TempDir())
	raw := []byte(`{"repo":"nil/only","operator":"implement","status":"success","started_at":"2026-10-28T00:00:00Z"}`)
	if _, err := s.UpsertRun(context.Background(), filepath.Join(snapshot.DataDir(), "runs", "fixture"), "run", raw); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		url     string
		handler http.HandlerFunc
		code    int
	}{
		{"/api/runs?repo=nil%2Fonly", HandleRuns, 200}, {"/api/runs?kind=pilot", HandleRuns, 200},
		{"/api/usage?repo=nil%2Fonly&period=2026-10-01", HandleUsage, 200},
		{"/api/runs?limit=-1", HandleRuns, 400}, {"/api/usage?period=bad", HandleUsage, 400}, {"/api/runs?from=bad", HandleRuns, 400},
	} {
		rr := httptest.NewRecorder()
		tc.handler(rr, httptest.NewRequest("GET", tc.url, nil))
		if rr.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.url, rr.Code, rr.Body.String())
		}
	}
	rr := httptest.NewRecorder()
	HandleUsage(rr, httptest.NewRequest("GET", "/api/usage?repo=nil%2Fonly", nil))
	var got store.UsageSummary
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Totals.Runs != 1 || got.Totals.TotalCostUSD != nil || got.ByRepo["nil/only"].Runs != 1 {
		t.Fatalf("summary %+v", got)
	}
	rr = httptest.NewRecorder()
	HandleRuns(rr, httptest.NewRequest("GET", "/api/runs?path=runs%2Ffixture", nil))
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0]["profile"] != "unknown" {
		t.Fatalf("runs %s %v", rr.Body.String(), err)
	}
}

func TestRunsInvalidQueryWithoutDatabase(t *testing.T) {
	for _, query := range []string{"limit=0", "limit=10001", "limit=no", "from=bad", "from=2026-10-29&until=2026-10-28", "kind=other", "attempt_id=bad", "cursor=bad", "cursor=", "attempt_id=", "role=", "profile=", "execution_status="} {
		rr := httptest.NewRecorder()
		HandleRuns(rr, httptest.NewRequest("GET", "/api/runs?"+query, nil))
		if rr.Code != 400 {
			t.Fatalf("%s: %d %s", query, rr.Code, rr.Body.String())
		}
	}
}

func TestRunsBoundedAPIAndLegacyDetail(t *testing.T) {
	s := historyDB(t)
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	for _, name := range []string{"a", "b", "c"} {
		raw := []byte(`{"repo":"o/r","project":"p","operator":"implement","status":"success","started_at":"2026-10-29T00:00:00Z","summary":"private result","profile":"p","development_session":{"role":"operator"}}`)
		if _, err := s.UpsertRun(ctx, filepath.Join(snapshot.DataDir(), "runs", name), "run", raw); err != nil {
			t.Fatal(err)
		}
	}
	next := ""
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		url := "/api/runs?limit=1&repo=o%2Fr&project=p&profile=p&role=operator&execution_status=unknown" + next
		rr := httptest.NewRecorder()
		HandleRuns(rr, httptest.NewRequest("GET", url, nil))
		var rows []map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil || len(rows) != 1 {
			t.Fatalf("%s %v", rr.Body.String(), err)
		}
		id := rows[0]["attempt_id"].(string)
		if seen[id] || rows[0]["summary"] != nil {
			t.Fatal(rows)
		}
		seen[id] = true
		if rows[0]["exit_code"] != nil || rows[0]["elapsed_ms"] != nil {
			t.Fatal(rows)
		}
		cursor := rr.Header().Get("X-Next-Cursor")
		if i < 2 && cursor == "" || i == 2 && cursor != "" {
			t.Fatal("cursor", cursor)
		}
		next = "&cursor=" + cursor
		detail := httptest.NewRecorder()
		HandleRuns(detail, httptest.NewRequest("GET", "/api/runs?attempt_id="+id, nil))
		if !strings.Contains(detail.Body.String(), "private result") {
			t.Fatal(detail.Body.String())
		}
	}
	for _, query := range []string{"", "?path=runs/a"} {
		rr := httptest.NewRecorder()
		HandleRuns(rr, httptest.NewRequest("GET", "/api/runs"+query, nil))
		var rows []map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		want := 3
		if query != "" {
			want = 1
		}
		if len(rows) != want {
			t.Fatal(rows)
		}
	}
	raw := []byte(`{"project":"p","status":"success","started_at":"2026-10-29T00:00:00Z"}`)
	if _, err := s.UpsertRun(ctx, filepath.Join(snapshot.DataDir(), "pilot-runs", "p"), "pilot", raw); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	HandleRuns(rr, httptest.NewRequest("GET", "/api/runs?kind=pilot&project=p&limit=1", nil))
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"kind":"pilot"`) {
		t.Fatal(rr.Body.String())
	}
}
