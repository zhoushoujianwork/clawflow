package snapshot

import (
	"context"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMetadataWritersUpsertPostgres(t *testing.T) {
	s := storetest.New(t)
	if _, err := s.Migrate(context.Background(), store.Migrations); err != nil {
		t.Fatal(err)
	}
	BindRunStore(s)
	t.Cleanup(func() { BindRunStore(nil) })
	now := time.Now().UTC()
	dir := t.TempDir()
	m := RunMeta{Repo: "o/r", Operator: "implement", Status: "running", StartedAt: now}
	if err := WriteRunMeta(dir, m); err != nil {
		t.Fatal(err)
	}
	m.Status = "success"
	m.EndedAt = &now
	m.Usage = &Usage{InputTokens: 9, Estimated: true}
	if err := WriteRunMeta(dir, m); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Runs(context.Background(), "run", store.RunFilter{})
	if err != nil || len(rows) != 1 || rows[0].Status != "success" || rows[0].CostUSD != nil || *rows[0].InputTokens != 9 {
		t.Fatalf("rows %+v %v", rows, err)
	}
	pilot := PilotRunMeta{Project: "p", Status: "running", StartedAt: now}
	if err := WritePilotRunMeta(filepath.Join(dir, "pilot"), pilot); err != nil {
		t.Fatal(err)
	}
	pilot.Status = "failed"
	if err := WritePilotRunMeta(filepath.Join(dir, "pilot"), pilot); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Runs(context.Background(), "pilot", store.RunFilter{})
	if err != nil || len(rows) != 1 || rows[0].Status != "failed" {
		t.Fatalf("pilot %+v %v", rows, err)
	}
}

func TestReconcileAndBackfillUpdatePostgres(t *testing.T) {
	s := storetest.New(t)
	if _, err := s.Migrate(context.Background(), store.Migrations); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	BindRunStore(s)
	t.Cleanup(func() { BindRunStore(nil) })
	start := time.Now().UTC().Add(-3 * time.Hour)
	dir := RunDir("o/r", 1, start)
	if err := WriteRunMeta(dir, RunMeta{Repo: "o/r", IssueNumber: 1, Operator: "implement", StartedAt: start, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if n, err := ReconcileStaleRuns(time.Hour); err != nil || n != 1 {
		t.Fatalf("reconcile n=%d err=%v", n, err)
	}
	rows, err := s.Runs(context.Background(), "run", store.RunFilter{})
	if err != nil || len(rows) != 1 || rows[0].Status == "running" {
		t.Fatalf("reconcile rows=%+v err=%v", rows, err)
	}
	events := `{"type":"assistant","message":{"id":"one","model":"model","usage":{"input_tokens":37}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(events), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := BackfillUsage(false); err != nil {
		t.Fatal(err)
	}
	rows, err = s.Runs(context.Background(), "run", store.RunFilter{})
	if err != nil || len(rows) != 1 || rows[0].InputTokens == nil || *rows[0].InputTokens != 37 || rows[0].CostUSD != nil {
		t.Fatalf("backfill rows=%+v err=%v", rows, err)
	}
}

func TestTerminalUsageWithoutCostRemainsUnreported(t *testing.T) {
	s := storetest.New(t)
	if _, err := s.Migrate(context.Background(), store.Migrations); err != nil {
		t.Fatal(err)
	}
	BindRunStore(s)
	t.Cleanup(func() { BindRunStore(nil) })
	for _, tc := range []struct {
		name, event string
		missing     bool
	}{
		{"absent", `{"type":"result","usage":{"input_tokens":42}}`, true},
		{"zero", `{"type":"result","total_cost_usd":0,"usage":{"input_tokens":42}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(tc.event+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			u, err := ExtractUsage(filepath.Join(dir, "events.jsonl"))
			if err != nil || u == nil || u.CostUnavailable != tc.missing {
				t.Fatalf("usage=%+v err=%v", u, err)
			}
			if err := WriteRunMeta(dir, RunMeta{Repo: "o/" + tc.name, Operator: "implement", Status: "success", StartedAt: time.Now().UTC(), Usage: u}); err != nil {
				t.Fatal(err)
			}
			rows, err := s.Runs(context.Background(), "run", store.RunFilter{Repo: "o/" + tc.name})
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			if (rows[0].CostUSD == nil) != tc.missing || *rows[0].InputTokens != 42 {
				t.Fatalf("row=%+v", rows[0])
			}
		})
	}
}
