package coordinator

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func hasRule(b Board, rule string) bool {
	for _, f := range b.Findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

func orphanFixture(now time.Time) []OrphanedPR {
	return []OrphanedPR{{Repo: "o/r", PR: 9, Cause: OrphanIssueUnlinked, Since: now.Add(-time.Hour)}}
}

// A blocked refresh must not delay board reads or hide other rules.
func TestBoardDoesNotWaitForBlockedOrphanRefresh(t *testing.T) {
	s, db, _ := setup(t)
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		task := Task{ID: "fail-" + string(rune('a'+i)), Kind: "implement", Repo: "o/r", Issue: 7, Operator: "implement", Payload: []byte(`{}`), Tags: []string{},
			State: "failed", Result: []byte(`{"status":"failed","error":"operator produced no outcome marker"}`), CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Duration(i+1) * time.Minute)}
		if err := db.DB().Create(&task).Error; err != nil {
			t.Fatal(err)
		}
	}
	release := make(chan struct{})
	started := make(chan struct{})
	s.OrphanedPRs = func(ctx context.Context) ([]OrphanedPR, error) {
		close(started)
		<-release
		return nil, nil
	}
	defer close(release)
	go s.RefreshOrphanedPRs(ctx)
	<-started
	done := make(chan Board, 1)
	go func() {
		b, err := s.Board(ctx)
		if err != nil {
			t.Error(err)
		}
		done <- b
	}()
	select {
	case b := <-done:
		if !hasRule(b, RuleRepeatedFailure) {
			t.Fatalf("other rules missing: %+v", b.Findings)
		}
		if hasRule(b, RuleOrphanedPR) {
			t.Fatalf("no result yet, orphaned_pr must be absent: %+v", b.Findings)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Board waited on the orphaned PR refresh")
	}
}

func TestOrphanRefreshFailureKeepsPreviousResult(t *testing.T) {
	s, _, _ := setup(t)
	now := time.Now().UTC()
	var fail atomic.Bool
	s.OrphanedPRs = func(context.Context) ([]OrphanedPR, error) {
		if fail.Load() {
			return nil, errors.New("provider down")
		}
		return orphanFixture(now), nil
	}
	s.RefreshOrphanedPRs(ctx)
	fail.Store(true)
	s.RefreshOrphanedPRs(ctx)
	b, err := s.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRule(b, RuleOrphanedPR) {
		t.Fatalf("previous result lost: %+v", b.Findings)
	}
}

func TestBoardReadsDoNotTriggerRefresh(t *testing.T) {
	s, _, _ := setup(t)
	var calls atomic.Int32
	s.OrphanedPRs = func(context.Context) ([]OrphanedPR, error) {
		calls.Add(1)
		return nil, nil
	}
	s.RefreshOrphanedPRs(ctx)
	for i := 0; i < 5; i++ {
		if _, err := s.Board(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("refresh calls = %d, want 1", n)
	}
}

// The refresh uses its own timeout and the board's context expiry cannot break the transaction path.
func TestOrphanRefreshHasOwnTimeoutAndBoardSurvivesContextTimeout(t *testing.T) {
	s, _, _ := setup(t)
	s.OrphanRefreshTimeout = 50 * time.Millisecond
	s.OrphanedPRs = func(c context.Context) ([]OrphanedPR, error) {
		<-c.Done()
		return nil, c.Err()
	}
	start := time.Now()
	s.RefreshOrphanedPRs(ctx)
	if time.Since(start) > 5*time.Second {
		t.Fatal("refresh ignored its own timeout")
	}
	// A short board context only affects database calls; no VCS call is made inside the transaction.
	bctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := s.Board(bctx); err != nil {
		t.Fatalf("board: %v", err)
	}
}

func TestOrphanedPRFindingsKeepFirstObservedSince(t *testing.T) {
	now := time.Now().UTC()
	since := now.Add(-90 * time.Minute)
	got := orphanedPRFindings(now, []OrphanedPR{{Repo: "o/r", PR: 1, Cause: OrphanIssueUnlinked, Since: since}}, nil, WatchdogConfig{OrphanAfter: time.Hour})
	if len(got) != 1 || !got[0].Since.Equal(since) {
		t.Fatalf("findings: %+v", got)
	}
}
