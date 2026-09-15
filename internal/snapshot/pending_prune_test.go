package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPrunePending_StaleAfterTerminalRun covers issue: a pending entry for
// (repo, issue, operator) must be dropped once a run of that same operator
// has started at or after the entry was captured, regardless of the run's
// outcome. This is the self-healing path for when the `clawflow run`
// process that fired the operator was interrupted before reaching its own
// end-of-pass pending.json rewrite.
func TestPrunePending_StaleAfterTerminalRun(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// No config.Load() repos — repoExists would be empty and every entry
	// pruned by the "repo no longer in config" rule. Write a minimal
	// config so the repo is recognized.
	cfgDir := filepath.Join(tmp, ".clawflow", "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configYAML := "repos:\n  owner/repo:\n    platform: github\n    base_branch: main\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	capturedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	runStartedAfter := capturedAt.Add(1 * time.Hour)
	runStartedBefore := capturedAt.Add(-1 * time.Hour)

	pending := []PendingEntry{
		// #1: implement already ran (success) after this was captured — stale, must be pruned.
		{Repo: "owner/repo", IssueNumber: 1, Operator: "implement", CapturedAt: capturedAt},
		// #2: implement ran but FAILED after capture — still stale, must be pruned (any terminal status counts).
		{Repo: "owner/repo", IssueNumber: 2, Operator: "implement", CapturedAt: capturedAt},
		// #3: implement's only run started BEFORE capture — genuinely still queued, must be kept.
		{Repo: "owner/repo", IssueNumber: 3, Operator: "implement", CapturedAt: capturedAt},
		// #4: no run at all for this operator — genuinely still queued, must be kept.
		{Repo: "owner/repo", IssueNumber: 4, Operator: "evaluate-bug", CapturedAt: capturedAt},
	}
	if err := WritePending(pending); err != nil {
		t.Fatalf("WritePending: %v", err)
	}

	runs := []RunIndexEntry{
		{RunMeta: RunMeta{Repo: "owner/repo", IssueNumber: 1, Operator: "implement", Status: "success", StartedAt: runStartedAfter}},
		{RunMeta: RunMeta{Repo: "owner/repo", IssueNumber: 2, Operator: "implement", Status: "failed", StartedAt: runStartedAfter}},
		{RunMeta: RunMeta{Repo: "owner/repo", IssueNumber: 3, Operator: "implement", Status: "success", StartedAt: runStartedBefore}},
	}
	if err := writeJSON(filepath.Join(DataDir(), "runs.json"), runs); err != nil {
		t.Fatalf("write runs.json: %v", err)
	}

	removed := PrunePending()
	if removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}

	raw, err := os.ReadFile(filepath.Join(DataDir(), "pending.json"))
	if err != nil {
		t.Fatalf("read pending.json: %v", err)
	}
	var kept []PendingEntry
	if err := json.Unmarshal(raw, &kept); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("kept = %d entries, want 2: %+v", len(kept), kept)
	}
	seen := map[int]bool{}
	for _, p := range kept {
		seen[p.IssueNumber] = true
	}
	if !seen[3] || !seen[4] {
		t.Fatalf("expected issues #3 and #4 to survive, got %+v", kept)
	}
}
