package coordinator

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

func TestDashboardUsageBucketsInControllerLocalDayAndScopesRepos(t *testing.T) {
	db := storetest.New(t)
	if _, err := db.Migrate(context.Background(), store.Migrations); err != nil {
		t.Fatal(err)
	}
	s := New(db.DB())
	loc := time.FixedZone("controller", 8*60*60)
	now := time.Date(2026, 11, 6, 2, 0, 0, 0, loc)
	insert := `INSERT INTO runs
 (run_dir, repo, status, started_at, cost_usd, input_tokens, output_tokens, cache_read_input_tokens, cache_creation_input_tokens, model, payload)
 VALUES (?, ?, 'success', ?, ?, ?, ?, ?, ?, ?, ?)`
	known := `{"usage":{"cost_basis":"api"}}` // false is omitted in real metadata.
	unknown := `{}`                           // Missing cost provenance fails closed to an estimate.
	if err := db.DB().Exec(insert, "today", "o/a", time.Date(2026, 11, 5, 16, 30, 0, 0, time.UTC), 1.25, 10, 20, 30, 40, "gpt-a", known).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.DB().Exec(insert, "yesterday", "o/b", time.Date(2026, 11, 5, 15, 30, 0, 0, time.UTC), 2.75, 100, 200, 300, 400, "gpt-b", unknown).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.DB().Exec(insert, "eight-days-ago", "o/a", time.Date(2026, 10, 28, 18, 0, 0, 0, time.UTC), 5.0, 1, 1, 1, 1, "gpt-a", known).Error; err != nil {
		t.Fatal(err)
	}
	usage, err := s.DashboardUsage(context.Background(), now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Total.Today.CostUSD == nil || *usage.Total.Today.CostUSD != 1.25 || usage.Total.Today.TotalTokens == nil || *usage.Total.Today.TotalTokens != 100 {
		t.Fatalf("wrong local today aggregate: %+v", usage.Total.Today)
	}
	if usage.Total.SevenDays.CostUSD == nil || *usage.Total.SevenDays.CostUSD != 4 || usage.Total.SevenDays.TotalTokens == nil || *usage.Total.SevenDays.TotalTokens != 1100 || !usage.Total.SevenDays.CostIsEstimate {
		t.Fatalf("wrong 7-day aggregate: %+v", usage.Total.SevenDays)
	}
	if usage.Total.ThirtyDays.CostUSD == nil || *usage.Total.ThirtyDays.CostUSD != 9 || usage.Total.ThirtyDays.TotalTokens == nil || *usage.Total.ThirtyDays.TotalTokens != 1104 || !usage.Total.ThirtyDays.CostIsEstimate {
		t.Fatalf("wrong 30-day cost semantics: %+v", usage.Total.ThirtyDays)
	}
	if got := usage.ByRepo["o/a"].MostUsedModel; got != "gpt-a" {
		t.Fatalf("repo model = %q", got)
	}
	if len(usage.Total.Daily) != 30 || usage.Total.Daily[29].CostUSD == nil || *usage.Total.Daily[29].CostUSD != 1.25 {
		t.Fatalf("daily buckets = %+v", usage.Total.Daily)
	}
}

func TestDashboardUsageUsesStartedIndexAndMeasures100kRows(t *testing.T) {
	db := storetest.New(t)
	if _, err := db.Migrate(context.Background(), store.Migrations); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	payload := `{"usage":{"cost_basis":"api"}}`
	if err := db.DB().Exec(`WITH RECURSIVE rows(n) AS (
 SELECT 1 UNION ALL SELECT n + 1 FROM rows WHERE n < 100000
)
INSERT INTO runs
 (run_dir, repo, status, started_at, cost_usd, input_tokens, output_tokens, cache_read_input_tokens, cache_creation_input_tokens, model, payload)
SELECT printf('fixture/%06d', n), CASE WHEN n % 2 = 0 THEN 'o/a' ELSE 'o/b' END, 'success', ?,
       0.01, 1, 2, 3, 4, CASE WHEN n % 3 = 0 THEN 'gpt-b' ELSE 'gpt-a' END, ? FROM rows`, now.Add(-time.Hour), payload).Error; err != nil {
		t.Fatal(err)
	}
	var plans []struct{ Detail string }
	if err := db.DB().Raw(`EXPLAIN QUERY PLAN SELECT repo, started_at, cost_usd FROM runs WHERE started_at >= ? AND started_at < ?`, now.AddDate(0, 0, -30), now).Scan(&plans).Error; err != nil {
		t.Fatal(err)
	}
	var planText string
	for _, plan := range plans {
		planText += plan.Detail + "\n"
	}
	if !strings.Contains(planText, "idx_runs_started") {
		t.Fatalf("query is not using started_at index:\n%s", planText)
	}
	started := time.Now()
	usage, err := New(db.DB()).DashboardUsage(context.Background(), now, time.UTC)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Total.ThirtyDays.CostUSD == nil || math.Abs(*usage.Total.ThirtyDays.CostUSD-1000) > 0.001 || usage.Total.ThirtyDays.TotalTokens == nil || *usage.Total.ThirtyDays.TotalTokens != 1_000_000 {
		t.Fatalf("100k aggregate wrong: %+v", usage.Total.ThirtyDays)
	}
	t.Logf("dashboard usage 100k-row cold query: %s", elapsed)
	if elapsed > 2*time.Second {
		t.Fatalf("dashboard usage cold query too slow: %s", elapsed)
	}
}
