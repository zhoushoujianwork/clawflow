package store_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/store"
)

func checkFixture(id string) store.ScheduledCheck {
	return store.ScheduledCheck{ID: id, Cron: "* * * * *", Timezone: "Asia/Shanghai", Prompt: "summarize merges", Ops: 2, TimeoutSeconds: 300, Owner: "test", Enabled: true}
}
func TestScheduledChecksLifecycle(t *testing.T) {
	s := historyStore(t)
	ctx := context.Background()
	j := checkFixture("daily")
	if err := s.SaveScheduledCheck(ctx, &j, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	runs, err := s.ClaimScheduledChecks(ctx, now)
	if err != nil || len(runs) != 1 {
		t.Fatalf("%+v %v", runs, err)
	}
	same, err := s.ClaimScheduledChecks(ctx, now.Add(30*time.Second))
	if err != nil || len(same) != 0 {
		t.Fatalf("duplicate: %+v %v", same, err)
	}
	overlap, err := s.ClaimScheduledChecks(ctx, now.Add(time.Minute))
	if err != nil || len(overlap) != 0 {
		t.Fatalf("overlap: %+v %v", overlap, err)
	}
	rows, _ := s.ScheduledCheckRuns(ctx, j.ID)
	if len(rows) != 2 || rows[0].Status != "skipped" {
		t.Fatalf("rows %+v", rows)
	}
	actions := json.RawMessage(`[{"outcome":"applied"}]`)
	usage := json.RawMessage(`{"input_tokens":10}`)
	if err := s.FinishScheduledCheck(ctx, runs[0].ID, "success", "done", actions, usage); err != nil {
		t.Fatal(err)
	}
	// A long downtime admits exactly the current slot, never the intervening slots.
	after, err := s.ClaimScheduledChecks(ctx, now.Add(24*time.Hour))
	if err != nil || len(after) != 1 {
		t.Fatalf("replay: %+v %v", after, err)
	}
	rows, _ = s.ScheduledCheckRuns(ctx, j.ID)
	if len(rows) != 3 {
		t.Fatalf("replayed %d runs", len(rows))
	}
	for _, r := range rows {
		if r.ID == runs[0].ID && (string(r.Actions) != `[{"outcome": "applied"}]` || len(r.Usage) == 0) {
			t.Fatalf("result not recorded %+v", r)
		}
	}
	for i := 0; i < 3; i++ {
		if i > 0 {
			after, err = s.ClaimScheduledChecks(ctx, now.Add(24*time.Hour+time.Duration(i)*time.Minute))
			if err != nil || len(after) != 1 {
				t.Fatalf("%+v %v", after, err)
			}
		}
		if err := s.FinishScheduledCheck(ctx, after[0].ID, "failed", "failure", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := s.ScheduledCheck(ctx, j.ID)
	if err != nil || saved.Enabled || saved.Failures != 3 {
		t.Fatalf("%+v %v", saved, err)
	}
	saved.Enabled = true
	if err := s.SaveScheduledCheck(ctx, &saved, false); err != nil {
		t.Fatal(err)
	}
	saved, _ = s.ScheduledCheck(ctx, j.ID)
	if !saved.Enabled || saved.Failures != 0 {
		t.Fatal(saved)
	}
	if err := s.RemoveScheduledCheck(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ScheduledCheckRuns(ctx, j.ID)
	if len(rows) == 0 {
		t.Fatal("deleted history")
	}
}
func TestScheduledCheckConcurrentClaimAndCrash(t *testing.T) {
	s := historyStore(t)
	ctx := context.Background()
	j := checkFixture("concurrent")
	j.TimeoutSeconds = 30
	if err := s.SaveScheduledCheck(ctx, &j, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	var wg sync.WaitGroup
	counts := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runs, err := s.ClaimScheduledChecks(ctx, now)
			if err != nil {
				t.Error(err)
			}
			counts <- len(runs)
		}()
	}
	wg.Wait()
	if n := <-counts + <-counts; n != 1 {
		t.Fatalf("claims=%d", n)
	}
	for i := 1; i <= 3; i++ {
		runs, err := s.ClaimScheduledChecks(ctx, now.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if i == 3 {
			want = 0
		}
		if len(runs) != want {
			t.Fatal(runs)
		}
	}
	saved, _ := s.ScheduledCheck(ctx, j.ID)
	if saved.Enabled || saved.Failures != 3 {
		t.Fatal(saved)
	}
}
