package store_test

import (
	"context"
	"encoding/json"
	"fmt"
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
	if r := rows[0]; r.Profile != "unknown" || r.Backend != "unknown" || r.Model != "unknown" || r.Machine != "unknown" || r.UsageReported || r.CostUSD != nil || r.ExitCode != nil || r.ElapsedMs != nil {
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

// A resumed session has distinct attempts, each with replacement (not additive)
// usage. Replaying its final metadata must leave both counts and evidence stable.
func TestSameSessionAttemptReplay(t *testing.T) {
	s := historyStore(t)
	root := t.TempDir()
	for _, kind := range []string{"run", "pilot"} {
		for i := 1; i <= 3; i++ {
			m := fixture("o/r", "failed", "2026-10-29T00:00:00Z", map[string]any{"input_tokens": 10, "output_tokens": 3})
			m["project"] = "platform"
			m["session_id"] = "shared-session"
			m["offload_run_id"] = fmt.Sprintf("attempt-%d", i)
			m["caller_run_id"] = fmt.Sprintf("caller-%d", i)
			m["exit_code"] = i
			m["elapsed_ms"] = int64(1200)
			m["error_kind"] = "execution"
			m["execution_status"] = "error"
			m["cumulative_usage"] = map[string]any{"input_tokens": 999999}
			dir := filepath.Join(root, kind, fmt.Sprint(i))
			if got := putRun(t, s, dir, kind, m); got != "imported" {
				t.Fatal(got)
			}
			if got := putRun(t, s, dir, kind, m); got != "skipped" {
				t.Fatal(got)
			}
			if got := putRun(t, s, dir+"-replayed", kind, m); got != "skipped" {
				t.Fatal(got)
			}
		}
		rows, err := s.Runs(context.Background(), kind, store.RunFilter{})
		if err != nil || len(rows) != 3 {
			t.Fatalf("rows=%+v err=%v", rows, err)
		}
		total := int64(0)
		for _, r := range rows {
			if r.ExitCode == nil || *r.ExitCode == 0 || r.ElapsedMs == nil || *r.ElapsedMs != 1200 || r.SessionID != "shared-session" || r.OffloadRunID == "" || r.CallerRunID == "" || r.InputTokens == nil {
				t.Fatalf("row: %+v", r)
			}
			total += *r.InputTokens
		}
		if total != 30 {
			t.Fatalf("accumulated usage: %d", total)
		}
		// The same Offload ID from another machine is a different attempt.
		remote := fixture("o/r", "failed", "2026-10-29T00:00:00Z", nil)
		remote["project"] = "platform"
		remote["machine"] = "remote-machine"
		remote["session_id"] = "shared-session"
		remote["offload_run_id"] = "attempt-1"
		if got := putRun(t, s, filepath.Join(root, kind, "remote"), kind, remote); got != "imported" {
			t.Fatal(got)
		}
		rows, err = s.Runs(context.Background(), kind, store.RunFilter{})
		if err != nil || len(rows) != 4 {
			t.Fatalf("remote attempt lost: rows=%+v err=%v", rows, err)
		}
	}
}

func TestRunHistoryPaginationAndFilters(t *testing.T) {
	s := historyStore(t)
	root := t.TempDir()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		m := fixture("o/r", "success", "2026-10-29T00:00:00Z", nil)
		m["project"], m["execution_status"], m["role"] = "platform", "ok", "operator"
		m["offload_run_id"] = fmt.Sprint(i)
		putRun(t, s, filepath.Join(root, fmt.Sprint(i)), "run", m)
	}
	from := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	until := from.Add(time.Hour)
	f := store.RunFilter{Repo: "o/r", Project: "platform", ExecutionStatus: "ok", Role: "operator", Profile: "p", From: &from, Until: &until, Limit: 2}
	seen := map[string]bool{}
	for {
		rows, err := s.Runs(ctx, "run", f)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			if seen[r.RunDir] {
				t.Fatal("duplicate", r.RunDir)
			}
			seen[r.RunDir] = true
			found, err := s.Runs(ctx, "run", store.RunFilter{AttemptID: store.AttemptID(r, "run")})
			if err != nil || len(found) != 1 || found[0].RunDir != r.RunDir {
				t.Fatalf("lookup: %+v %v", found, err)
			}
		}
		f.Cursor = store.RunsCursor(rows[len(rows)-1], "run")
	}
	if len(seen) != 5 {
		t.Fatal(seen)
	}
	f.Cursor = ""
	f.Profile = "other"
	rows, err := s.Runs(ctx, "run", f)
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
}

func TestRunListProjectionLegacyAndPrivacy(t *testing.T) {
	raw := []byte(`{"repo":"o/r","operator":"implement","status":"success","started_at":"2026-10-29T00:00:00Z","prompt":"SECRET","summary":"SECRET","result":"SECRET","issue_body":"SECRET","development_session":{"workdir":"SECRET"},"usage":{"input_tokens":3,"result":"SECRET"}}`)
	r, _, err := store.DecodeRun(filepath.Join(t.TempDir(), "run"), "run", raw)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.RunListPayloads([]store.Run{r}, filepath.Dir(r.RunDir), "run")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rows)
	if strings.Contains(string(b), "SECRET") {
		t.Fatal(string(b))
	}
	if rows[0]["exit_code"] != nil {
		t.Fatal(rows)
	}
	a := rows[0]["availability"].(store.RunAvailability)
	if a.Attempt || a.Cost || a.Elapsed || a.ExitCode || a.ExecutionStatus || !a.Tokens {
		t.Fatal(a)
	}
	if rows[0]["attribution"].(store.RunAttribution).Role != "unknown" {
		t.Fatal(rows)
	}
	id := store.AttemptID(r, "run")
	if err := store.ValidateHistoryKeys("run", id, store.RunsCursor(r, "run")); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateHistoryKeys("pilot", id, ""); err == nil {
		t.Fatal("cross-kind lookup")
	}
	r.OffloadRunID = "attempt"
	r.Machine = "m"
	id = store.AttemptID(r, "run")
	r.RunDir += "-replay"
	if store.AttemptID(r, "run") != id {
		t.Fatal("attributed ID changed on replay")
	}
	for _, key := range []string{"garbage", "e30", "", strings.Repeat("a", 9000)} {
		if key != "" && store.ValidateHistoryKeys("run", key, "") == nil {
			t.Fatal("accepted", key)
		}
	}
}

func TestRunListReportedZeroRemainsKnown(t *testing.T) {
	raw := []byte(`{"repo":"o/r","operator":"implement","status":"success","started_at":"2026-10-29T00:00:00Z","usage":{"total_cost_usd":0,"input_tokens":0},"exit_code":0,"elapsed_ms":0,"execution_status":"ok"}`)
	r, _, err := store.DecodeRun(filepath.Join(t.TempDir(), "zero"), "run", raw)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.RunListPayloads([]store.Run{r}, filepath.Dir(r.RunDir), "run")
	if err != nil {
		t.Fatal(err)
	}
	a := rows[0]["availability"].(store.RunAvailability)
	if !a.Cost || !a.Tokens || !a.Elapsed || !a.ExitCode || !a.ExecutionStatus {
		t.Fatal(a)
	}
	usage := rows[0]["usage"].(map[string]any)
	if usage["total_cost_usd"] != float64(0) || usage["input_tokens"] != float64(0) {
		t.Fatal(usage)
	}
}

func TestUsageFiltersCoverageAndSafeHistory(t *testing.T) {
	s := historyStore(t)
	ctx := context.Background()
	root := t.TempDir()
	for i := 0; i < 55; i++ {
		m := fixture("o/r", "success", fmt.Sprintf("2026-10-16T00:%02d:00Z", i), map[string]any{
			"cost_basis": "api", "cost_is_estimate": true, "total_cost_usd": 1,
			"input_tokens": 20, "output_tokens": 5, "cache_read_input_tokens": 70, "cache_creation_input_tokens": 10,
		})
		m["project"], m["role"], m["issue_number"] = "one", "implement", 545
		m["prompt"], m["result"] = "secret-prompt", "secret-result"
		putRun(t, s, filepath.Join(root, "runs", "o__r", "issue-545", fmt.Sprintf("attempt-%02d", i)), "run", m)
	}
	// An unattributed legacy attempt is never assigned to a current project.
	putRun(t, s, filepath.Join(root, "legacy"), "run", fixture("old/r", "failed", "2026-10-16T01:00:00Z", nil))
	from, _ := time.Parse(time.RFC3339, "2026-10-16T00:00:00Z")
	until := from.Add(24 * time.Hour)
	f := store.RunFilter{Project: "one", Repo: "o/r", Role: "implement", Profile: "p", From: &from, Until: &until}
	got, err := s.Usage(ctx, f, 15, store.UsageSelection{Machine: "m", Model: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Totals.Runs != 55 || got.Totals.Coverage.Cost != 55 || *got.Totals.TotalTokens != 55*105 || got.Totals.EstimatedCostRuns != 55 {
		t.Fatalf("totals %+v", got.Totals)
	}
	if len(got.History) != 50 || got.History[0].Href != "/runs/o__r/issue-545/attempt-54" || got.RangeStart == nil || !got.RangeStart.Equal(from) || got.RangeEnd == nil || !got.RangeEnd.Equal(until) {
		t.Fatalf("range/history %+v", got)
	}
	if len(got.Periods) != 1 || got.Periods[0].PeriodStart != "2026-10-15" || got.Periods[0].PeriodEnd != "2026-11-14" {
		t.Fatalf("periods %+v", got.Periods)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "secret-") || strings.Contains(string(encoded), root) {
		t.Fatal("private body/path leaked")
	}
	for _, selection := range []store.UsageSelection{{Machine: "other"}, {Model: "other"}} {
		empty, err := s.Usage(ctx, f, 15, selection)
		if err != nil || empty.Totals.Runs != 0 || empty.Totals.TotalCostUSD != nil {
			t.Fatalf("empty %+v %v", empty, err)
		}
	}
	for _, wrong := range []store.RunFilter{{Role: "other"}, {Profile: "other"}, {Project: "other"}} {
		empty, err := s.Usage(ctx, wrong, 15)
		if err != nil || empty.Totals.Runs != 0 {
			t.Fatalf("filter %+v: %+v %v", wrong, empty.Totals, err)
		}
	}
	legacy, err := s.Usage(ctx, store.RunFilter{Project: "unknown"}, 15)
	if err != nil || legacy.Totals.Runs != 1 || legacy.Totals.TotalCostUSD != nil || legacy.ByRole["unknown"].Runs != 1 {
		t.Fatalf("legacy %+v %v", legacy, err)
	}
}
