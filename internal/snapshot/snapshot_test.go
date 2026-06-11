package snapshot

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
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

	if err := WriteUsageSummary(entries, 1); err != nil {
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
	if got.EndedAt == nil {
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
	// Live runners write events.jsonl within seconds of starting, so a
	// healthy "still running" row always has one. Reconciliation should
	// leave it alone as long as the file's mtime is fresh.
	makeRunDir(t, root, "owner__repo", 8, recentStart, recent, `{"type":"system","subtype":"init"}`+"\n")

	n, err := reconcileStaleRunsAt(root, time.Hour)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 0 {
		t.Errorf("fixed=%d, want 0 (recent running with live events should be left alone)", n)
	}
}

// A run whose meta.json says "running" but has no events.jsonl AT ALL is
// reconciled once it's older than DefaultQuietWindow — RunClaude opens the events
// file before launching claude, so a missing file past that window means
// the runner died before getting that far. Without this aggressive sweep
// the dashboard would carry a frozen "running" row for the full staleAfter
// timeout (default 1h).
func TestReconcileStaleRuns_NoEventsBeyondQuietWindow_RewrittenToFailed(t *testing.T) {
	root := t.TempDir()
	start := time.Now().UTC().Add(-DefaultQuietWindow - time.Minute) // safely past 2min
	stuck := &RunMeta{
		Operator:    "classify",
		Repo:        "owner/repo",
		IssueNumber: 9,
		StartedAt:   start,
		Status:      "running",
	}
	makeRunDir(t, root, "owner__repo", 9, start, stuck, "") // no events.jsonl

	n, err := reconcileStaleRunsAt(root, time.Hour)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Errorf("fixed=%d, want 1 (no events past DefaultQuietWindow should reconcile)", n)
	}
}

// A run whose events.jsonl has gone quiet past DefaultQuietWindow but whose
// runner PID is still alive (e.g. claude is mid-`api_retry` against an
// upstream 502, or running a slow tool/subagent) must NOT be marked
// failed. Without this, `clawflow web`'s minute-tick reconciler races
// the runner and falsely flips live runs to "failed", confusing the
// dashboard while the actual claude subprocess keeps producing work.
func TestReconcileStaleRuns_QuietButRunnerAlive_Untouched(t *testing.T) {
	root := t.TempDir()
	start := time.Now().UTC().Add(-DefaultQuietWindow - time.Minute)
	live := &RunMeta{
		Operator:    "implement",
		Repo:        "owner/repo",
		IssueNumber: 42,
		StartedAt:   start,
		Status:      "running",
	}
	dir := makeRunDir(t, root, "owner__repo", 42, start, live, `{"type":"system","subtype":"init"}`+"\n")
	// Backdate events.jsonl so it's quiet by mtime even though the runner
	// would still be considered alive.
	old := start.Add(time.Second)
	if err := os.Chtimes(filepath.Join(dir, "events.jsonl"), old, old); err != nil {
		t.Fatalf("chtimes events.jsonl: %v", err)
	}

	prev := runnerStillAlive
	t.Cleanup(func() { runnerStillAlive = prev })
	runnerStillAlive = func(repo string, issueNum int, metaStart time.Time) bool {
		if repo != "owner/repo" || issueNum != 42 {
			t.Errorf("runnerStillAlive called with unexpected repo=%q issue=%d", repo, issueNum)
		}
		return true
	}

	n, err := reconcileStaleRunsAt(root, time.Hour)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 0 {
		t.Errorf("fixed=%d, want 0 (live runner should be left alone)", n)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "meta.json"))
	var got RunMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if got.Status != "running" {
		t.Errorf("status=%q, want running (untouched)", got.Status)
	}
}

// Same setup as the previous test but the runner liveness check returns
// false (no lock, dead PID, or mismatched start time). The reconciler
// should mark the run failed and the error message must reflect the
// real check (PID dead/missing) rather than the old wording that
// claimed the runner was gone without ever checking.
func TestReconcileStaleRuns_QuietRunnerDead_RewrittenToFailed(t *testing.T) {
	root := t.TempDir()
	start := time.Now().UTC().Add(-DefaultQuietWindow - time.Minute)
	dead := &RunMeta{
		Operator:    "evaluate-bug", // uses DefaultQuietWindow, not an overridden operator
		Repo:        "owner/repo",
		IssueNumber: 43,
		StartedAt:   start,
		Status:      "running",
	}
	dir := makeRunDir(t, root, "owner__repo", 43, start, dead, `{"type":"system","subtype":"init"}`+"\n")
	old := start.Add(time.Second)
	if err := os.Chtimes(filepath.Join(dir, "events.jsonl"), old, old); err != nil {
		t.Fatalf("chtimes events.jsonl: %v", err)
	}

	prev := runnerStillAlive
	t.Cleanup(func() { runnerStillAlive = prev })
	runnerStillAlive = func(string, int, time.Time) bool { return false }

	n, err := reconcileStaleRunsAt(root, time.Hour)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Errorf("fixed=%d, want 1 (dead runner should be reconciled)", n)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "meta.json"))
	var got RunMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if got.Status != "failed" {
		t.Errorf("status=%q, want failed", got.Status)
	}
	if !strings.Contains(got.Error, "PID is dead/missing") {
		t.Errorf("Error=%q, want it to mention the real PID check", got.Error)
	}
}

// TestReconcileStaleRuns_ImplementOperator_WiderQuietWindow verifies that the
// "implement" operator is not reconciled when events.jsonl is only past the
// DefaultQuietWindow (2 min) but within the operator-specific window (10 min).
// This is the core regression test for issue #105.
func TestReconcileStaleRuns_ImplementOperator_WiderQuietWindow(t *testing.T) {
	root := t.TempDir()
	// Start time is well in the past so tooOld is false (staleAfter=1h).
	start := time.Now().UTC().Add(-5 * time.Minute)
	running := &RunMeta{
		Operator:    "implement",
		Repo:        "owner/repo",
		IssueNumber: 77,
		StartedAt:   start,
		Status:      "running",
	}
	dir := makeRunDir(t, root, "owner__repo", 77, start, running, `{"type":"system","subtype":"init"}`+"\n")

	// Backdate events.jsonl to 3 minutes ago — past DefaultQuietWindow (2 min)
	// but within the implement override (10 min). A live runner would not be
	// killed here.
	threeMinAgo := time.Now().Add(-3 * time.Minute)
	if err := os.Chtimes(filepath.Join(dir, "events.jsonl"), threeMinAgo, threeMinAgo); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	prev := runnerStillAlive
	t.Cleanup(func() { runnerStillAlive = prev })
	runnerStillAlive = func(string, int, time.Time) bool { return false }

	n, err := reconcileStaleRunsAt(root, time.Hour)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 0 {
		t.Errorf("fixed=%d, want 0: implement operator should not be reconciled at 3 min quiet (window is 10 min)", n)
	}
}

// TestReconcileStaleRuns_ImplementOperator_PastWiderWindow verifies that an
// "implement" run IS reconciled once events.jsonl exceeds its 10-min window.
func TestReconcileStaleRuns_ImplementOperator_PastWiderWindow(t *testing.T) {
	root := t.TempDir()
	implementWindow := OperatorQuietWindows["implement"]
	start := time.Now().UTC().Add(-implementWindow - 5*time.Minute)
	running := &RunMeta{
		Operator:    "implement",
		Repo:        "owner/repo",
		IssueNumber: 78,
		StartedAt:   start,
		Status:      "running",
	}
	dir := makeRunDir(t, root, "owner__repo", 78, start, running, `{"type":"system","subtype":"init"}`+"\n")

	// Backdate events.jsonl past the 10-min implement window.
	pastWindow := time.Now().Add(-implementWindow - time.Minute)
	if err := os.Chtimes(filepath.Join(dir, "events.jsonl"), pastWindow, pastWindow); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	prev := runnerStillAlive
	t.Cleanup(func() { runnerStillAlive = prev })
	runnerStillAlive = func(string, int, time.Time) bool { return false }

	n, err := reconcileStaleRunsAt(root, time.Hour)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Errorf("fixed=%d, want 1: implement operator should be reconciled past its 10-min window", n)
	}
}

func TestReconcileStaleRuns_TerminalStatus_Untouched(t *testing.T) {
	root := t.TempDir()
	old := time.Now().UTC().Add(-3 * time.Hour)
	endedAt := old.Add(time.Minute)
	for _, status := range []string{"success", "failed", "skipped"} {
		m := &RunMeta{
			Operator:    "x",
			Repo:        "owner/repo",
			IssueNumber: 1,
			StartedAt:   old,
			EndedAt:     &endedAt,
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

// TestReconcileStaleRuns_SuccessResult_ReconciledAsSuccess verifies that when
// events.jsonl contains a terminal "result" event with subtype "success", the
// reconciler marks the run as "success" rather than "failed" — even though the
// runner process died before writing the final meta.json.
func TestReconcileStaleRuns_SuccessResult_ReconciledAsSuccess(t *testing.T) {
	root := t.TempDir()
	start := time.Now().UTC().Add(-DefaultQuietWindow - time.Minute)
	stuck := &RunMeta{
		Operator:    "evaluate-bug",
		Repo:        "owner/repo",
		IssueNumber: 42,
		StartedAt:   start,
		Status:      "running",
	}
	events := `{"type":"system","subtype":"init"}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"evaluating..."}]}}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"duration_ms":120000,"num_turns":5,"result":"evaluation done\n<!-- clawflow:outcome=agent-evaluated -->","total_cost_usd":0.50,"usage":{"input_tokens":5000,"output_tokens":500,"cache_read_input_tokens":0,"cache_creation_input_tokens":0},"modelUsage":{"claude-opus-4-6":{"inputTokens":5000,"outputTokens":500,"cacheReadInputTokens":0,"cacheCreationInputTokens":0,"costUSD":0.50}}}` + "\n"

	dir := makeRunDir(t, root, "owner__repo", 42, start, stuck, events)
	// Backdate events.jsonl mtime so it looks quiet
	past := time.Now().Add(-DefaultQuietWindow - 30*time.Second)
	os.Chtimes(filepath.Join(dir, "events.jsonl"), past, past)

	n, err := reconcileStaleRunsAt(root, time.Hour)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Errorf("fixed=%d, want 1", n)
	}

	// Read back meta and verify it's success, not failed.
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var m RunMeta
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Status != "success" {
		t.Errorf("status=%q, want \"success\"", m.Status)
	}
	if m.Usage == nil {
		t.Error("expected usage to be backfilled")
	} else if m.Usage.DurationMs != 120000 {
		t.Errorf("usage.duration_ms=%d, want 120000", m.Usage.DurationMs)
	}
	if m.Summary == "" {
		t.Error("expected summary to be populated from result text")
	}
}

// TestReconcileStaleRuns_ErrorResult_ReconciledAsFailed verifies that when
// events.jsonl has a terminal result with subtype != "success" (e.g. "error"),
// the reconciler still marks the run as "failed".
func TestReconcileStaleRuns_ErrorResult_ReconciledAsFailed(t *testing.T) {
	root := t.TempDir()
	start := time.Now().UTC().Add(-DefaultQuietWindow - time.Minute)
	stuck := &RunMeta{
		Operator:    "evaluate-bug", // uses DefaultQuietWindow, not an overridden operator
		Repo:        "owner/repo",
		IssueNumber: 99,
		StartedAt:   start,
		Status:      "running",
	}
	events := `{"type":"system","subtype":"init"}` + "\n" +
		`{"type":"result","subtype":"error","is_error":true,"duration_ms":5000,"num_turns":1,"result":"something went wrong","total_cost_usd":0.01,"usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":0},"modelUsage":{}}` + "\n"

	dir := makeRunDir(t, root, "owner__repo", 99, start, stuck, events)
	past := time.Now().Add(-DefaultQuietWindow - 30*time.Second)
	os.Chtimes(filepath.Join(dir, "events.jsonl"), past, past)

	n, err := reconcileStaleRunsAt(root, time.Hour)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Errorf("fixed=%d, want 1", n)
	}

	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var m RunMeta
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Status != "failed" {
		t.Errorf("status=%q, want \"failed\"", m.Status)
	}
}

// TestReconcileStaleRuns_Finalizing_PromotedToSuccess verifies that a run
// stuck in "finalizing" (process killed after operator.Run() completed VCS
// side-effects but before WriteRunMeta wrote the terminal status) is promoted
// to "success" by the reconciler without re-queuing the issue.
func TestReconcileStaleRuns_Finalizing_PromotedToSuccess(t *testing.T) {
	root := t.TempDir()
	start := time.Now().UTC().Add(-5 * time.Minute)
	stuck := &RunMeta{
		Operator:    "implement",
		Repo:        "owner/repo",
		IssueNumber: 55,
		StartedAt:   start,
		Status:      "finalizing",
		Summary:     "PR opened at https://github.com/owner/repo/pull/42",
	}
	events := `{"type":"system","subtype":"init"}` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"duration_ms":60000,"num_turns":3,"result":"done","total_cost_usd":0.20,"usage":{"input_tokens":2000,"output_tokens":200,"cache_read_input_tokens":0,"cache_creation_input_tokens":0},"modelUsage":{}}` + "\n"

	dir := makeRunDir(t, root, "owner__repo", 55, start, stuck, events)

	n, err := reconcileStaleRunsAt(root, time.Hour)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Errorf("fixed=%d, want 1 (finalizing run with dead runner should be promoted)", n)
	}

	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var m RunMeta
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m.Status != "success" {
		t.Errorf("status=%q, want \"success\"", m.Status)
	}
	if m.EndedAt == nil {
		t.Error("EndedAt should be populated after reconciliation")
	}
	if m.Usage == nil {
		t.Error("expected usage to be backfilled from events.jsonl")
	}
	if m.Error == "" {
		t.Error("Error field should describe the reconciliation reason")
	}
}

// TestReconcileStaleRuns_Finalizing_RunnerAlive_Untouched verifies that a
// "finalizing" run whose runner PID is still alive is left alone — the runner
// is still in the process of writing the terminal meta.
func TestReconcileStaleRuns_Finalizing_RunnerAlive_Untouched(t *testing.T) {
	root := t.TempDir()
	start := time.Now().UTC().Add(-1 * time.Minute)

	stuck := &RunMeta{
		Operator:    "implement",
		Repo:        "owner/repo",
		IssueNumber: 55,
		StartedAt:   start,
		Status:      "finalizing",
	}
	dir := makeRunDir(t, root, "owner__repo", 55, start, stuck, `{"type":"system","subtype":"init"}`+"\n")

	prev := runnerStillAlive
	t.Cleanup(func() { runnerStillAlive = prev })
	runnerStillAlive = func(repo string, issueNum int, metaStart time.Time) bool {
		return repo == "owner/repo" && issueNum == 55
	}

	n, err := reconcileStaleRunsAt(root, time.Hour)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 0 {
		t.Errorf("fixed=%d, want 0 (finalizing run with live runner should be left alone)", n)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "meta.json"))
	var got RunMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if got.Status != "finalizing" {
		t.Errorf("status=%q, want finalizing (untouched)", got.Status)
	}
}

// --- MigrateLegacyDataDir tests ----------------------------------------

// TestMigrateLegacyDataDir_MovesContentWhenLegacyExists is the happy path:
// pre-split installs that have data under ~/.clawflow/dashboard/data/ should
// have it transparently moved to ~/.clawflow/data/ on the next web start.
func TestMigrateLegacyDataDir_MovesContentWhenLegacyExists(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	legacy := filepath.Join(tmp, ".clawflow", "dashboard", "data")
	if err := os.MkdirAll(filepath.Join(legacy, "runs", "owner__repo", "issue-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "runs.json"), []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "runs", "owner__repo", "issue-1", "meta.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	moved, err := MigrateLegacyDataDir()
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !moved {
		t.Fatal("expected moved=true when legacy dir had content")
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy dir should be gone after migrate, stat err=%v", err)
	}
	want := filepath.Join(tmp, ".clawflow", "data", "runs", "owner__repo", "issue-1", "meta.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected migrated file at %s: %v", want, err)
	}
}

// TestMigrateLegacyDataDir_NoOpWhenLegacyMissing returns moved=false on a
// fresh install where there is nothing to migrate. Critical because we call
// it on every web start; a noisy success on a no-op would clutter logs.
func TestMigrateLegacyDataDir_NoOpWhenLegacyMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	moved, err := MigrateLegacyDataDir()
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if moved {
		t.Fatal("expected moved=false when no legacy dir exists")
	}
}

// TestMigrateLegacyDataDir_SkipsWhenNewLocationPopulated covers the rare
// "both exist" case (manual user move, partial prior migration). We never
// merge — too easy to clobber. Caller can investigate by hand.
func TestMigrateLegacyDataDir_SkipsWhenNewLocationPopulated(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	legacy := filepath.Join(tmp, ".clawflow", "dashboard", "data")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "runs.json"), []byte(`["legacy"]`), 0o644); err != nil {
		t.Fatal(err)
	}

	current := filepath.Join(tmp, ".clawflow", "data")
	if err := os.MkdirAll(current, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "runs.json"), []byte(`["new"]`), 0o644); err != nil {
		t.Fatal(err)
	}

	moved, err := MigrateLegacyDataDir()
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if moved {
		t.Fatal("expected moved=false when new location already populated")
	}
	// Both dirs should be untouched.
	got, _ := os.ReadFile(filepath.Join(current, "runs.json"))
	if string(got) != `["new"]` {
		t.Fatalf("new location overwritten: %q", got)
	}
	got2, _ := os.ReadFile(filepath.Join(legacy, "runs.json"))
	if string(got2) != `["legacy"]` {
		t.Fatalf("legacy location overwritten: %q", got2)
	}
}



// TestCleanupLegacyDashboardDir_RemovesDirWithSPAArtifacts is the
// post-refactor happy path: an existing extracted SPA tree (assets/,
// index.html, favicon.svg, logo.svg) gets nuked because the binary
// now serves SPA content from embed.FS — the on-disk copy is dead
// weight.
func TestCleanupLegacyDashboardDir_RemovesDirWithSPAArtifacts(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	root := filepath.Join(tmp, ".clawflow", "dashboard")
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"index.html", "favicon.svg", "logo.svg"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := CleanupLegacyDashboardDir(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("dashboard dir should be gone, stat err=%v", err)
	}
}

// TestCleanupLegacyDashboardDir_NoOpWhenMissing returns nil quietly
// for fresh installs where there is nothing to clean up. Important
// because the cleanup runs on every web start.
func TestCleanupLegacyDashboardDir_NoOpWhenMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	if err := CleanupLegacyDashboardDir(); err != nil {
		t.Fatalf("expected nil for missing dir, got %v", err)
	}
}

// TestCleanupLegacyDashboardDir_RefusesUnknownEntries is the safety
// net: if a future contributor (or a user) drops something we do not
// recognize into the dashboard root, we refuse rather than blindly
// rm -rf. Better to leave a stray dir than corrupt unknown data.
func TestCleanupLegacyDashboardDir_RefusesUnknownEntries(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	root := filepath.Join(tmp, ".clawflow", "dashboard")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "user-notes.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CleanupLegacyDashboardDir(); err == nil {
		t.Fatal("expected error refusing to clean dir with unknown entry")
	}
	// Dir + file should still be there.
	if _, err := os.Stat(filepath.Join(root, "user-notes.txt")); err != nil {
		t.Fatalf("user file should be preserved: %v", err)
	}
}

// TestCleanupLegacyDashboardDir_RefusesNonEmptyDataSubdir guards
// against running cleanup before MigrateLegacyDataDir succeeds.
// If data is still inside the dashboard tree, removing the parent
// would silently delete it.
func TestCleanupLegacyDashboardDir_RefusesNonEmptyDataSubdir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	root := filepath.Join(tmp, ".clawflow", "dashboard")
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data", "runs.json"), []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CleanupLegacyDashboardDir(); err == nil {
		t.Fatal("expected error refusing to clean dashboard dir with non-empty data/")
	}
	if _, err := os.Stat(filepath.Join(root, "data", "runs.json")); err != nil {
		t.Fatalf("data/runs.json should be preserved: %v", err)
	}
}

// TestWriteIssues_StickyClosedIssues verifies that WriteIssues merges back
// previously-known closed issues that are absent from the new batch.
//
// Scenario: The GitHub API returns at most ~100 items. An older closed issue
// (#92) was written in the previous snapshot but is now absent because it
// fell out of the 100-item window. WriteIssues must preserve it so the
// dashboard can correctly classify it as Closed rather than assuming it is open.
func TestWriteIssues_StickyClosedIssues(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if err := os.MkdirAll(filepath.Join(tmp, ".clawflow", "data"), 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	earlier := now.Add(-24 * time.Hour)

	// Simulate the previous snapshot: contains closed issue #92 plus open #200.
	prev := []IssueEntry{
		{Repo: "owner/repo", IssueNumber: 92, IssueTitle: "old bug", State: "closed", CapturedAt: earlier},
		{Repo: "owner/repo", IssueNumber: 200, IssueTitle: "open issue", State: "open", CapturedAt: earlier},
	}
	if err := WriteIssues(prev); err != nil {
		t.Fatalf("first WriteIssues: %v", err)
	}

	// Simulate the next API refresh: #92 is absent (fell out of the window),
	// only #200 (now closed) and a new #205 are returned.
	fresh := []IssueEntry{
		{Repo: "owner/repo", IssueNumber: 200, IssueTitle: "open issue", State: "closed", CapturedAt: now},
		{Repo: "owner/repo", IssueNumber: 205, IssueTitle: "new issue", State: "open", CapturedAt: now},
	}
	if err := WriteIssues(fresh); err != nil {
		t.Fatalf("second WriteIssues: %v", err)
	}

	// Read back and verify.
	data, err := os.ReadFile(filepath.Join(tmp, ".clawflow", "data", "issues.json"))
	if err != nil {
		t.Fatalf("read issues.json: %v", err)
	}
	var got []IssueEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	byNum := make(map[int]IssueEntry)
	for _, e := range got {
		byNum[e.IssueNumber] = e
	}

	// #92 must be preserved (sticky closed).
	e92, ok := byNum[92]
	if !ok {
		t.Fatal("issue #92 was lost — closed issues must be preserved across refreshes")
	}
	if e92.State != "closed" {
		t.Errorf("#92 state=%q, want closed", e92.State)
	}

	// #200 must reflect the new closed state from the fresh batch.
	e200, ok := byNum[200]
	if !ok {
		t.Fatal("issue #200 missing")
	}
	if e200.State != "closed" {
		t.Errorf("#200 state=%q, want closed", e200.State)
	}

	// #205 must be present (new open issue).
	e205, ok := byNum[205]
	if !ok {
		t.Fatal("issue #205 missing")
	}
	if e205.State != "open" {
		t.Errorf("#205 state=%q, want open", e205.State)
	}

	// Total: 3 entries.
	if len(got) != 3 {
		t.Errorf("got %d entries, want 3 (92, 200, 205)", len(got))
	}
}

// TestWriteIssues_StickyDoesNotDuplicateClosedInBatch verifies that closed
// issues already present in the new batch are NOT duplicated by the sticky merge.
func TestWriteIssues_StickyDoesNotDuplicateClosedInBatch(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if err := os.MkdirAll(filepath.Join(tmp, ".clawflow", "data"), 0o755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	earlier := now.Add(-1 * time.Hour)

	prev := []IssueEntry{
		{Repo: "owner/repo", IssueNumber: 10, State: "closed", CapturedAt: earlier},
	}
	if err := WriteIssues(prev); err != nil {
		t.Fatalf("first WriteIssues: %v", err)
	}

	// Same closed #10 is in the fresh batch — must NOT be duplicated.
	fresh := []IssueEntry{
		{Repo: "owner/repo", IssueNumber: 10, State: "closed", CapturedAt: now},
	}
	if err := WriteIssues(fresh); err != nil {
		t.Fatalf("second WriteIssues: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(tmp, ".clawflow", "data", "issues.json"))
	var got []IssueEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d entries, want 1 (no duplicates)", len(got))
	}
	// The fresh entry (newer CapturedAt) should win.
	if !got[0].CapturedAt.Equal(now) {
		t.Errorf("expected fresh CapturedAt to win; got %v", got[0].CapturedAt)
	}
}

// TestWriteIssues_StaleWriterDoesNotRollBack verifies the monotonic merge:
// a writer carrying data captured BEFORE what is already on disk (the
// clawflow-run scan-start snapshot landing after a long operator phase,
// while a dashboard Sync-all wrote fresher state in between) must not roll
// individual entries back.
func TestWriteIssues_StaleWriterDoesNotRollBack(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if err := os.MkdirAll(filepath.Join(tmp, ".clawflow", "data"), 0o755); err != nil {
		t.Fatal(err)
	}

	scanStart := time.Now().UTC().Add(-10 * time.Minute)
	syncTime := time.Now().UTC()

	// Sync-all wrote fresh state: #1 closed, #2 picked up agent-implemented.
	synced := []IssueEntry{
		{Repo: "owner/repo", IssueNumber: 1, State: "closed", CapturedAt: syncTime},
		{Repo: "owner/repo", IssueNumber: 2, State: "open", Labels: []string{"feat", "agent-implemented"}, CapturedAt: syncTime},
	}
	if err := WriteIssues(synced); err != nil {
		t.Fatalf("first WriteIssues: %v", err)
	}

	// The run finishes and writes its scan-start snapshot: #1 still open,
	// #2 without the new label, plus #3 the sync didn't know about.
	stale := []IssueEntry{
		{Repo: "owner/repo", IssueNumber: 1, State: "open", CapturedAt: scanStart},
		{Repo: "owner/repo", IssueNumber: 2, State: "open", Labels: []string{"feat"}, CapturedAt: scanStart},
		{Repo: "owner/repo", IssueNumber: 3, State: "open", CapturedAt: scanStart},
	}
	if err := WriteIssues(stale); err != nil {
		t.Fatalf("second WriteIssues: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, ".clawflow", "data", "issues.json"))
	if err != nil {
		t.Fatalf("read issues.json: %v", err)
	}
	var got []IssueEntry
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	byNum := make(map[int]IssueEntry)
	for _, e := range got {
		byNum[e.IssueNumber] = e
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	if e := byNum[1]; e.State != "closed" || !e.CapturedAt.Equal(syncTime) {
		t.Errorf("#1 rolled back: state=%q captured_at=%v, want closed @ %v", e.State, e.CapturedAt, syncTime)
	}
	if e := byNum[2]; len(e.Labels) != 2 {
		t.Errorf("#2 labels rolled back: %v, want [feat agent-implemented]", e.Labels)
	}
	if _, ok := byNum[3]; !ok {
		t.Error("#3 missing — entries new to the stale batch must still land")
	}
}
