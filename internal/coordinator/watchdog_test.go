package coordinator

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWatchdogFindings(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { at := now.Add(-d); return &at }
	future := now.Add(time.Hour)
	seen := now.Add(-time.Second)
	free := []Worker{{ID: "w", RunUntagged: true, MaxParallel: 2}}
	open := dispatchLimits{known: true}
	for _, tc := range []struct {
		name    string
		task    BoardTask
		workers []Worker
		limits  dispatchLimits
		rule    string
	}{
		{name: "recovery below threshold", task: BoardTask{State: "leased", Durable: true, LeaseExpiresAt: ago(recoveryStallAfter - time.Second)}},
		{name: "recovery at threshold", task: BoardTask{State: "leased", Durable: true, LeaseExpiresAt: ago(recoveryStallAfter)}, rule: RuleRecoveryStalled},
		{name: "finalizing below threshold", task: BoardTask{State: "leased", UpdatedAt: *ago(finalizingStallAfter - time.Second)}},
		{name: "finalizing stalled", task: BoardTask{State: "leased", UpdatedAt: *ago(finalizingStallAfter)}, rule: RuleFinalizingStalled},
		{name: "unknown progress is not a finding", task: BoardTask{State: "leased", LeaseExpiresAt: &future, UpdatedAt: *ago(24 * time.Hour)}},
		{name: "recent progress", task: BoardTask{State: "leased", LeaseExpiresAt: &future, LastEventAt: ago(noProgressAfter - time.Second)}},
		{name: "no progress", task: BoardTask{State: "leased", LeaseExpiresAt: &future, LastEventAt: ago(noProgressAfter)}, rule: RuleNoProgress},
		{name: "dispatch stalled", task: BoardTask{State: "pending", UpdatedAt: *ago(dispatchStallAfter)}, workers: free, limits: open, rule: RuleDispatchStalled},
		{name: "dispatch recent", task: BoardTask{State: "pending", UpdatedAt: *ago(dispatchStallAfter - time.Second)}, workers: free, limits: open},
		{name: "unknown limits", task: BoardTask{State: "pending", UpdatedAt: *ago(time.Hour)}, workers: free},
		{name: "platform limit", task: BoardTask{State: "pending", UpdatedAt: *ago(time.Hour)}, workers: free, limits: dispatchLimits{known: true, total: 1}},
		{name: "repo limit", task: BoardTask{Repo: "o/r", State: "pending", UpdatedAt: *ago(time.Hour)}, workers: free, limits: dispatchLimits{known: true, repos: map[string]int{"o/r": 1}}},
		{name: "paused worker", task: BoardTask{State: "pending", UpdatedAt: *ago(time.Hour)}, workers: []Worker{{ID: "w", RunUntagged: true, MaxParallel: 2, Paused: true}}, limits: open},
		{name: "full worker", task: BoardTask{State: "pending", UpdatedAt: *ago(time.Hour)}, workers: []Worker{{ID: "w", RunUntagged: true, MaxParallel: 1}}, limits: open},
		{name: "tags do not match", task: BoardTask{State: "pending", Tags: []string{"gpu"}, UpdatedAt: *ago(time.Hour)}, workers: free, limits: open},
		{name: "no worker", task: BoardTask{State: "pending", UpdatedAt: *ago(time.Hour)}, limits: open},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.task.ID = "task"
			tasks := []BoardTask{tc.task}
			// The limit cases need one other leased task, owned by the worker.
			if strings.HasSuffix(tc.name, "limit") || tc.name == "full worker" {
				tasks = append(tasks, BoardTask{ID: "other", Repo: tc.task.Repo, State: "leased", Worker: "w", LeaseExpiresAt: &future})
			}
			tokens := []Token{}
			for _, w := range tc.workers {
				tokens = append(tokens, Token{Name: w.ID, LastSeen: &seen})
			}
			b := deriveBoard(now, tc.workers, tokens, nil, tasks)
			b.applyFindings(now, tc.workers, tc.limits)
			if tc.rule == "" {
				if len(b.Findings) != 0 {
					t.Fatalf("findings: %+v", b.Findings)
				}
				return
			}
			if len(b.Findings) != 1 || b.Findings[0].Rule != tc.rule || b.Findings[0].TaskID != "task" || b.Findings[0].AgeSec <= 0 {
				t.Fatalf("findings: %+v", b.Findings)
			}
			if !strings.HasPrefix(b.Tasks[0].WaitingReason, "Watchdog: ") {
				t.Fatalf("reason: %q", b.Tasks[0].WaitingReason)
			}
		})
	}
}

func TestWatchdogKeepsExistingRecoveryReason(t *testing.T) {
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Hour)
	b := deriveBoard(now, nil, nil, nil, []BoardTask{{ID: "task", State: "leased", Durable: true, LeaseExpiresAt: &expired}})
	b.applyFindings(now, nil, dispatchLimits{known: true})
	if reason := b.Tasks[0].WaitingReason; !strings.Contains(reason, "for 60 min") || !strings.Contains(reason, "Work remains reserved") {
		t.Fatalf("reason: %q", reason)
	}
}

func TestFindingTrackerReportsTransitionsOnce(t *testing.T) {
	a, b := Finding{TaskID: "a", Rule: RuleNoProgress}, Finding{TaskID: "b", Rule: RuleDispatchStalled}
	tracker := &FindingTracker{}
	if opened, resolved := tracker.Update([]Finding{a}); len(opened) != 1 || len(resolved) != 0 {
		t.Fatalf("first: %v %v", opened, resolved)
	}
	if opened, resolved := tracker.Update([]Finding{a}); len(opened) != 0 || len(resolved) != 0 {
		t.Fatalf("repeat: %v %v", opened, resolved)
	}
	if opened, resolved := tracker.Update([]Finding{b}); len(opened) != 1 || opened[0].TaskID != "b" || len(resolved) != 1 || resolved[0].TaskID != "a" {
		t.Fatalf("change: %v %v", opened, resolved)
	}
}

func TestReportProgressStore(t *testing.T) {
	s, _, _ := setup(t)
	register(t, s, "w", 1)
	enqueue(t, s, 1)
	task, err := s.Claim(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	read := func() *time.Time {
		t.Helper()
		var row Task
		if err := s.db.First(&row, "id = ?", task.ID).Error; err != nil {
			t.Fatal(err)
		}
		return row.LastEventAt
	}
	if read() != nil {
		t.Fatal("a new claim has no known progress")
	}
	for _, age := range []int64{-1, MaxProgressAgeSec + 1} {
		if err := s.ReportProgress(ctx, task.ID, "w", task.FencingToken, age); !errors.Is(err, ErrInvalid) {
			t.Fatalf("age %d: %v", age, err)
		}
	}
	if err := s.ReportProgress(ctx, task.ID, "other", task.FencingToken, 0); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("wrong worker: %v", err)
	}
	if err := s.ReportProgress(ctx, task.ID, "w", task.FencingToken+1, 0); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("wrong fence: %v", err)
	}
	var before Task
	if err := s.db.First(&before, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ReportProgress(ctx, task.ID, "w", task.FencingToken, 3600); err != nil {
		t.Fatal(err)
	}
	old := read()
	if old == nil || time.Since(*old) < 59*time.Minute {
		t.Fatalf("resolved time: %v", old)
	}
	if err := s.ReportProgress(ctx, task.ID, "w", task.FencingToken, 0); err != nil {
		t.Fatal(err)
	}
	recent := read()
	if time.Since(*recent) > time.Minute {
		t.Fatalf("recent: %v", recent)
	}
	if err := s.ReportProgress(ctx, task.ID, "w", task.FencingToken, 7200); err != nil {
		t.Fatal(err)
	}
	if got := read(); !got.Equal(*recent) {
		t.Fatalf("progress moved backwards: %v -> %v", recent, got)
	}
	var after Task
	if err := s.db.First(&after, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !after.LeaseExpiresAt.Equal(*before.LeaseExpiresAt) || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("progress renewed the lease: %+v", after)
	}
	board, err := s.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if board.Tasks[0].LastEventAt == nil || board.Findings == nil {
		t.Fatalf("board: %+v", board)
	}
	raw, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "last_event") {
		t.Fatalf("worker task JSON exposes progress: %s", raw)
	}
}

func TestClaimClearsProgressOfPreviousAttempt(t *testing.T) {
	s, _, _ := setup(t)
	register(t, s, "w", 1)
	enqueue(t, s, 1)
	task, err := s.Claim(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReportProgress(ctx, task.ID, "w", task.FencingToken, 7200); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, task.ID, "w", task.FencingToken, "test"); err != nil {
		t.Fatal(err)
	}
	again, err := s.Claim(ctx, "w")
	if err != nil {
		t.Fatal(err)
	}
	if again.LastEventAt != nil {
		t.Fatalf("stale progress: %v", again.LastEventAt)
	}
}
