package upgrade

import (
	"context"
	"errors"
	"fmt"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
)

// Worker step states.
const (
	WorkerPending   = "pending"
	WorkerDraining  = "draining"
	WorkerRolled    = "rolled"
	WorkerReady     = "ready"
	WorkerPostponed = "postponed"
	WorkerFailed    = "failed"
)

// runWorkers promotes workers strictly one at a time. For each worker: stop
// new assignments, wait for its leases and unsettled results to finish, start
// the target manager generation, require a fresh registration, then resume.
// Other ready workers keep serving throughout; if replacing a worker would
// drop capacity below MinCapacity the step is postponed instead.
func (e *Engine) runWorkers(ctx context.Context, rec *Record) error {
	if !rec.stageDone(StagePreflight) {
		err := e.step(rec, StagePreflight, 0, func(*StageRecord) (string, error) { return e.workerPreflight(ctx, rec) })
		if err != nil {
			if errors.Is(err, ErrInterrupted) {
				return err
			}
			return e.finish(rec, ResultFailed, "worker promotion not started: "+err.Error(), "Nothing changed. Fix the cause and run it again.")
		}
	}
	for i := range rec.Workers {
		w := &rec.Workers[i]
		if w.Status == WorkerReady {
			continue
		}
		if w.Status == WorkerPostponed || w.Status == WorkerFailed {
			// A resumed record retries postponed workers.
			w.Status = WorkerPending
		}
		if err := e.promoteWorker(ctx, rec, w); err != nil {
			if errors.Is(err, ErrInterrupted) {
				return err
			}
			cctx := context.WithoutCancel(ctx)
			// Before the new generation starts, the old one is intact and can
			// take work again. After it starts and fails readiness, the worker
			// stays paused: it has not shown it can safely accept assignments.
			var restoreErr error
			if w.RolledAt == nil {
				restoreErr = e.restoreWorker(cctx, rec, w)
			}
			var pp *postponeError
			result := ResultFailed
			if errors.As(err, &pp) {
				result, w.Status = ResultPostponed, WorkerPostponed
			} else {
				w.Status = WorkerFailed
			}
			w.Detail = err.Error()
			note := fmt.Sprintf("worker %s not promoted: %v. Workers promoted so far stay on the target; remaining workers are untouched. No task was cancelled or replayed.", w.Name, err)
			recovery := "Run the upgrade again to continue with the remaining workers. A worker already on the target is skipped."
			if restoreErr != nil {
				note += " Could not resume " + w.Name + ": " + restoreErr.Error()
			}
			if w.RolledAt != nil {
				recovery = "Worker " + w.Name + " stays paused because its new generation did not become ready. Check its manager.log, then `clawflow dashboard resume " + w.Name + "`."
			}
			return e.finish(rec, result, note, recovery)
		}
	}
	return e.finish(rec, ResultSucceeded, fmt.Sprintf("%d worker(s) promoted one at a time", len(rec.Workers)), "")
}

func (e *Engine) workerPreflight(ctx context.Context, rec *Record) (string, error) {
	if _, err := e.Host.Describe(ctx, rec.Target); err != nil {
		return "", fmt.Errorf("target manifest: %w", err)
	}
	if rec.Target.Manifest.WorkerProtocol != WorkerProtocol {
		return "", fmt.Errorf("target worker protocol %d is not supported here", rec.Target.Manifest.WorkerProtocol)
	}
	if err := e.Host.Probe(ctx); err != nil {
		return "", fmt.Errorf("controller API is not healthy: %w", err)
	}
	snap, err := e.Host.Snapshot(ctx, nil)
	if err != nil {
		return "", err
	}
	known := map[string]bool{}
	for _, w := range snap.Workers {
		known[w.Name] = true
	}
	for _, w := range rec.Workers {
		if !known[w.Name] {
			return "", fmt.Errorf("worker %q is not registered", w.Name)
		}
		if w.Descriptor == "" {
			return "", fmt.Errorf("worker %q has no generation descriptor", w.Name)
		}
	}
	return fmt.Sprintf("%d worker(s) planned", len(rec.Workers)), nil
}

func findWorker(snap coordinator.UpgradeSnapshot, name string) (coordinator.UpgradeWorker, bool) {
	for _, w := range snap.Workers {
		if w.Name == name {
			return w, true
		}
	}
	return coordinator.UpgradeWorker{}, false
}

// otherCapacity is the parallel capacity of ready workers excluding name.
func otherCapacity(snap coordinator.UpgradeSnapshot, name string) int {
	total := 0
	for _, w := range snap.Workers {
		if w.Name != name && w.Live && !w.Paused && !w.Revoked {
			total += w.MaxParallel
		}
	}
	return total
}

func (e *Engine) promoteWorker(ctx context.Context, rec *Record, w *WorkerStep) error {
	name := "worker:" + w.Name
	// Draining: pause (write-ahead), then wait for the leases to settle.
	if err := e.step(rec, name+":drain", e.Opt.WorkerTimeout, func(st *StageRecord) (string, error) {
		snap, err := e.Host.Snapshot(ctx, nil)
		if err != nil {
			return "", err
		}
		cur, ok := findWorker(snap, w.Name)
		if !ok || cur.Revoked {
			return "", fmt.Errorf("worker is not registered or was revoked")
		}
		if !cur.Paused && w.Status != WorkerDraining {
			if have := otherCapacity(snap, w.Name); have < e.Opt.MinCapacity {
				return "", &postponeError{reason: fmt.Sprintf("replacing %s would leave %d parallel slot(s) of ready capacity, below the required %d", w.Name, have, e.Opt.MinCapacity)}
			}
		}
		if w.Status != WorkerDraining {
			w.WasPaused = cur.Paused
		}
		w.Status = WorkerDraining
		if !w.WasPaused && !contains(rec.Quiesced, w.Name) {
			rec.Quiesced = append(rec.Quiesced, w.Name)
		}
		if err := e.save(rec); err != nil {
			return "", err
		}
		if !cur.Paused {
			if err := e.Host.SetPaused(ctx, w.Name, true); err != nil {
				return "", fmt.Errorf("stop new assignments: %w", err)
			}
		}
		deadline := e.Opt.Now().Add(e.Opt.WorkerTimeout)
		if st.Deadline != nil {
			deadline = *st.Deadline
		}
		w.Deadline = &deadline
		ok, detail, err := e.waitUntil(ctx, deadline, func() (bool, string) {
			s, err := e.Host.Snapshot(ctx, nil)
			if err != nil {
				return false, err.Error()
			}
			n := 0
			for _, l := range s.Leases {
				if l.Owner == w.Name {
					n++
				}
			}
			return n == 0, fmt.Sprintf("%d lease(s) or unsettled result(s) still owned by %s", n, w.Name)
		})
		if err != nil {
			return "", err
		}
		if !ok {
			return "", &postponeError{reason: "drain deadline expired: " + detail + "; the leases were left running"}
		}
		return "no leases or unsettled results remain", nil
	}); err != nil {
		return err
	}
	if err := e.step(rec, name+":roll", 0, func(*StageRecord) (string, error) {
		if err := e.Host.RollWorker(ctx, w.Descriptor, rec.Target); err != nil {
			return "", fmt.Errorf("start target generation: %w", err)
		}
		now := e.Opt.Now().UTC()
		w.Status, w.RolledAt = WorkerRolled, &now
		return "target manager generation started; existing executors keep their pinned binary", nil
	}); err != nil {
		return err
	}
	if err := e.step(rec, name+":ready", e.Opt.VerifyTimeout, func(st *StageRecord) (string, error) {
		rolled := w.RolledAt
		if rolled == nil {
			return "", fmt.Errorf("worker generation start time is missing")
		}
		deadline := e.Opt.Now().Add(e.Opt.VerifyTimeout)
		if st.Deadline != nil {
			deadline = *st.Deadline
		}
		ok, detail, err := e.waitUntil(ctx, deadline, func() (bool, string) {
			s, err := e.Host.Snapshot(ctx, nil)
			if err != nil {
				return false, err.Error()
			}
			cur, found := findWorker(s, w.Name)
			if !found || cur.LastSeen == nil || !cur.LastSeen.After(*rolled) {
				return false, "the new generation has not registered since it started"
			}
			return cur.Live, "worker is not live"
		})
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("readiness probe failed: %s", detail)
		}
		return "new generation registered and live", nil
	}); err != nil {
		return err
	}
	if err := e.step(rec, name+":resume", 0, func(*StageRecord) (string, error) {
		if err := e.restoreWorker(ctx, rec, w); err != nil {
			return "", err
		}
		w.Status = WorkerReady
		return "assignments resumed", nil
	}); err != nil {
		return err
	}
	return nil
}

// restoreWorker resumes w only when this upgrade paused it.
func (e *Engine) restoreWorker(ctx context.Context, rec *Record, w *WorkerStep) error {
	if w.WasPaused || !contains(rec.Quiesced, w.Name) {
		return nil
	}
	if err := e.Host.SetPaused(ctx, w.Name, false); err != nil {
		return err
	}
	out := rec.Quiesced[:0]
	for _, n := range rec.Quiesced {
		if n != w.Name {
			out = append(out, n)
		}
	}
	rec.Quiesced = out
	return e.save(rec)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
