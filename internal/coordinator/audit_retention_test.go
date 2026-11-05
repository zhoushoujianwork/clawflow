package coordinator

import (
	"testing"
	"time"
)

func seedAudit(t *testing.T, s *Store, worker, action string, age time.Duration) {
	t.Helper()
	if err := s.db.Create(&Audit{Worker: worker, Action: action, CreatedAt: time.Now().UTC().Add(-age)}).Error; err != nil {
		t.Fatal(err)
	}
}

func countAudit(t *testing.T, s *Store, action string) int64 {
	t.Helper()
	var n int64
	if err := s.db.Model(&Audit{}).Where("action = ?", action).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPurgeAuditRetention(t *testing.T) {
	s, _, _ := setup(t)
	day := 24 * time.Hour
	seedAudit(t, s, "w", "http:claim:200", 40*day)
	seedAudit(t, s, "w", "http:claim:200", 1*day)
	seedAudit(t, s, "w", "heartbeat", 40*day)
	seedAudit(t, s, "w", "heartbeat", 39*day) // newest heartbeat, must survive
	seedAudit(t, s, "w", "cancel", 40*day)
	seedAudit(t, s, "w", "token-revoke", 40*day)
	seedAudit(t, s, "w", "http:claim:401", 40*day)
	seedAudit(t, s, "w", "token-create", 200*day)
	before, err := s.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.PurgeAudit(ctx, time.Now(), AuditRetention{Batch: 2, Pause: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 { // old claim, old heartbeat, expired token-create
		t.Fatalf("deleted %d", n)
	}
	if countAudit(t, s, "http:claim:200") != 1 || countAudit(t, s, "heartbeat") != 1 {
		t.Fatal("routine retention wrong")
	}
	for _, a := range []string{"cancel", "token-revoke", "http:claim:401"} {
		if countAudit(t, s, a) != 1 {
			t.Fatal("security row purged", a)
		}
	}
	after, err := s.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Workers) != len(after.Workers) {
		t.Fatal("board changed")
	}
	var last struct{ At string }
	_ = last
}

func TestPurgeAuditBatches(t *testing.T) {
	s, _, _ := setup(t)
	for i := 0; i < 25; i++ {
		seedAudit(t, s, "w", "token-verify", 40*24*time.Hour)
	}
	var batches []int64
	n, err := s.PurgeAudit(ctx, time.Now(), AuditRetention{Batch: 10, Pause: time.Millisecond, afterBatch: func(d int64) { batches = append(batches, d) }})
	if err != nil || n != 25 {
		t.Fatal(n, err)
	}
	if len(batches) != 3 || batches[0] != 10 || batches[2] != 5 {
		t.Fatal(batches)
	}
	// A concurrent writer is not blocked for the whole purge: it can commit between batches.
	for i := 0; i < 50; i++ {
		seedAudit(t, s, "w", "token-verify", 40*24*time.Hour)
	}
	wrote := 0
	_, err = s.PurgeAudit(ctx, time.Now(), AuditRetention{Batch: 10, Pause: time.Millisecond, afterBatch: func(int64) {
		start := time.Now()
		if err := s.AppendAudit(ctx, "other", "", "register"); err != nil {
			t.Error(err)
		}
		if time.Since(start) > time.Second {
			t.Error("writer blocked")
		}
		wrote++
	}})
	if err != nil || wrote != 5 {
		t.Fatal(wrote, err)
	}
}

func TestAuditSamplingReducesIdlePollingRows(t *testing.T) {
	s, _, _ := setup(t)
	// One hour of idle polling every 2 seconds: one verify and one empty claim per poll.
	const polls = 1800
	unsampled := int64(polls * 2)
	var sampler auditSampler
	base := time.Now()
	var kept int64
	for i := 0; i < polls; i++ {
		now := base.Add(time.Duration(i) * 2 * time.Second)
		for _, a := range []string{"token-verify", "http:claim:200"} {
			if sampler.allow("w", a, DefaultAuditSampleWindow, now) {
				kept++
			}
		}
	}
	t.Logf("audit rows per idle hour: before=%d after=%d", unsampled, kept)
	if kept > 12 || kept >= unsampled/50 {
		t.Fatal(kept)
	}
	// Store path: repeated calls within the window write once; other actions are never sampled.
	for i := 0; i < 5; i++ {
		_ = s.AppendAudit(ctx, "w", "", "http:claim:200")
		_ = s.AppendAudit(ctx, "w", "", "http:claim:401")
	}
	if countAudit(t, s, "http:claim:200") != 1 || countAudit(t, s, "http:claim:401") != 5 {
		t.Fatal("store sampling wrong")
	}
}

func TestPurgeAuditFreesPagesForReuse(t *testing.T) {
	s, _, _ := setup(t)
	for i := 0; i < 2000; i++ {
		seedAudit(t, s, "w", "token-verify", 40*24*time.Hour)
	}
	var free0, free1 int64
	s.db.Raw("PRAGMA freelist_count").Scan(&free0)
	if _, err := s.PurgeAudit(ctx, time.Now(), AuditRetention{Pause: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	s.db.Raw("PRAGMA freelist_count").Scan(&free1)
	if free1 <= free0 {
		t.Fatal("no free pages after purge", free0, free1)
	}
}
