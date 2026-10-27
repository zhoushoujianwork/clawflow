package commands

import (
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func TestDueScheduledRepos(t *testing.T) {
	cfg := &config.Config{Repos: map[string]config.Repo{
		"o/a":   {Enabled: true},
		"o/b":   {Enabled: true},
		"o/off": {Enabled: false},
		"o/els": {Enabled: true, BoundMachine: "elsewhere"},
	}}
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	last := map[string]time.Time{}
	interval := 5 * time.Minute

	due, next := dueScheduledRepos(cfg, "h", last, t0, interval)
	if len(due) != 0 || !next.Equal(t0.Add(interval)) {
		t.Fatalf("first tick must not fire: due=%v next=%v", due, next)
	}
	if _, ok := last["o/off"]; ok {
		t.Fatal("disabled repo scheduled")
	}
	if _, ok := last["o/els"]; ok {
		t.Fatal("repo bound elsewhere scheduled")
	}

	// A fires earlier than B; only A is due, independent of B.
	last["o/a"] = t0.Add(-4 * time.Minute)
	due, next = dueScheduledRepos(cfg, "h", last, t0.Add(2*time.Minute), interval)
	if !due["o/a"] || due["o/b"] || len(due) != 1 {
		t.Fatalf("due = %v", due)
	}
	if !next.Equal(t0.Add(time.Minute)) {
		t.Fatalf("next = %v", next)
	}

	// A repo that leaves the schedule is forgotten.
	cfg.Repos["o/b"] = config.Repo{Enabled: false}
	dueScheduledRepos(cfg, "h", last, t0, interval)
	if _, ok := last["o/b"]; ok {
		t.Fatal("stale lastFire kept")
	}

	// Nothing schedulable: zero next fire hides the countdown.
	if _, next := dueScheduledRepos(&config.Config{}, "h", map[string]time.Time{}, t0, interval); !next.IsZero() {
		t.Fatalf("next = %v", next)
	}
}
