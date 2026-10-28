package commands

import (
	"context"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

func TestPilotTickIndependentAndSerialized(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	old := schedulePilotTick
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	done := make(chan struct{})
	schedulePilotTick = func(context.Context, time.Duration, time.Time) (int, error) {
		entered <- struct{}{}
		<-release
		close(done)
		return 0, nil
	}
	t.Cleanup(func() { schedulePilotTick = old })
	cfg := &config.Config{}
	cfg.Settings.RunPaused = true
	triggerPilotTick(cfg)
	select {
	case <-entered:
		t.Fatal("paused tick launched")
	default:
	}
	cfg.Settings.RunPaused = false
	// Simulate another pass scheduling Pilot: the tick must yield immediately.
	if !snapshot.TryAcquirePilotScheduleLock() {
		t.Fatal("lock unavailable")
	}
	triggerPilotTick(cfg)
	select {
	case <-entered:
		t.Fatal("overlapping scheduling")
	default:
	}
	snapshot.ReleasePilotScheduleLock()
	// A live repository pass must not prevent supervision.
	if err := snapshot.AcquireRepoRunLock("o/r", "test"); err != nil {
		t.Fatal(err)
	}
	defer snapshot.ReleaseRepoRunLock("o/r")
	// No due repository or enabled run interval is required for supervision.
	triggerPilotTick(cfg)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("tick did not launch")
	}
	triggerPilotTick(cfg)
	select {
	case <-entered:
		t.Fatal("duplicate tick launched")
	default:
	}
	close(release)
	<-done
	// Wait for the goroutine's deferred unlock before test HOME is restored.
	deadline := time.Now().Add(time.Second)
	for !snapshot.TryAcquirePilotScheduleLock() {
		if time.Now().After(deadline) {
			t.Fatal("lock not released")
		}
		time.Sleep(time.Millisecond)
	}
	snapshot.ReleasePilotScheduleLock()
}
