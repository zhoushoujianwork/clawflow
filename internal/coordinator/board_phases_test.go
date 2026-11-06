package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBoardRecordsPhasesNotInJSON(t *testing.T) {
	s, _, _ := setup(t)
	b, err := s.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	summary := b.PhaseSummary()
	for _, name := range []string{"acquire_connection", "begin", "query_workers", "query_tasks", "derive_board", "query_history", "findings", "commit"} {
		if !strings.Contains(summary, name+"=") {
			t.Fatalf("phase %s missing: %q", name, summary)
		}
	}
	raw, _ := json.Marshal(b)
	if strings.Contains(string(raw), "Phases") || strings.Contains(string(raw), "begin") {
		t.Fatalf("phases leaked into JSON: %s", raw)
	}
}

func TestBoardSlowLeaseLimitsShowsInPhases(t *testing.T) {
	s, _, _ := setup(t)
	s.LeaseLimits = func() (int, map[string]int, error) {
		time.Sleep(30 * time.Millisecond)
		return 1, nil, nil
	}
	b, err := s.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Phases) == 0 || b.Phases[0].Name != "lease_limits" || b.Phases[0].Duration < 30*time.Millisecond {
		t.Fatalf("phases: %+v", b.Phases)
	}
}

func TestBoardErrorNamesCompletedAndRunningPhases(t *testing.T) {
	s, _, _ := setup(t)
	s.LeaseLimits = func() (int, map[string]int, error) { return 1, nil, nil }
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Board(cctx)
	if err == nil {
		t.Fatal("expected error with canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("original error lost: %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "lease_limits=") || !strings.Contains(msg, "in progress: acquire_connection") {
		t.Fatalf("error lacks phases: %q", msg)
	}
}
