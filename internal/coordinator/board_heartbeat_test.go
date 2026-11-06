package coordinator

import (
	"strings"
	"testing"
	"time"
)

func TestBoardLastHeartbeatPerWorker(t *testing.T) {
	s, _, _ := setup(t)
	for _, id := range []string{"hb-a", "hb-b", "hb-none"} {
		if err := s.db.Create(&Worker{ID: id, Tags: []string{}, Roles: []string{}, MaxParallel: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)
	rows := []Audit{
		{Worker: "hb-a", Action: "heartbeat", CreatedAt: now.Add(-10 * time.Minute)},
		{Worker: "hb-a", Action: "heartbeat", CreatedAt: now.Add(-2 * time.Minute)},
		{Worker: "hb-a", Action: "claim", CreatedAt: now},
		{Worker: "hb-b", Action: "heartbeat", CreatedAt: now.Add(-5 * time.Minute)},
		{Worker: "hb-none", Action: "claim", CreatedAt: now},
	}
	if err := s.db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	b, err := s.Board(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*time.Time{}
	for _, w := range b.Workers {
		got[w.Name] = w.LastHeartbeat
	}
	if got["hb-a"] == nil || !got["hb-a"].Equal(now.Add(-2*time.Minute)) {
		t.Fatalf("hb-a = %v", got["hb-a"])
	}
	if got["hb-b"] == nil || !got["hb-b"].Equal(now.Add(-5*time.Minute)) {
		t.Fatalf("hb-b = %v", got["hb-b"])
	}
	if got["hb-none"] != nil {
		t.Fatalf("hb-none = %v", got["hb-none"])
	}
}

func TestBoardHeartbeatQueryUsesIndex(t *testing.T) {
	s, _, _ := setup(t)
	var plan []struct {
		ID, Parent, Notused int
		Detail              string
	}
	q := "EXPLAIN QUERY PLAN SELECT w.id AS id, (SELECT MAX(a.created_at) FROM coordinator_audit a WHERE a.worker = w.id AND a.action = 'heartbeat') AS at FROM coordinator_workers w"
	if err := s.db.Raw(q).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range plan {
		if strings.Contains(p.Detail, "coordinator_audit") {
			if strings.HasPrefix(p.Detail, "SCAN") {
				t.Fatalf("full scan: %s", p.Detail)
			}
			if strings.Contains(p.Detail, "SEARCH") && strings.Contains(p.Detail, "coordinator_audit_heartbeat") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no SEARCH on coordinator_audit_heartbeat: %+v", plan)
	}
}

func TestParseHeartbeatTime(t *testing.T) {
	if _, ok, err := parseHeartbeatTime(nil); ok || err != nil {
		t.Fatal(ok, err)
	}
	want := time.Date(2026, 1, 2, 3, 4, 5, 600000000, time.UTC)
	for _, in := range []any{want, "2026-01-02 03:04:05.6+00:00", "2026-01-02T03:04:05.6Z", "2026-01-02 03:04:05.6 +0000 UTC m=+1.5"} {
		got, ok, err := parseHeartbeatTime(in)
		if err != nil || !ok || !got.Equal(want) {
			t.Fatalf("%v: %v %v %v", in, got, ok, err)
		}
	}
}
