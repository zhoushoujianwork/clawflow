package coordinator

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

const interruptedWithSession = `{"status":"failed","infra_kind":"execution-interrupted","error":"context deadline exceeded","provenance":{"session_id":"retained-native"}}`

// driveToLastAttempt reports and recovers interruptions until the task sits on
// its last recoverable attempt, then returns it with the lease still current.
func driveToLastAttempt(t *testing.T, s *Store, issue int) *Task {
	t.Helper()
	if _, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: issue, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
		t.Fatal(err)
	}
	task := claim(t, s, "w")
	raw := json.RawMessage(interruptedWithSession)
	for task.Attempt < MaxInfrastructureAttempts-1 {
		reportInterruption(t, s, task, raw)
		next, err := s.RecoverExecution(ctx, task.ID, "w", task.FencingToken)
		if err != nil {
			t.Fatalf("attempt %d not recoverable: %v", task.Attempt, err)
		}
		task = next
	}
	return task
}

func reportInterruption(t *testing.T, s *Store, task *Task, raw json.RawMessage) {
	t.Helper()
	if err := s.AcceptResult(WithReportDigest(ctx, raw, true), task.ID, "w", task.FencingToken, raw); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeApplication(ctx, task.ID, task.FencingToken, true, func(Task) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

// The reported case: the last allowed recovery was spent, the interruption was
// reported again, and the task used to stay leased forever while the worker got
// 409 on every recovery request. It must now fail and free both slots.
func TestExhaustedInterruptionFailsAndReleasesSlots(t *testing.T) {
	s, db, _ := setup(t)
	registerDurable(t, s, "w", "other")
	s.LeaseLimits = func() (int, map[string]int, error) { return 10, map[string]int{"o/r": 1}, nil }
	task := driveToLastAttempt(t, s, 787)
	before, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(interruptedWithSession)

	var announced []Task
	if err := s.AcceptResult(WithReportDigest(ctx, raw, true), task.ID, "w", task.FencingToken, raw); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeApplication(ctx, task.ID, task.FencingToken, true, func(c Task) error { announced = append(announced, c); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(announced) != 1 || !RecoveryExhausted(&announced[0]) {
		t.Fatalf("finalize callback did not see an exhausted task: %+v", announced)
	}

	got, err := s.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "failed" || got.LeaseExpiresAt != nil || got.ApplicationError != "" {
		t.Fatalf("task not terminal: %+v", got)
	}
	// Evidence and identity are untouched, so the worker's receipt still matches.
	if string(got.Result) == "" || got.Attempt != before.Attempt || got.FencingToken != task.FencingToken || got.LeaseOwner != "w" || !MatchingReport(got, "w", task.FencingToken, raw, true) {
		t.Fatalf("evidence changed: %+v", got)
	}
	if acked, err := s.ReportAcknowledged(ctx, task.ID, "w", task.FencingToken, raw, true); err != nil || !acked {
		t.Fatalf("report receipt lost: %v, %v", acked, err)
	}
	var audits int64
	if err := db.DB().Model(&Audit{}).Where("task = ? AND action = 'recovery-exhausted'", task.ID).Count(&audits).Error; err != nil || audits != 1 {
		t.Fatalf("audit rows = %d, %v", audits, err)
	}
	// Only the two earlier interruptions were retained; the last one was not.
	if err := db.DB().Model(&Audit{}).Where("task = ? AND action = 'interruption-retained'", task.ID).Count(&audits).Error; err != nil || audits != MaxInfrastructureAttempts-1 {
		t.Fatalf("retained audits = %d, %v", audits, err)
	}

	// Nothing may recover it, resurrect it, or restart finalization.
	if _, err := s.RecoverExecution(ctx, task.ID, "w", task.FencingToken); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("ended task was recovered: %v", err)
	}
	if err := s.Heartbeat(ctx, task.ID, "w", task.FencingToken); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("ended task kept authority: %v", err)
	}
	if pending, err := s.PendingResults(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("ended task is pending again: %v, %v", pending, err)
	}
	if active, err := s.Active(ctx); err != nil || len(active) != 0 {
		t.Fatalf("ended task is still active: %+v, %v", active, err)
	}
	if again, err := s.EndExhaustedInterruptions(ctx, nil); err != nil || len(again) != 0 {
		t.Fatalf("sweep touched an ended task: %v, %v", again, err)
	}

	// The worker slot and the repository slot are free, and no replacement
	// task exists for the issue.
	var tasks int64
	if err := db.DB().Model(&Task{}).Where("issue = 787").Count(&tasks).Error; err != nil || tasks != 1 {
		t.Fatalf("tasks for the issue = %d, %v", tasks, err)
	}
	if _, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 900, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
		t.Fatal(err)
	}
	if next, err := s.Claim(ctx, "w"); err != nil || next.Issue != 900 {
		t.Fatalf("worker slot not released: %+v, %v", next, err)
	}
}

// Before the budget is spent, behavior is unchanged: the interruption stays
// reserved, nothing else is claimed, and the next recovery is authorized.
func TestInterruptionBeforeBudgetStaysReserved(t *testing.T) {
	s, _, _ := setup(t)
	registerDurable(t, s, "w", "other")
	s.LeaseLimits = func() (int, map[string]int, error) { return 10, map[string]int{"o/r": 1}, nil }
	if _, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 787, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
		t.Fatal(err)
	}
	task := claim(t, s, "w")
	raw := json.RawMessage(interruptedWithSession)
	var seen Task
	if err := s.AcceptResult(WithReportDigest(ctx, raw, true), task.ID, "w", task.FencingToken, raw); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeApplication(ctx, task.ID, task.FencingToken, true, func(c Task) error { seen = c; return nil }); err != nil {
		t.Fatal(err)
	}
	if RecoveryExhausted(&seen) {
		t.Fatal("a task with recovery budget left was reported as exhausted")
	}
	got, err := s.Task(ctx, task.ID)
	if err != nil || got.State != "leased" || got.LeaseExpiresAt == nil {
		t.Fatalf("interruption was not retained: %+v, %v", got, err)
	}
	if _, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 900, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, "other"); !errors.Is(err, ErrNoTask) {
		t.Fatalf("slot was released early: %v", err)
	}
	if ended, err := s.EndExhaustedInterruptions(ctx, func(Task) error { t.Fatal("announced a recoverable task"); return nil }); err != nil || len(ended) != 0 {
		t.Fatalf("sweep ended a recoverable task: %+v, %v", ended, err)
	}
	if next, err := s.RecoverExecution(ctx, task.ID, "w", task.FencingToken); err != nil || next.Attempt != 1 || next.FencingToken != task.FencingToken+1 {
		t.Fatalf("recovery not authorized: %+v, %v", next, err)
	}
}

// A task an earlier controller left reserved after its budget ran out is ended
// by the sweep, announced first, and keeps all of its evidence.
func TestEndExhaustedInterruptionsSweepsStuckTask(t *testing.T) {
	for name, raw := range map[string]string{
		"interrupted":       interruptedWithSession,
		"blocked launch":    `{"status":"blocked","infra_kind":"launch","next_owner":"scheduling"}`,
		"blocked, not seen": `{"status":"failed","infra_kind":"execution-interrupted"}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, db, _ := setup(t)
			registerDurable(t, s, "w", "other")
			s.LeaseLimits = func() (int, map[string]int, error) { return 10, map[string]int{"o/r": 1}, nil }
			task := driveToLastAttempt(t, s, 787)
			report := json.RawMessage(raw)
			if err := s.AcceptResult(WithReportDigest(ctx, report, true), task.ID, "w", task.FencingToken, report); err != nil {
				t.Fatal(err)
			}
			// Reproduce the old finalization: the exhausted task stays leased.
			if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).Updates(map[string]any{"lease_expires_at": time.Now().Add(-time.Minute)}).Error; err != nil {
				t.Fatal(err)
			}
			before, _ := s.Task(ctx, task.ID)
			if _, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 900, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Claim(ctx, "other"); !errors.Is(err, ErrNoTask) {
				t.Fatalf("slot was not held before the sweep: %v", err)
			}

			var announced []Task
			ended, err := s.EndExhaustedInterruptions(ctx, func(c Task) error { announced = append(announced, c); return nil })
			if err != nil || len(ended) != 1 || ended[0].ID != task.ID || len(announced) != 1 {
				t.Fatalf("ended=%+v announced=%d err=%v", ended, len(announced), err)
			}
			got, err := s.Task(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != "failed" || got.LeaseExpiresAt != nil || string(got.Result) != string(before.Result) || got.ResultDigest != before.ResultDigest || got.FencingToken != before.FencingToken || got.Attempt != before.Attempt || got.LeaseOwner != "w" {
				t.Fatalf("evidence changed:\nbefore %+v\nafter  %+v", before, got)
			}
			if next, err := s.Claim(ctx, "other"); err != nil || next.Issue != 900 {
				t.Fatalf("slot not released: %+v, %v", next, err)
			}
			if _, err := s.RecoverExecution(ctx, task.ID, "w", task.FencingToken); !errors.Is(err, ErrStaleLease) {
				t.Fatalf("ended task was recovered: %v", err)
			}
			if again, err := s.EndExhaustedInterruptions(ctx, nil); err != nil || len(again) != 0 {
				t.Fatalf("sweep is not idempotent: %v, %v", again, err)
			}
		})
	}
}

// If the issue comment cannot be written the task stays reserved, so the
// exhaustion is not lost, but a VCS outage cannot hold the slot for ever.
func TestEndExhaustedInterruptionsBoundsAnnouncementFailures(t *testing.T) {
	s, db, _ := setup(t)
	registerDurable(t, s, "w")
	task := driveToLastAttempt(t, s, 787)
	raw := json.RawMessage(interruptedWithSession)
	if err := s.AcceptResult(WithReportDigest(ctx, raw, true), task.ID, "w", task.FencingToken, raw); err != nil {
		t.Fatal(err)
	}
	if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).Update("lease_expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	down := errors.New("provider down")
	for i := 1; i < maxAnnounceFailures; i++ {
		ended, err := s.EndExhaustedInterruptions(ctx, func(Task) error { return down })
		if !errors.Is(err, down) || len(ended) != 0 {
			t.Fatalf("pass %d: ended=%+v err=%v", i, ended, err)
		}
		got, _ := s.Task(ctx, task.ID)
		if got.State != "leased" || got.ApplicationAttempts != i || got.ApplicationError == "" {
			t.Fatalf("pass %d: task = %+v", i, got)
		}
	}
	ended, err := s.EndExhaustedInterruptions(ctx, func(Task) error { return down })
	if !errors.Is(err, down) || len(ended) != 1 {
		t.Fatalf("last pass: ended=%+v err=%v", ended, err)
	}
	if got, _ := s.Task(ctx, task.ID); got.State != "failed" || got.LeaseExpiresAt != nil {
		t.Fatalf("task still holds its slot after repeated failures: %+v", got)
	}
}

// Reports the sweep must leave alone: effects waiting to be applied, a task
// being recovered, work still running, and issues that were closed or skipped
// (EndInactiveInterruptions owns those and writes nothing to the VCS).
func TestEndExhaustedInterruptionsLeavesOtherTasks(t *testing.T) {
	announce := func(Task) error { t.Fatal("announced a task that must be left alone"); return nil }
	t.Run("running", func(t *testing.T) {
		s, _, _ := setup(t)
		registerDurable(t, s, "w")
		driveToLastAttempt(t, s, 1)
		if ended, err := s.EndExhaustedInterruptions(ctx, announce); err != nil || len(ended) != 0 {
			t.Fatalf("ended a running task: %+v, %v", ended, err)
		}
	})
	t.Run("inactive issue", func(t *testing.T) {
		s, db, _ := setup(t)
		registerDurable(t, s, "w")
		task := driveToLastAttempt(t, s, 1)
		raw := json.RawMessage(`{"status":"failed","infra_kind":"execution-interrupted","error":"` + inactiveText + `","provenance":{"session_id":"native-1"}}`)
		if err := s.AcceptResult(WithReportDigest(ctx, raw, true), task.ID, "w", task.FencingToken, raw); err != nil {
			t.Fatal(err)
		}
		if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).Update("lease_expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
			t.Fatal(err)
		}
		if ended, err := s.EndExhaustedInterruptions(ctx, announce); err != nil || len(ended) != 0 {
			t.Fatalf("ended an inactive-issue interruption: %+v, %v", ended, err)
		}
		if ended, err := s.EndInactiveInterruptions(ctx); err != nil || len(ended) != 1 {
			t.Fatalf("inactive sweep no longer owns it: %+v, %v", ended, err)
		}
	})
	t.Run("unsettled effect", func(t *testing.T) {
		s, db, _ := setup(t)
		registerDurable(t, s, "w")
		task := driveToLastAttempt(t, s, 1)
		raw := json.RawMessage(interruptedWithSession)
		if err := s.AcceptResult(WithReportDigest(ctx, raw, true), task.ID, "w", task.FencingToken, raw); err != nil {
			t.Fatal(err)
		}
		if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).Update("lease_expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.DB().Create(&EffectIntent{ID: "i1", TaskID: task.ID, LeaseEpoch: task.FencingToken, Kind: "issue-comment", Repo: "o/r", Number: 1, IdempotencyKey: "k", PayloadDigest: "d", Payload: json.RawMessage(`{}`), Status: "pending", UpdatedAt: time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
		if ended, err := s.EndExhaustedInterruptions(ctx, announce); err != nil || len(ended) != 0 {
			t.Fatalf("ended a task with pending effects: %+v, %v", ended, err)
		}
	})
	t.Run("non-durable", func(t *testing.T) {
		s, db, _ := setup(t)
		register(t, s, "plain", 1)
		enqueue(t, s, 1)
		task := claim(t, s, "plain")
		raw := json.RawMessage(interruptedWithSession)
		if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).Updates(map[string]any{"attempt": MaxInfrastructureAttempts - 1, "result": raw, "lease_expires_at": time.Now().Add(-time.Minute)}).Error; err != nil {
			t.Fatal(err)
		}
		if ended, err := s.EndExhaustedInterruptions(ctx, announce); err != nil || len(ended) != 0 {
			t.Fatalf("ended a non-durable task: %+v, %v", ended, err)
		}
	})
}
