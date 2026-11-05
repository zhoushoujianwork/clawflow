package repairledger

import (
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestCancelRerunsBoundedPerHeadAndLeaveRepairLedger(t *testing.T) {
	root := t.TempDir()
	pr := vcs.PR{Number: 7, HeadSHA: "aaaa"}
	for i := 0; i < MaxCancelReruns; i++ {
		g, err := ReserveCancelReruns(root, "o/r", pr, []string{"run:1"})
		if err != nil || !g[0] {
			t.Fatalf("rerun %d: %v %v", i, g, err)
		}
	}
	if CancelRerunsExhausted(root, "o/r", vcs.PR{Number: 7, HeadSHA: "aaaa"}) != true {
		t.Fatal("expected exhausted")
	}
	g, err := ReserveCancelReruns(root, "o/r", pr, []string{"run:1", "run:2"})
	if err != nil || g[0] || !g[1] {
		t.Fatalf("limit is per check: %v %v", g, err)
	}
	if CancelRerunsExhausted(root, "o/r", vcs.PR{Number: 7, HeadSHA: "bbbb"}) {
		t.Fatal("new head must reset")
	}
	g, _ = ReserveCancelReruns(root, "o/r", vcs.PR{Number: 7, HeadSHA: "bbbb"}, []string{"run:1"})
	if !g[0] {
		t.Fatal("new head should rerun")
	}
	_, path := Path(root, "o/r", 7)
	l, err := Load(path)
	if err != nil || len(l.Attempts) != 0 {
		t.Fatalf("repair ledger changed: %+v %v", l, err)
	}
	if err := CheckBudget(root, "o/r", 7, time.Now()); err != nil {
		t.Fatal(err)
	}
}
