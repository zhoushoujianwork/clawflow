package coordinator

import (
	"path/filepath"
	"testing"
	"time"
)

func failures(now time.Time, n int, kind, errText, head string) []FinishedTask {
	var out []FinishedTask
	for i := 0; i < n; i++ {
		out = append(out, FinishedTask{Repo: "o/r", Issue: 7, State: "failed", InfraKind: kind, Err: errText, Head: head, UpdatedAt: now.Add(-time.Duration(i+1) * time.Minute)})
	}
	return out
}

func TestRepeatedFailure(t *testing.T) {
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	cfg := WatchdogConfig{FailureThreshold: 3}
	if got := repeatedFailureFindings(now, failures(now, 3, "", "operator produced no outcome marker", ""), cfg); len(got) != 1 || got[0].Rule != RuleRepeatedFailure || got[0].Issue != 7 || got[0].Repo != "o/r" {
		t.Fatalf("no-marker: %+v", got)
	}
	if got := repeatedFailureFindings(now, failures(now, 2, "", "operator produced no outcome marker", ""), cfg); len(got) != 0 {
		t.Fatalf("below threshold: %+v", got)
	}
	cfg5 := WatchdogConfig{FailureThreshold: 5}
	if got := repeatedFailureFindings(now, failures(now, 5, "coordination-changed", "", ""), cfg5); len(got) != 1 {
		t.Fatalf("zero-cost coordination-changed: %+v", got)
	}
	// Mixed reasons do not accumulate.
	mixed := append(failures(now, 2, "launch", "", ""), failures(now.Add(-time.Hour), 2, "coordination-changed", "", "")...)
	if got := repeatedFailureFindings(now, mixed, cfg); len(got) != 0 {
		t.Fatalf("mixed: %+v", got)
	}
	// A new head resets the count.
	reset := append(failures(now, 2, "launch", "", "new"), failures(now.Add(-time.Hour), 3, "launch", "", "old")...)
	if got := repeatedFailureFindings(now, reset, cfg); len(got) != 0 {
		t.Fatalf("new head: %+v", got)
	}
	// A success resets the count.
	ok := append(failures(now, 2, "launch", "", ""), FinishedTask{Repo: "o/r", Issue: 7, State: "done", UpdatedAt: now.Add(-3 * time.Minute)})
	ok = append(ok, failures(now.Add(-time.Hour), 3, "launch", "", "")...)
	if got := repeatedFailureFindings(now, ok, cfg); len(got) != 0 {
		t.Fatalf("success: %+v", got)
	}
	// Failures outside the window are ignored.
	if got := repeatedFailureFindings(now, failures(now.Add(-48*time.Hour), 4, "launch", "", ""), cfg); len(got) != 0 {
		t.Fatalf("window: %+v", got)
	}
}

func TestOrphanedPR(t *testing.T) {
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	cfg := WatchdogConfig{OrphanAfter: 30 * time.Minute}
	old := now.Add(-time.Hour)
	for i, cause := range []string{OrphanAcceptanceBlocked, OrphanRepairExhausted, OrphanBranchUnmanaged, OrphanIssueUnlinked} {
		pr := OrphanedPR{Repo: "o/r", PR: 10 + i, Issue: 1, Cause: cause, Since: old}
		if cause == OrphanIssueUnlinked {
			pr.Issue = 0
		}
		got := orphanedPRFindings(now, []OrphanedPR{pr}, nil, cfg)
		if len(got) != 1 || got[0].Rule != RuleOrphanedPR || got[0].PR != pr.PR || got[0].Reason == "" {
			t.Fatalf("%s: %+v", cause, got)
		}
	}
	pr := OrphanedPR{Repo: "o/r", PR: 10, Issue: 1, Cause: OrphanAcceptanceBlocked, Since: old}
	if got := orphanedPRFindings(now, []OrphanedPR{pr}, []BoardTask{{Repo: "o/r", PR: 10, State: "leased"}}, cfg); len(got) != 0 {
		t.Fatalf("active task: %+v", got)
	}
	pr.Since = now.Add(-time.Minute)
	if got := orphanedPRFindings(now, []OrphanedPR{pr}, nil, cfg); len(got) != 0 {
		t.Fatalf("below threshold: %+v", got)
	}
}

func TestFileTrackerDoesNotReopenAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	f := Finding{Repo: "o/r", Issue: 7, Rule: RuleRepeatedFailure}
	opened, _ := NewFileFindingTracker(path).Update([]Finding{f})
	if len(opened) != 1 {
		t.Fatalf("first open: %+v", opened)
	}
	restarted := NewFileFindingTracker(path)
	if opened, _ := restarted.Update([]Finding{f}); len(opened) != 0 {
		t.Fatalf("reopened after restart: %+v", opened)
	}
	if _, resolved := NewFileFindingTracker(path).Update(nil); len(resolved) != 1 {
		t.Fatalf("resolve after restart: %+v", resolved)
	}
}

func TestSameHeadCycle(t *testing.T) {
	now := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	cfg := WatchdogConfig{FailureThreshold: 3}
	mk := func(kind, head string, i int) FinishedTask {
		return FinishedTask{Repo: "o/r", Issue: 7, PR: 11, State: "done", Kind: kind, Head: head, UpdatedAt: now.Add(-time.Duration(i) * time.Minute)}
	}
	cycle := []FinishedTask{mk("repair", "h1", 1), mk("acceptance", "h1", 2), mk("repair", "h1", 3)}
	got := sameHeadCycleFindings(now, cycle, cfg)
	if len(got) != 1 || got[0].Rule != RuleSameHeadCycle || got[0].PR != 11 {
		t.Fatalf("cycle: %+v", got)
	}
	// Different heads, too few tasks, or no repair involved: no finding.
	if got := sameHeadCycleFindings(now, []FinishedTask{mk("repair", "h1", 1), mk("acceptance", "h2", 2), mk("repair", "h3", 3)}, cfg); len(got) != 0 {
		t.Fatalf("new heads: %+v", got)
	}
	if got := sameHeadCycleFindings(now, cycle[:2], cfg); len(got) != 0 {
		t.Fatalf("below threshold: %+v", got)
	}
	if got := sameHeadCycleFindings(now, []FinishedTask{mk("acceptance", "h1", 1), mk("acceptance", "h1", 2), mk("acceptance", "h1", 3)}, cfg); len(got) != 0 {
		t.Fatalf("no repair: %+v", got)
	}
}
