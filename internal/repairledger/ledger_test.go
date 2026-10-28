package repairledger

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

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
