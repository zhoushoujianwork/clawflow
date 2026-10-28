package snapshot

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/store"
)

// BindRunStore is called at command startup, before any writers or readers run.
// File-only unit fixtures can leave it unset; stateful CLI commands must bind it.
var runStore atomic.Pointer[store.Store]

func BindRunStore(s *store.Store) { runStore.Store(s) }
func RunStore() *store.Store      { return runStore.Load() }

func persistRun(dir, kind string, meta any) error {
	if err := writeJSON(filepath.Join(dir, "meta.json"), meta); err != nil {
		return err
	}
	if s := RunStore(); s != nil {
		raw, err := json.Marshal(meta)
		if err != nil {
			return err
		}
		// Final writes must survive cancellation of the execution context.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, err = s.UpsertRun(ctx, dir, kind, raw)
		return err
	}
	return nil
}

func databaseRuns(limit int) ([]RunIndexEntry, error) {
	rows, err := RunStore().Runs(context.Background(), "run", store.RunFilter{Limit: limit})
	if err != nil {
		return nil, err
	}
	payload, err := store.RunPayloads(rows, DataDir())
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	entries := []RunIndexEntry{}
	err = json.Unmarshal(b, &entries)
	return entries, err
}
func databasePilotRuns(limit int) ([]PilotRunIndexEntry, error) {
	rows, err := RunStore().Runs(context.Background(), "pilot", store.RunFilter{Limit: limit})
	if err != nil {
		return nil, err
	}
	payload, err := store.RunPayloads(rows, DataDir())
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	entries := []PilotRunIndexEntry{}
	err = json.Unmarshal(b, &entries)
	return entries, err
}
