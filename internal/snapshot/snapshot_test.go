package snapshot

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// floatEq compares floats with a small epsilon to absorb the round-off you
// always get when summing IEEE-754 values (0.10 + 0.20 != 0.30).
func floatEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// TestExtractUsage feeds a hand-rolled events.jsonl that mirrors the real
// shape claude-cli writes — top-level snake_case + camelCase inside
// modelUsage — and asserts every field round-trips through ExtractUsage.
func TestExtractUsage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	// Two non-result lines first to exercise the scanner skip path; the
	// result line is the LAST line so ExtractUsage's "take the final result"
	// behavior is what's under test.
	lines := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"duration_ms":466374,"num_turns":3,"result":"done","total_cost_usd":0.834076,"usage":{"input_tokens":101590,"output_tokens":1814,"cache_creation_input_tokens":42,"cache_read_input_tokens":17},"modelUsage":{"claude-haiku-4-5-20251001":{"inputTokens":262881,"outputTokens":3579,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"costUSD":0.280776},"claude-opus-4-6":{"inputTokens":101590,"outputTokens":1814,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"costUSD":0.5532999999999999}}}`,
	}
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatalf("write events.jsonl: %v", err)
	}

	u, err := ExtractUsage(path)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if u == nil {
		t.Fatal("expected non-nil Usage")
	}

	if u.DurationMs != 466374 {
		t.Errorf("DurationMs = %d, want 466374", u.DurationMs)
	}
	if u.NumTurns != 3 {
		t.Errorf("NumTurns = %d, want 3", u.NumTurns)
	}
	if u.TotalCostUSD != 0.834076 {
		t.Errorf("TotalCostUSD = %v, want 0.834076", u.TotalCostUSD)
	}
	if u.InputTokens != 101590 {
		t.Errorf("InputTokens = %d, want 101590", u.InputTokens)
	}
	if u.OutputTokens != 1814 {
		t.Errorf("OutputTokens = %d, want 1814", u.OutputTokens)
	}
	if u.CacheCreationInputTokens != 42 {
		t.Errorf("CacheCreationInputTokens = %d, want 42", u.CacheCreationInputTokens)
	}
	if u.CacheReadInputTokens != 17 {
		t.Errorf("CacheReadInputTokens = %d, want 17", u.CacheReadInputTokens)
	}
	if len(u.ModelUsage) != 2 {
		t.Fatalf("ModelUsage len = %d, want 2", len(u.ModelUsage))
	}
	haiku, ok := u.ModelUsage["claude-haiku-4-5-20251001"]
	if !ok {
		t.Fatal("haiku model missing")
	}
	if haiku.InputTokens != 262881 || haiku.OutputTokens != 3579 || haiku.CostUSD != 0.280776 {
		t.Errorf("haiku model usage mismatch: %+v", haiku)
	}
	opus, ok := u.ModelUsage["claude-opus-4-6"]
	if !ok {
		t.Fatal("opus model missing")
	}
	if opus.InputTokens != 101590 || opus.OutputTokens != 1814 || opus.CostUSD != 0.5532999999999999 {
		t.Errorf("opus model usage mismatch: %+v", opus)
	}
}

// TestExtractUsageNoResult covers the in-flight case: events.jsonl exists but
// has no terminal result event yet. ExtractUsage must return (nil, nil) so
// the caller can retry on the next refresh.
func TestExtractUsageNoResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	lines := []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`,
	}
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatalf("write events.jsonl: %v", err)
	}
	u, err := ExtractUsage(path)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if u != nil {
		t.Errorf("expected nil Usage for in-flight run, got %+v", u)
	}
}

// TestExtractUsageMissingFile mirrors the case where events.jsonl was never
// created (test runs, dry runs). Should return (nil, nil) — not an error.
func TestExtractUsageMissingFile(t *testing.T) {
	u, err := ExtractUsage(filepath.Join(t.TempDir(), "missing.jsonl"))
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if u != nil {
		t.Errorf("expected nil Usage for missing file, got %+v", u)
	}
}

// TestUsageSummaryAggregation feeds two RunIndexEntry rows with overlapping
// models + repos + operators and asserts the rollup sums correctly.
func TestUsageSummaryAggregation(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	now := time.Now().UTC()
	entries := []RunIndexEntry{
		{
			RunMeta: RunMeta{
				Operator:    "evaluate-bug",
				Repo:        "owner/repo-a",
				IssueNumber: 1,
				StartedAt:   now,
				Status:      "success",
				Usage: &Usage{
					DurationMs:   1000,
					NumTurns:     2,
					TotalCostUSD: 0.10,
					InputTokens:  100,
					OutputTokens: 50,
					ModelUsage: map[string]ModelUsage{
						"opus": {InputTokens: 100, OutputTokens: 50, CostUSD: 0.10},
					},
				},
			},
		},
		{
			RunMeta: RunMeta{
				Operator:    "evaluate-bug",
				Repo:        "owner/repo-b",
				IssueNumber: 2,
				StartedAt:   now,
				Status:      "success",
				Usage: &Usage{
					DurationMs:   2000,
					NumTurns:     4,
					TotalCostUSD: 0.20,
					InputTokens:  200,
					OutputTokens: 100,
					ModelUsage: map[string]ModelUsage{
						"opus":  {InputTokens: 150, OutputTokens: 75, CostUSD: 0.15},
						"haiku": {InputTokens: 50, OutputTokens: 25, CostUSD: 0.05},
					},
				},
			},
		},
		{
			// Skipped (no Usage) — must NOT count toward Runs or any field.
			RunMeta: RunMeta{Operator: "evaluate-bug", Repo: "owner/repo-a", Status: "skipped"},
		},
	}

	if err := WriteUsageSummary(entries); err != nil {
		t.Fatalf("WriteUsageSummary: %v", err)
	}

	// Read it back to confirm the on-disk shape matches what the dashboard
	// will render. (Also doubles as an integration test for writeJSON.)
	path := filepath.Join(DataDir(), "usage.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read usage.json: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("usage.json is empty")
	}

	// Re-aggregate in memory to assert the sums without depending on JSON
	// field ordering. This builds the same UsageSummary the writer produced.
	var sum UsageSummary
	if err := json.Unmarshal(data, &sum); err != nil {
		t.Fatalf("unmarshal usage.json: %v", err)
	}

	if sum.Totals.Runs != 2 {
		t.Errorf("Totals.Runs = %d, want 2 (skipped run must be ignored)", sum.Totals.Runs)
	}
	if !floatEq(sum.Totals.TotalCostUSD, 0.30) {
		t.Errorf("Totals.TotalCostUSD = %v, want ~0.30", sum.Totals.TotalCostUSD)
	}
	if sum.Totals.InputTokens != 300 {
		t.Errorf("Totals.InputTokens = %d, want 300", sum.Totals.InputTokens)
	}
	if sum.Totals.OutputTokens != 150 {
		t.Errorf("Totals.OutputTokens = %d, want 150", sum.Totals.OutputTokens)
	}
	if sum.Totals.DurationMs != 3000 {
		t.Errorf("Totals.DurationMs = %d, want 3000", sum.Totals.DurationMs)
	}

	op := sum.ByOperator["evaluate-bug"]
	if op.Runs != 2 || !floatEq(op.TotalCostUSD, 0.30) {
		t.Errorf("ByOperator[evaluate-bug] = %+v, want runs=2 cost=~0.30", op)
	}

	if a := sum.ByRepo["owner/repo-a"]; a.Runs != 1 || !floatEq(a.TotalCostUSD, 0.10) {
		t.Errorf("ByRepo[repo-a] = %+v, want runs=1 cost=0.10", a)
	}
	if b := sum.ByRepo["owner/repo-b"]; b.Runs != 1 || !floatEq(b.TotalCostUSD, 0.20) {
		t.Errorf("ByRepo[repo-b] = %+v, want runs=1 cost=0.20", b)
	}

	opus := sum.ByModel["opus"]
	if !floatEq(opus.CostUSD, 0.25) || opus.InputTokens != 250 || opus.OutputTokens != 125 {
		t.Errorf("ByModel[opus] = %+v, want cost=0.25 input=250 output=125", opus)
	}
	haiku := sum.ByModel["haiku"]
	if !floatEq(haiku.CostUSD, 0.05) || haiku.InputTokens != 50 {
		t.Errorf("ByModel[haiku] = %+v, want cost=0.05 input=50", haiku)
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

// --- Reconciliation tests ---

// makeRunDir creates a runs/<repo>/issue-<N>/<ts>/ directory and writes the
// supplied meta + optional events.jsonl. Returns the run dir path.
func makeRunDir(t *testing.T, root, repoSlug string, issueNum int, ts time.Time, m *RunMeta, events string) string {
	t.Helper()
	dir := filepath.Join(root, repoSlug, "issue-"+itoa(issueNum), ts.UTC().Format("2006-01-02T15-04-05Z"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if m != nil {
		if err := WriteRunMeta(dir, *m); err != nil {
			t.Fatalf("write meta: %v", err)
		}
	}
	if events != "" {
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(events), 0o644); err != nil {
			t.Fatalf("write events: %v", err)
		}
	}
	return dir
}

func itoa(n int) string {
	// Avoid pulling strconv just for one call site.
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	negative := n < 0
	if negative {
		n = -n
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if negative {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

func TestReconcileStaleRuns_StuckRunning_RewrittenToFailed(t *testing.T) {
	root := t.TempDir()
	stuckStart := time.Now().UTC().Add(-3 * time.Hour) // well past the 1h cutoff
	stuck := &RunMeta{
		Operator:    "implement",
		Repo:        "owner/repo",
		IssueNumber: 7,
		StartedAt:   stuckStart,
		Status:      "running",
	}
	dir := makeRunDir(t, root, "owner__repo", 7, stuckStart, stuck, `{"type":"system","subtype":"init"}`+"\n")

	n, err := reconcileStaleRunsAt(root, time.Hour)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Errorf("fixed=%d, want 1", n)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "meta.json"))
	var got RunMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("read patched meta: %v", err)
	}
	if got.Status != "failed" {
		t.Errorf("status=%q, want failed", got.Status)
	}
	if got.Error == "" {
		t.Error("Error field should describe the reconciliation reason")
	}
	if got.EndedAt.IsZero() {
		t.Error("EndedAt should be populated after reconciliation")
	}
}

func TestReconcileStaleRuns_RecentRunning_Untouched(t *testing.T) {
	root := t.TempDir()
	recentStart := time.Now().UTC().Add(-5 * time.Minute) // well within the 1h cutoff
	recent := &RunMeta{
		Operator:    "implement",
		Repo:        "owner/repo",
		IssueNumber: 8,
		StartedAt:   recentStart,
		Status:      "running",
	}
	makeRunDir(t, root, "owner__repo", 8, recentStart, recent, "")

	n, err := reconcileStaleRunsAt(root, time.Hour)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 0 {
		t.Errorf("fixed=%d, want 0 (recent running should be left alone)", n)
	}
}

func TestReconcileStaleRuns_TerminalStatus_Untouched(t *testing.T) {
	root := t.TempDir()
	old := time.Now().UTC().Add(-3 * time.Hour)
	for _, status := range []string{"success", "failed", "skipped"} {
		m := &RunMeta{
			Operator:    "x",
			Repo:        "owner/repo",
			IssueNumber: 1,
			StartedAt:   old,
			EndedAt:     old.Add(time.Minute),
			Status:      status,
		}
		// Distinct dir per status so they don't collide.
		ts := old.Add(time.Duration(len(status)) * time.Second)
		makeRunDir(t, root, "owner__repo", 1, ts, m, "")
	}

	n, err := reconcileStaleRunsAt(root, time.Hour)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 0 {
		t.Errorf("fixed=%d, want 0 (terminal statuses should not be reconciled)", n)
	}
}

func TestReconcileStaleRuns_MissingMeta_Synthesized(t *testing.T) {
	root := t.TempDir()
	ts := time.Now().UTC().Add(-30 * time.Minute)
	// No meta.json, but events.jsonl present.
	makeRunDir(t, root, "owner__repo", 9, ts, nil, `{"type":"system","subtype":"init"}`+"\n")

	n, err := reconcileStaleRunsAt(root, time.Hour)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Errorf("fixed=%d, want 1", n)
	}
	dir := filepath.Join(root, "owner__repo", "issue-9", ts.Format("2006-01-02T15-04-05Z"))
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("synthetic meta missing: %v", err)
	}
	var got RunMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal synthetic meta: %v", err)
	}
	if got.Status != "failed" {
		t.Errorf("status=%q, want failed", got.Status)
	}
	if got.Repo != "owner/repo" {
		t.Errorf("repo=%q, want owner/repo (slug should be un-mangled)", got.Repo)
	}
	if got.IssueNumber != 9 {
		t.Errorf("issue=%d, want 9", got.IssueNumber)
	}
}

func TestReconcileStaleRuns_NothingToFix(t *testing.T) {
	root := t.TempDir()
	// Nonexistent runsRoot is a normal cold-start case.
	if n, err := reconcileStaleRunsAt(filepath.Join(root, "does-not-exist"), time.Hour); err != nil || n != 0 {
		t.Errorf("nonexistent root: n=%d err=%v, want 0/nil", n, err)
	}
}

func TestReconcileStaleRuns_Idempotent(t *testing.T) {
	root := t.TempDir()
	stuckStart := time.Now().UTC().Add(-3 * time.Hour)
	stuck := &RunMeta{
		Operator:    "implement",
		Repo:        "owner/repo",
		IssueNumber: 7,
		StartedAt:   stuckStart,
		Status:      "running",
	}
	makeRunDir(t, root, "owner__repo", 7, stuckStart, stuck, "")

	if n, _ := reconcileStaleRunsAt(root, time.Hour); n != 1 {
		t.Errorf("first pass fixed=%d, want 1", n)
	}
	// Second pass should find the run already terminal and do nothing.
	if n, _ := reconcileStaleRunsAt(root, time.Hour); n != 0 {
		t.Errorf("second pass fixed=%d, want 0 (idempotent)", n)
	}
}
