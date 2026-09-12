package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestBackfillUsage_DryRunThenApply covers issue #322's one-time recovery of
// historical rows: wakes and operator runs whose meta.json carries usage=null
// but whose events.jsonl still has per-message token counts. A dry run must
// report without touching disk; --apply must write the recovered figures.
func TestBackfillUsage_DryRunThenApply(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	events := `{"type":"system","subtype":"init"}` + "\n" +
		`{"type":"assistant","message":{"id":"m1","model":"claude-opus-5","usage":{"input_tokens":2000,"output_tokens":100}}}` + "\n"

	// A pilot wake frozen in "running" for hours.
	pilotStart := time.Now().UTC().Add(-4 * time.Hour)
	pilotDir := makePilotRunDir(t, filepath.Join(DataDir(), "pilot-runs"), "eda", pilotStart,
		PilotRunMeta{Project: "eda", StartedAt: pilotStart, Status: "running"}, events)

	// A failed operator run with no result event.
	opStart := time.Now().UTC().Add(-5 * time.Hour)
	opDir := filepath.Join(DataDir(), "runs", "owner__repo", "issue-9",
		opStart.Format("2006-01-02T15-04-05Z"))
	if err := os.MkdirAll(opDir, 0o755); err != nil {
		t.Fatalf("mkdir op run dir: %v", err)
	}
	opMeta := RunMeta{
		Operator: "implement", Repo: "owner/repo", IssueNumber: 9,
		StartedAt: opStart, Status: "failed",
	}
	if err := WriteRunMeta(opDir, opMeta); err != nil {
		t.Fatalf("write op meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(opDir, "events.jsonl"), []byte(events), 0o644); err != nil {
		t.Fatalf("write op events: %v", err)
	}

	// Dry run: reports both, writes nothing.
	entries, err := BackfillUsage(true)
	if err != nil {
		t.Fatalf("BackfillUsage(dry): %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("dry run entries=%d, want 2 (%+v)", len(entries), entries)
	}
	for _, e := range entries {
		if e.Written {
			t.Errorf("dry run must not write: %+v", e)
		}
		if e.Usage == nil || e.Usage.InputTokens != 2000 {
			t.Errorf("entry %s: usage=%+v, want 2000 input tokens", e.Label, e.Usage)
		}
		if !e.Usage.Estimated {
			t.Errorf("entry %s: recovered usage should be flagged estimated", e.Label)
		}
	}
	if got := readPilotMeta(t, pilotDir); got.Usage != nil {
		t.Errorf("dry run mutated pilot meta: %+v", got.Usage)
	}

	// Apply: figures land on disk.
	entries, err = BackfillUsage(false)
	if err != nil {
		t.Fatalf("BackfillUsage(apply): %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("apply entries=%d, want 2", len(entries))
	}
	for _, e := range entries {
		if !e.Written {
			t.Errorf("apply should write %s", e.Label)
		}
	}
	if got := readPilotMeta(t, pilotDir); got.Usage == nil || got.Usage.InputTokens != 2000 {
		t.Errorf("pilot usage not persisted: %+v", got.Usage)
	}
	data, _ := os.ReadFile(filepath.Join(opDir, "meta.json"))
	var gotOp RunMeta
	if err := json.Unmarshal(data, &gotOp); err != nil {
		t.Fatalf("read op meta: %v", err)
	}
	if gotOp.Usage == nil || gotOp.Usage.InputTokens != 2000 {
		t.Errorf("operator usage not persisted: %+v", gotOp.Usage)
	}

	// Idempotent: a second pass finds nothing left to do.
	entries, err = BackfillUsage(false)
	if err != nil {
		t.Fatalf("BackfillUsage(second apply): %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("second pass entries=%d, want 0", len(entries))
	}
}

// A live run must never be backfilled with a partial figure.
func TestBackfillUsage_SkipsLiveRun(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	start := time.Now().UTC().Add(-30 * time.Second)
	dir := filepath.Join(DataDir(), "runs", "owner__repo", "issue-1",
		start.Format("2006-01-02T15-04-05Z"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := WriteRunMeta(dir, RunMeta{
		Operator: "classify", Repo: "owner/repo", IssueNumber: 1,
		StartedAt: start, Status: "running",
	}); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"),
		[]byte(`{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":10}}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	entries, err := BackfillUsage(false)
	if err != nil {
		t.Fatalf("BackfillUsage: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("entries=%d, want 0 (live run must be skipped): %+v", len(entries), entries)
	}
}
