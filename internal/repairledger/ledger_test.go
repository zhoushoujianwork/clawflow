package repairledger

import (
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
