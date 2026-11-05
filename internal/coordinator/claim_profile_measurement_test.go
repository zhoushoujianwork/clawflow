package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	storepkg "github.com/zhoushoujianwork/clawflow/internal/store"
)

func TestProfileClaimComponents(t *testing.T) {
	if os.Getenv("CLAWFLOW_PROFILE_SCALE") != "1" {
		t.Skip("set CLAWFLOW_PROFILE_SCALE=1 to profile claim components")
	}
	ctx := context.Background()
	db, err := storepkg.Open(ctx, filepath.Join(t.TempDir(), "profile.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(ctx, storepkg.Migrations); err != nil {
		t.Fatal(err)
	}
	s := New(db.DB())
	var secret string
	for i := 0; i < 50; i++ {
		v, err := s.CreateToken(ctx, fmt.Sprintf("profile-%02d", i))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			secret = v
		}
	}
	if err := s.RegisterWorker(ctx, Worker{ID: "profile-00", MaxParallel: 100, RunUntagged: true, Roles: []string{"operator"}}); err != nil {
		t.Fatal(err)
	}
	limits := map[string]int{}
	for i := 0; i < 100; i++ {
		repo := fmt.Sprintf("synthetic/project-%03d", i)
		limits[repo] = 5
		if _, err := s.Enqueue(ctx, Task{Kind: "implement", Repo: repo, Issue: i + 1, Operator: "implement", Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	s.LeaseLimits = func() (int, map[string]int, error) { return 100, limits, nil }

	verify := profileDurations(t, 100, func() error { _, err := s.VerifyToken(ctx, secret); return err })
	auditOnly := profileDurations(t, 100, func() error { return s.AppendAudit(ctx, "profile-00", "", "profile") })
	selectPending := profileDurations(t, 100, func() error {
		var id string
		return db.DB().Raw("SELECT id FROM coordinator_tasks WHERE state = 'pending' ORDER BY priority ASC, queue_position ASC, created_at ASC, id ASC LIMIT 1").Scan(&id).Error
	})
	capacity := profileDurations(t, 100, func() error {
		var n int64
		if err := db.DB().Model(&Task{}).Where("state = 'leased'").Count(&n).Error; err != nil {
			return err
		}
		if err := db.DB().Model(&Task{}).Where("state = 'leased' AND lease_owner = ?", "profile-00").Count(&n).Error; err != nil {
			return err
		}
		return db.DB().Model(&Task{}).Where("repo = ? AND state = 'leased'", "synthetic/project-000").Count(&n).Error
	})
	claim := profileDurations(t, 100, func() error { _, err := s.Claim(ctx, "profile-00"); return err })
	t.Logf("PROFILE store=sqlite tokens=50 samples=100 verify_p50=%s verify_p95=%s audit_p50=%s audit_p95=%s select_p50=%s select_p95=%s capacity_p50=%s capacity_p95=%s claim_p50=%s claim_p95=%s",
		profilePct(verify, .50), profilePct(verify, .95), profilePct(auditOnly, .50), profilePct(auditOnly, .95),
		profilePct(selectPending, .50), profilePct(selectPending, .95), profilePct(capacity, .50), profilePct(capacity, .95), profilePct(claim, .50), profilePct(claim, .95))
}

func TestMeasureLeaseRecoveryTTL(t *testing.T) {
	if os.Getenv("CLAWFLOW_MEASURE_RECOVERY") != "1" {
		t.Skip("set CLAWFLOW_MEASURE_RECOVERY=1 to measure default lease recovery")
	}
	ctx := context.Background()
	db, err := storepkg.Open(ctx, filepath.Join(t.TempDir(), "recovery.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Migrate(ctx, storepkg.Migrations); err != nil {
		t.Fatal(err)
	}
	s := New(db.DB())
	for _, id := range []string{"lost", "replacement"} {
		if err := s.RegisterWorker(ctx, Worker{ID: id, MaxParallel: 1, RunUntagged: true, Roles: []string{"operator"}}); err != nil {
			t.Fatal(err)
		}
	}
	task, err := s.Enqueue(ctx, Task{Kind: "implement", Repo: "synthetic/recovery", Issue: 1, Operator: "implement", Payload: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.Claim(ctx, "lost")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if claimed.LeaseExpiresAt == nil {
		t.Fatal("claim has no lease expiry")
	}
	time.Sleep(time.Until(*claimed.LeaseExpiresAt) + 10*time.Millisecond)
	expired, err := s.ExpireLeases(ctx, time.Now())
	if err != nil || len(expired) != 1 {
		t.Fatalf("expire: tasks=%d err=%v", len(expired), err)
	}
	recovered, err := s.Claim(ctx, "replacement")
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ID != task.ID || recovered.FencingToken != claimed.FencingToken+1 || recovered.Attempt != 1 {
		t.Fatalf("unexpected recovery: %+v", recovered)
	}
	t.Logf("RECOVERY lease_ttl=%s expiry_to_reclaim=%s total=%s task_reused=true fence_advanced=true duplicate_execution=false",
		claimed.LeaseExpiresAt.Sub(claimed.UpdatedAt).Round(time.Millisecond), time.Since(*claimed.LeaseExpiresAt).Round(time.Millisecond), time.Since(started).Round(time.Millisecond))
}

func profileDurations(t *testing.T, n int, fn func() error) []time.Duration {
	t.Helper()
	out := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		if err := fn(); err != nil {
			t.Fatal(err)
		}
		out = append(out, time.Since(start))
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func profilePct(v []time.Duration, p float64) time.Duration {
	return v[int(float64(len(v)-1)*p)].Round(time.Microsecond)
}
