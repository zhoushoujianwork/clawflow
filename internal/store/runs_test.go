package store_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

func historyStore(t *testing.T) *store.Store {
	t.Helper()
	s := storetest.New(t)
	if _, err := s.Migrate(context.Background(), store.Migrations); err != nil {
		t.Fatal(err)
	}
	return s
}
func fixture(repo, status, started string, usage any) map[string]any {
	return map[string]any{"repo": repo, "operator": "implement", "status": status, "started_at": started, "usage": usage, "profile": "p", "backend": "b", "machine": "m", "model": "model"}
}
func putRun(t *testing.T, s *store.Store, path, kind string, m map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.UpsertRun(context.Background(), path, kind, raw)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRunsUpsertAndModelReplacement(t *testing.T) {
	s := historyStore(t)
	dir := t.TempDir()
	m := fixture("o/r", "running", "2026-10-28T00:00:00Z", nil)
	if got := putRun(t, s, dir, "run", m); got != "imported" {
		t.Fatal(got)
	}
	m["status"] = "success"
	m["usage"] = map[string]any{"total_cost_usd": 2, "input_tokens": 10, "model_usage": map[string]any{"model": map[string]any{"cost_usd": 2, "input_tokens": 10}}}
	if got := putRun(t, s, dir, "run", m); got != "updated" {
		t.Fatal(got)
	}
	if got := putRun(t, s, dir, "run", m); got != "skipped" {
		t.Fatal(got)
	}
	rows, err := s.Runs(context.Background(), "run", store.RunFilter{})
	if err != nil || len(rows) != 1 || rows[0].CostUSD == nil || *rows[0].CostUSD != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	m["usage"] = nil
	putRun(t, s, dir, "run", m)
	var count int64
	if err := s.DB().Model(&store.RunModelUsage{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("models=%d err=%v", count, err)
	}
	rows, _ = s.Runs(context.Background(), "run", store.RunFilter{})
	if rows[0].UsageReported || rows[0].CostUSD != nil || rows[0].InputTokens != nil {
		t.Fatal("upsert failed to clear usage")
	}
}

func TestImportRunsCountsAndLegacy(t *testing.T) {
	s := historyStore(t)
	root := t.TempDir()
	write := func(rel string, m any) {
		t.Helper()
		path := filepath.Join(root, rel, "meta.json")
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(m)
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	legacy := map[string]any{"repo": "old/repo", "operator": "implement", "status": "success", "started_at": "2026-10-01T00:00:00Z"}
	write("runs/legacy", legacy)
	write("runs/costless", fixture("o/r", "failed", "2026-10-02T00:00:00Z", map[string]any{"input_tokens": 7}))
	write("pilot-runs/project/one", map[string]any{"project": "project", "status": "success", "started_at": "2026-10-01T00:00:00Z"})
	write("runs/old.orphaned-123/run", legacy)
	write("pilot-runs/old.orphaned-123/run", legacy)
	write("runs/invalid", map[string]any{"status": "success"})
	ctx := context.Background()
	var diagnostics []store.ImportDiagnostic
	report := func(d store.ImportDiagnostic) { diagnostics = append(diagnostics, d) }
	c, err := s.ImportRuns(ctx, root, report)
	if err != nil || c != (store.ImportCounts{Imported: 3, Invalid: 1}) {
		t.Fatalf("first %+v %v", c, err)
	}
	if len(diagnostics) != 1 || diagnostics[0].Path != "runs/invalid/meta.json" || !strings.Contains(diagnostics[0].Reason, "missing run identity") {
		t.Fatalf("diagnostics: %+v", diagnostics)
	}
	diagnostics = nil
	c, err = s.ImportRuns(ctx, root, report)
	if len(diagnostics) != 4 {
		t.Fatalf("diagnostics: %+v", diagnostics)
	}
	for _, d := range diagnostics {
		if d.Status == "skipped" && d.Reason != "unchanged metadata already imported" {
			t.Fatalf("skip reason: %+v", d)
		}
	}
	if err != nil || c != (store.ImportCounts{Skipped: 3, Invalid: 1}) {
		t.Fatalf("again %+v %v", c, err)
	}
	legacy["summary"] = "updated"
	write("runs/legacy", legacy)
	c, err = s.ImportRuns(ctx, root)
	if err != nil || c != (store.ImportCounts{Updated: 1, Skipped: 2, Invalid: 1}) {
		t.Fatalf("update %+v %v", c, err)
	}
	rows, err := s.Runs(ctx, "run", store.RunFilter{Repo: "old/repo"})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if r := rows[0]; r.Profile != "unknown" || r.Backend != "unknown" || r.Model != "unknown" || r.Machine != "unknown" || r.UsageReported || r.CostUSD != nil {
		t.Fatalf("legacy %+v", r)
	}
}

func TestUsageMixedAttributionAndPeriods(t *testing.T) {
	s := historyStore(t)
	ctx := context.Background()
	root := t.TempDir()
	usage := map[string]any{"total_cost_usd": 2, "input_tokens": 10, "output_tokens": 5, "model_usage": map[string]any{"model": map[string]any{"cost_usd": 2, "input_tokens": 10, "output_tokens": 5}}}
	cases := []struct {
		repo, status, date string
		u                  any
	}{
		{"o/r", "success", "2026-10-15T00:00:00Z", usage},
		{"o/r", "failed", "2026-10-16T00:00:00Z", map[string]any{"input_tokens": 7}},
		{"o/r", "cancelled", "2026-10-16T00:00:00Z", nil},
		{"nil/only", "success", "2026-10-17T00:00:00Z", nil},
		{"o/r", "running", "2026-10-18T00:00:00Z", usage},
		{"o/r", "finalizing", "2026-10-18T00:00:00Z", usage},
		{"old/repo", "success", "2026-10-14T23:59:59Z", usage},
	}
	for i, c := range cases {
		m := fixture(c.repo, c.status, c.date, c.u)
		if c.repo == "nil/only" {
			m["profile"] = "other"
			m["backend"] = "new"
			m["machine"] = "other"
		}
		putRun(t, s, filepath.Join(root, time.Unix(int64(i), 0).Format("150405")), "run", m)
	}
	sum, err := s.Usage(ctx, store.RunFilter{}, 15)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Totals.Runs != 5 || *sum.Totals.TotalCostUSD != 4 || *sum.Totals.InputTokens != 27 {
		t.Fatalf("totals %+v", sum.Totals)
	}
	if a := sum.ByRepo["nil/only"]; a.Runs != 1 || a.TotalCostUSD != nil || a.InputTokens != nil {
		t.Fatalf("nil repo %+v", a)
	}
	if sum.ByExecutor["p/b"].Runs != 4 || sum.ByExecutor["other/new"].Runs != 1 || sum.ByMachine["m"].Runs != 4 || sum.ByMachine["other"].Runs != 1 || sum.ByModel["model"].Runs != 5 {
		t.Fatal("attribution dropped runs")
	}
	if len(sum.Periods) != 2 || sum.Periods[0].PeriodStart != "2026-10-15" || sum.Periods[0].Totals.Runs != 4 {
		t.Fatalf("periods %+v", sum.Periods)
	}
	start, end := store.BillingPeriod(time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC), 15)
	filtered, err := s.Usage(ctx, store.RunFilter{Repo: "o/r", From: &start, Until: &end}, 15)
	if err != nil || filtered.Totals.Runs != 3 || *filtered.Totals.InputTokens != 17 || *filtered.Totals.TotalCostUSD != 2 {
		t.Fatalf("filtered %+v %v", filtered, err)
	}
	pilot := map[string]any{"project": "proj", "status": "success", "started_at": "2026-10-16T00:00:00Z"}
	putRun(t, s, filepath.Join(root, "pilot"), "pilot", pilot)
	sum, err = s.Usage(ctx, store.RunFilter{}, 15)
	if err != nil || sum.Totals.Runs != 6 || sum.ByOperator["pilot:proj"].Runs != 1 {
		t.Fatalf("pilot %+v %v", sum, err)
	}
	if _, ok := sum.ByRepo[""]; ok {
		t.Fatal("pilot attributed to empty repo")
	}
}

func TestImportInvalidFilesAndOrphanedDirectories(t *testing.T) {
	root := t.TempDir()
	for rel, raw := range map[string]string{
		"runs/bad/meta.json":                         "{",
		"runs/missing/meta.json":                     "{}",
		"runs/repo.orphaned-123/run/meta.json":       "{}",
		"pilot-runs/repo.orphaned-123/run/meta.json": "{}",
	} {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Invalid and archived files must never reach the database.
	var s *store.Store
	var diagnostics []store.ImportDiagnostic
	counts, err := s.ImportRuns(context.Background(), root, func(d store.ImportDiagnostic) { diagnostics = append(diagnostics, d) })
	if err != nil || counts.Invalid != 2 || len(diagnostics) != 2 {
		t.Fatalf("counts=%+v diagnostics=%+v err=%v", counts, diagnostics, err)
	}
	if diagnostics[0].Path != "runs/bad/meta.json" || !strings.Contains(diagnostics[0].Reason, "unexpected end") {
		t.Fatalf("malformed JSON: %+v", diagnostics[0])
	}
	if diagnostics[1].Path != "runs/missing/meta.json" || diagnostics[1].Reason != "missing run identity, status or started_at" {
		t.Fatalf("missing fields: %+v", diagnostics[1])
	}
	for _, d := range diagnostics {
		if d.Status != "invalid" {
			t.Fatalf("status: %+v", d)
		}
	}
}
