// Package agentcapacity shares agent slots across all local ClawFlow processes.
package agentcapacity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

const DefaultTotal = 4

func Total() (int, error) {
	cfg, err := config.Load()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if cfg != nil && cfg.Settings.MaxConcurrentAgents > 0 {
		return cfg.Settings.MaxConcurrentAgents, nil
	}
	return DefaultTotal, nil
}

func slotPath(slot int) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "locks", "agents", fmt.Sprintf("slot-%d.lock", slot))
}

// Acquire waits cancellably for any slot. Persistent inodes are never removed;
// the kernel releases their locks on process death. Reload the limit on every
// scan so waiting processes observe configuration changes.
func Acquire(ctx context.Context) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		total, err := Total()
		if err != nil {
			return nil, err
		}
		for slot := 0; slot < total; slot++ {
			f, err := filelock.Try(slotPath(slot), map[string]any{"pid": os.Getpid()})
			if errors.Is(err, filelock.ErrHeld) {
				continue
			}
			if err != nil {
				return nil, err
			}
			var once sync.Once
			release := func() { once.Do(func() { filelock.Release(f) }) }
			if err := ctx.Err(); err != nil {
				release()
				return nil, err
			}
			return release, nil
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// Used probes actual kernel ownership, including slots draining after a limit
// reduction. Counts are a point-in-time observation, not a reservation.
func Used() int {
	paths, _ := filepath.Glob(filepath.Join(filepath.Dir(slotPath(0)), "slot-*.lock"))
	used := 0
	for _, path := range paths {
		if filelock.Held(path) {
			used++
		}
	}
	return used
}
