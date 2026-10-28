package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
