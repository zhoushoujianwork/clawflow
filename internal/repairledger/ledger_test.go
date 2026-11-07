package repairledger

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestResourceContentionDoesNotConsumeRepairBudgets(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	pr := vcs.PR{Number: 1, HeadSHA: "head"}
	for i := 0; i < MaxInfra+MaxAttempts+1; i++ {
		now = now.Add(24 * time.Hour)
		n, err := Reserve(root, "o/r", pr, "ci", now)
		if err != nil || n != 1 {
			t.Fatalf("reserve: %d %v", n, err)
		}
		if err := Release(root, "o/r", pr, n, "resource-busy", now); err != nil {
			t.Fatal(err)
		}
	}
	_, path := Path(root, "o/r", 1)
	l, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if consumed, infra := l.Counts(); consumed != 0 || infra != 0 {
		t.Fatalf("busy consumed budgets: %d %d", consumed, infra)
	}
}

func TestRerunReservationSharedBudgetAndConcurrency(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	pr := vcs.PR{Number: 1, HeadSHA: "head"}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- ReserveRerun(root, "o/r", pr, now) }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if err.Error() != "already_rerun_for_head" {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatal(successes)
	}
	_, path := Path(root, "o/r", 1)
	l, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c, i := l.Counts(); c != 0 || i != 1 {
		t.Fatalf("counts %d %d", c, i)
	}
	if _, err := Reserve(root, "o/r", pr, "ci", now); err == nil {
		t.Fatal("ignored cooldown")
	}
	if n, err := Reserve(root, "o/r", pr, "ci", now.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	for i := 1; i < MaxInfra; i++ {
		pr.HeadSHA = string(rune('a' + i))
		if err := ReserveRerun(root, "o/r", pr, now.Add(time.Duration(i+1)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	pr.HeadSHA = "last"
	if err := ReserveRerun(root, "o/r", pr, now.Add(24*time.Hour)); err == nil || !strings.Contains(err.Error(), "infrastructure budget exhausted") {
		t.Fatal(err)
	}
}

func TestCoordinatorResultReplayAndLegacyReservation(t *testing.T) {
	for _, reserved := range []bool{false, true} {
		for _, kind := range []string{"", "launch", "no-change"} {
			root := t.TempDir()
			now := time.Now()
			pr := vcs.PR{Number: 7, HeadSHA: "head"}
			if reserved {
				if n, err := Reserve(root, "o/r", pr, "ci", now); err != nil || n != 1 {
					t.Fatalf("reserve: %d %v", n, err)
				}
			}
			for i := 0; i < 3; i++ {
				if err := ApplyResult(root, "o/r", pr, 1, "task", "ci", kind, now.Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			_, path := Path(root, "o/r", 7)
			l, err := Load(path)
			if err != nil || len(l.Attempts) != 1 {
				t.Fatalf("ledger: %+v %v", l, err)
			}
			a := l.Attempts[0]
			if a.Released != kind || !a.Completed || a.TaskID != "task" {
				t.Fatalf("attempt: %+v", a)
			}
			// The same head and attempt number can be reused after an infra release.
			if kind == "launch" {
				if n, err := Reserve(root, "o/r", pr, "ci", now.Add(3*time.Hour)); err != nil || n != 1 {
					t.Fatalf("next: %d %v", n, err)
				}
				if err := ApplyResult(root, "o/r", pr, 1, "next-task", "ci", "", now.Add(4*time.Hour)); err != nil {
					t.Fatal(err)
				}
				l, _ = Load(path)
				if len(l.Attempts) != 2 || !l.Attempts[1].Completed || l.Attempts[1].Released != "" {
					t.Fatalf("next ledger: %+v", l)
				}
			}
		}
	}
}

func TestCoordinationChangedConsumesNoBudgetOrBackoff(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	pr := vcs.PR{Number: 1, HeadSHA: "head"}
	for i := 0; i < MaxInfra+3; i++ {
		now = now.Add(10 * time.Minute)
		n, err := Reserve(root, "o/r", pr, "ci", now)
		if err != nil {
			t.Fatalf("locked after %d coordination releases: %v", i, err)
		}
		if err := Release(root, "o/r", pr, n, ReleaseCoordinationChanged, now); err != nil {
			t.Fatal(err)
		}
	}
	_, path := Path(root, "o/r", 1)
	l, _ := Load(path)
	if c, infra := l.Counts(); c != 0 || infra != 0 || !l.NotBefore.IsZero() {
		t.Fatalf("budget/backoff changed: %d %d %v", c, infra, l.NotBefore)
	}
	// Reservations already charged by older releases no longer count.
	old := Ledger{}
	for i := 0; i < MaxInfra; i++ {
		old.Attempts = append(old.Attempts, Attempt{Attempt: 1, Released: "coordination-changed"})
	}
	if err := Budget(old, now.Add(time.Hour)); err != nil {
		t.Fatalf("legacy charged ledger stays locked: %v", err)
	}
}

func TestAcceptanceNoChangeSpendsBudgetAndStopsHead(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Add(-48 * time.Hour)
	pr := vcs.PR{Number: 9, HeadSHA: "h1"}
	for i := 1; i <= MaxAttempts; i++ {
		now = now.Add(time.Hour)
		n, err := Reserve(root, "o/r", pr, ReasonAcceptance, now)
		if err != nil || n != i {
			t.Fatalf("reserve %d: %d %v", i, n, err)
		}
		if err := Release(root, "o/r", pr, n, ReleaseNoChange, now); err != nil {
			t.Fatal(err)
		}
	}
	if !NoChangeAtHead(root, "o/r", 9, "h1") || NoChangeAtHead(root, "o/r", 9, "h2") {
		t.Fatal("no-change must bind to the exact head")
	}
	// Repeated no-change is bounded: the budget is spent after MaxAttempts.
	if _, err := Reserve(root, "o/r", pr, ReasonAcceptance, now.Add(time.Hour)); !errors.Is(err, ErrExhausted) {
		t.Fatalf("want exhausted, got %v", err)
	}
}

func TestNoChangeForCIReasonAndInfraStayNonConsuming(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Add(-48 * time.Hour)
	pr := vcs.PR{Number: 10, HeadSHA: "h1"}
	for i, kind := range []string{"capacity", "launch"} {
		now = now.Add(3 * time.Hour)
		n, err := Reserve(root, "o/r", pr, ReasonAcceptance, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := Release(root, "o/r", pr, n, kind, now); err != nil {
			t.Fatal(err)
		}
		_, path := Path(root, "o/r", 10)
		l, _ := Load(path)
		if c, inf := l.Counts(); c != 0 || inf != i+1 {
			t.Fatalf("%s: consumed=%d infra=%d", kind, c, inf)
		}
		if l.NoChangeAtHead("h1") {
			t.Fatal("infra release is not a no-change conclusion")
		}
	}
	now = now.Add(3 * time.Hour)
	n, _ := Reserve(root, "o/r", pr, "ci", now)
	if err := Release(root, "o/r", pr, n, ReleaseNoChange, now); err != nil {
		t.Fatal(err)
	}
	_, path := Path(root, "o/r", 10)
	l, _ := Load(path)
	// A CI no-change spends the budget (#768) but never counts as an
	// acceptance conclusion: acceptance review and repair stay untouched.
	if c, _ := l.Counts(); c != 1 || l.NoChangeAtHead("h1") || !l.CINoChangeAtHead("h1") {
		t.Fatalf("ci no-change: consumed=%d acceptance=%v ci=%v", c, l.NoChangeAtHead("h1"), l.CINoChangeAtHead("h1"))
	}
}

func TestCINoChangeBindsToHeadSpendsBudgetAndNotifiesOnce(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Add(-48 * time.Hour)
	pr := vcs.PR{Number: 12, HeadSHA: "h1"}
	if CINoChangeAtHead(root, "o/r", 12, "h1") || CINoChangeNeedsNotice(root, "o/r", 12, "h1") {
		t.Fatal("empty ledger reports a conclusion")
	}
	if err := MarkCINoChangeNotified(root, "o/r", 12, "h1"); err != nil {
		t.Fatalf("mark on empty ledger: %v", err)
	}
	n, err := Reserve(root, "o/r", pr, ReasonCI, now)
	if err != nil {
		t.Fatal(err)
	}
	if CINoChangeAtHead(root, "o/r", 12, "h1") {
		t.Fatal("an open reservation is not a conclusion")
	}
	if err := Release(root, "o/r", pr, n, ReleaseNoChange, now); err != nil {
		t.Fatal(err)
	}
	if !CINoChangeAtHead(root, "o/r", 12, "h1") || CINoChangeAtHead(root, "o/r", 12, "h2") || CINoChangeAtHead(root, "o/r", 12, "") {
		t.Fatal("conclusion must bind to the exact head")
	}
	if NoChangeAtHead(root, "o/r", 12, "h1") {
		t.Fatal("CI conclusion leaked into the acceptance gate")
	}
	if !CINoChangeNeedsNotice(root, "o/r", 12, "h1") {
		t.Fatal("a fresh conclusion needs a notice")
	}
	if err := MarkCINoChangeNotified(root, "o/r", 12, "h1"); err != nil {
		t.Fatal(err)
	}
	if CINoChangeNeedsNotice(root, "o/r", 12, "h1") || !CINoChangeAtHead(root, "o/r", 12, "h1") {
		t.Fatal("notice flag must not clear the conclusion")
	}
	// A different head's conclusion is noticed independently.
	pr2 := vcs.PR{Number: 12, HeadSHA: "h2"}
	n, err = Reserve(root, "o/r", pr2, ReasonCI, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := Release(root, "o/r", pr2, n, ReleaseNoChange, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !CINoChangeNeedsNotice(root, "o/r", 12, "h2") || CINoChangeNeedsNotice(root, "o/r", 12, "h1") {
		t.Fatal("notice state must be per head")
	}
	// Both no-changes spent budget; the third attempt is the last, then exhausted.
	if c, infra := loadCounts(t, root, 12); c != 2 || infra != 0 {
		t.Fatalf("consumed=%d infra=%d", c, infra)
	}
	later := now.Add(3 * time.Hour)
	pr3 := vcs.PR{Number: 12, HeadSHA: "h3"}
	n, err = Reserve(root, "o/r", pr3, ReasonCI, later)
	if err != nil || n != 3 {
		t.Fatalf("third attempt: %d %v", n, err)
	}
	if err := Release(root, "o/r", pr3, n, ReleaseNoChange, later); err != nil {
		t.Fatal(err)
	}
	if _, err := Reserve(root, "o/r", vcs.PR{Number: 12, HeadSHA: "h4"}, ReasonCI, later.Add(24*time.Hour)); !errors.Is(err, ErrExhausted) {
		t.Fatalf("a new head must not bypass the per-PR cap, got %v", err)
	}
}

func loadCounts(t *testing.T, root string, pr int) (int, int) {
	t.Helper()
	_, path := Path(root, "o/r", pr)
	l, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return l.Counts()
}

// Coordinator mode: the worker reports kind "no-change" and the controller's
// ledger records the conclusion through ApplyResult, with or without a prior
// reservation, and replaying the result changes nothing.
func TestApplyResultRecordsCINoChangeForCoordinator(t *testing.T) {
	for _, reserved := range []bool{false, true} {
		root := t.TempDir()
		now := time.Now().UTC().Add(-time.Hour)
		pr := vcs.PR{Number: 13, HeadSHA: "h1"}
		if reserved {
			if n, err := Reserve(root, "o/r", pr, ReasonCI, now); err != nil || n != 1 {
				t.Fatalf("reserve: %d %v", n, err)
			}
		}
		for i := 0; i < 2; i++ {
			if err := ApplyResult(root, "o/r", pr, 1, "task-1", ReasonCI, ReleaseNoChange, now.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
		if !CINoChangeAtHead(root, "o/r", 13, "h1") || CINoChangeAtHead(root, "o/r", 13, "h2") || NoChangeAtHead(root, "o/r", 13, "h1") {
			t.Fatalf("reserved=%v: conclusion not bound to the CI gate at this head", reserved)
		}
		_, path := Path(root, "o/r", 13)
		l, _ := Load(path)
		if len(l.Attempts) != 1 {
			t.Fatalf("reserved=%v: replay duplicated the attempt: %+v", reserved, l.Attempts)
		}
		if c, infra := l.Counts(); c != 1 || infra != 0 {
			t.Fatalf("reserved=%v: consumed=%d infra=%d", reserved, c, infra)
		}
	}
}
