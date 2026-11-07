package coordinator

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// The text a worker stores when recovery finds the issue closed or owner-skipped.
// Written out so a change to the production wording fails these tests.
const inactiveText = "coordination context changed: coordination issue is closed or owner-skipped"

func registerDurable(t *testing.T, s *Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := s.RegisterWorker(ctx, Worker{ID: id, Durable: true, MaxParallel: 1, RunUntagged: true, Roles: []string{"operator"}}); err != nil {
			t.Fatal(err)
		}
	}
}

// retainInterruption claims a durable task and leaves it the way a finalized
// interruption does: leased, expired, with its report committed.
func retainInterruption(t *testing.T, s *Store, issue int, raw string) *Task {
	t.Helper()
	if _, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: issue, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
		t.Fatal(err)
	}
	task := claim(t, s, "w")
	report := json.RawMessage(raw)
	if err := s.AcceptResult(WithReportDigest(ctx, report, true), task.ID, "w", task.FencingToken, report); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeApplication(ctx, task.ID, task.FencingToken, true, func(Task) error { return nil }); err != nil {
		t.Fatal(err)
	}
	got, err := s.Task(ctx, task.ID)
	if err != nil || got.State != "leased" || got.LeaseExpiresAt == nil {
		t.Fatalf("interruption was not retained: %+v, %v", got, err)
	}
	return got
}

func countTasks(t *testing.T, s *Store) int64 {
	t.Helper()
	var n int64
	if err := s.db.Model(&Task{}).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// The reported case: the issue closed after the interruption was retained, and
// the recovery attempt found that out. The task must end and free its slot.
func TestEndInactiveInterruptionsReleasesSlot(t *testing.T) {
	for name, raw := range map[string]string{
		"closed issue":      `{"status":"failed","infra_kind":"execution-interrupted","error":"` + inactiveText + `"}`,
		"with a session":    `{"status":"failed","infra_kind":"execution-interrupted","error":"` + inactiveText + `","provenance":{"session_id":"native-1"}}`,
		"unapplied effects": `{"status":"failed","infra_kind":"execution-interrupted","error":"` + inactiveText + `","unapplied_effects":[{"kind":"issue-comment","repo":"o/r","number":773,"body":"never sent"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, _, _ := setup(t)
			registerDurable(t, s, "w", "other")
			s.LeaseLimits = func() (int, map[string]int, error) { return 10, map[string]int{"o/r": 1}, nil }
			task := retainInterruption(t, s, 773, raw)
			before, err := s.Task(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}

			// The retained task holds the repository's only slot.
			if _, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 800, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Claim(ctx, "other"); !errors.Is(err, ErrNoTask) {
				t.Fatalf("slot was not held before the sweep: %v", err)
			}
			tasksBefore := countTasks(t, s)

			ended, err := s.EndInactiveInterruptions(ctx)
			if err != nil || len(ended) != 1 || ended[0].ID != task.ID {
				t.Fatalf("ended = %+v, %v", ended, err)
			}
			got, err := s.Task(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != "cancelled" || got.LeaseExpiresAt != nil {
				t.Fatalf("task not terminal: %+v", got)
			}
			// Evidence and identity are untouched.
			if string(got.Result) != string(before.Result) || got.ResultDigest != before.ResultDigest || got.FencingToken != before.FencingToken || got.Attempt != before.Attempt || got.LeaseOwner != before.LeaseOwner {
				t.Fatalf("evidence changed:\nbefore %+v\nafter  %+v", before, got)
			}
			if !MatchingReport(got, "w", task.FencingToken, json.RawMessage(raw), true) {
				t.Fatal("the worker's acknowledged report no longer matches")
			}
			if acked, err := s.ReportAcknowledged(ctx, task.ID, "w", task.FencingToken, json.RawMessage(raw), true); err != nil || !acked {
				t.Fatalf("report receipt lost: %v, %v", acked, err)
			}
			if countTasks(t, s) != tasksBefore {
				t.Fatal("ending the task created or removed a task")
			}
			var intents int64
			if err := s.db.Model(&EffectIntent{}).Where("task_id = ?", task.ID).Count(&intents).Error; err != nil || intents != 0 {
				t.Fatalf("effect intents appeared: %d, %v", intents, err)
			}
			var audits int64
			if err := s.db.Model(&Audit{}).Where("task = ? AND action = 'interruption-ended:issue-inactive'", task.ID).Count(&audits).Error; err != nil || audits != 1 {
				t.Fatalf("audit rows = %d, %v", audits, err)
			}

			// The slot is free, and nothing may recover or touch the ended task.
			next, err := s.Claim(ctx, "other")
			if err != nil || next.Issue != 800 {
				t.Fatalf("slot not released: %+v, %v", next, err)
			}
			if _, err := s.RecoverExecution(ctx, task.ID, "w", task.FencingToken); !errors.Is(err, ErrStaleLease) {
				t.Fatalf("ended task was recovered: %v", err)
			}
			if err := s.Heartbeat(ctx, task.ID, "w", task.FencingToken); !errors.Is(err, ErrStaleLease) {
				t.Fatalf("ended task kept authority: %v", err)
			}
			active, err := s.Active(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range active {
				if a.ID == task.ID {
					t.Fatal("ended task is still active")
				}
			}
			pending, err := s.PendingResults(ctx)
			if err != nil || len(pending) != 0 {
				t.Fatalf("ended task is pending again: %v, %v", pending, err)
			}
			if again, err := s.EndInactiveInterruptions(ctx); err != nil || len(again) != 0 {
				t.Fatalf("sweep is not idempotent: %v, %v", again, err)
			}
		})
	}
}

// A pinned older executor cannot tell a blocked recovery from a first attempt
// and reports blocked/coordination-changed. FinalizeApplication keeps that task
// because the previous attempt was interrupted; it must end the same way.
func TestEndInactiveInterruptionsOlderExecutorShape(t *testing.T) {
	s, db, _ := setup(t)
	registerDurable(t, s, "w")
	if _, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 773, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
		t.Fatal(err)
	}
	task := claim(t, s, "w")
	if err := db.DB().Model(&Task{}).Where("id = ?", task.ID).Update("lease_expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	next, err := s.RecoverExecution(ctx, task.ID, "w", task.FencingToken)
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"status":"blocked","infra_kind":"coordination-changed","next_owner":"scheduling","error":"` + inactiveText + `"}`)
	if err := s.AcceptResult(WithReportDigest(ctx, raw, true), next.ID, "w", next.FencingToken, raw); err != nil {
		t.Fatal(err)
	}
	if err := s.FinalizeApplication(ctx, next.ID, next.FencingToken, true, func(Task) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Task(ctx, next.ID); got.State != "leased" {
		t.Fatalf("precondition: task should be retained: %+v", got)
	}
	ended, err := s.EndInactiveInterruptions(ctx)
	if err != nil || len(ended) != 1 {
		t.Fatalf("ended = %+v, %v", ended, err)
	}
	if got, _ := s.Task(ctx, next.ID); got.State != "cancelled" || got.FencingToken != next.FencingToken || got.Attempt != 1 {
		t.Fatalf("not ended cleanly: %+v", got)
	}
}

// Every other reason keeps the existing behavior: the task stays reserved.
func TestEndInactiveInterruptionsKeepsOtherReasons(t *testing.T) {
	for name, raw := range map[string]string{
		"timeout with a session":      `{"status":"failed","infra_kind":"execution-interrupted","error":"context deadline exceeded","provenance":{"session_id":"native-1"}}`,
		"no error text":               `{"status":"failed","infra_kind":"execution-interrupted","provenance":{"session_id":"native-1"}}`,
		"snapshot changed":            `{"status":"failed","infra_kind":"execution-interrupted","error":"coordination context changed before worker start; scheduling must reassess"}`,
		"invalid approved snapshot":   `{"status":"failed","infra_kind":"execution-interrupted","error":"coordination context changed: invalid approved snapshot"}`,
		"issue unavailable":           `{"status":"failed","infra_kind":"execution-interrupted","error":"coordination context changed: coordination issue unavailable"}`,
		"provider failure":            `{"status":"failed","infra_kind":"execution-interrupted","error":"coordination context changed: provider offline"}`,
		"cause quoted inside another": `{"status":"failed","infra_kind":"execution-interrupted","error":"retry: ` + inactiveText + `"}`,
		"other infra kind":            `{"status":"failed","infra_kind":"launch","error":"` + inactiveText + `"}`,
		"success status":              `{"status":"success","infra_kind":"execution-interrupted","error":"` + inactiveText + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			s, _, _ := setup(t)
			registerDurable(t, s, "w")
			s.LeaseLimits = func() (int, map[string]int, error) { return 10, map[string]int{"o/r": 1}, nil }
			var task *Task
			if name == "other infra kind" || name == "success status" {
				// FinalizeApplication would not retain these; hold them by hand
				// so the sweep's own filter is what is tested.
				if _, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 9, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
					t.Fatal(err)
				}
				task = claim(t, s, "w")
				if err := s.db.Model(&Task{}).Where("id = ?", task.ID).Updates(map[string]any{"result": raw, "lease_expires_at": time.Now().Add(-time.Minute)}).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				task = retainInterruption(t, s, 9, raw)
			}
			ended, err := s.EndInactiveInterruptions(ctx)
			if err != nil || len(ended) != 0 {
				t.Fatalf("ended an unrelated task: %+v, %v", ended, err)
			}
			got, err := s.Task(ctx, task.ID)
			if err != nil || got.State != "leased" || got.LeaseOwner != "w" || got.FencingToken != task.FencingToken {
				t.Fatalf("task changed: %+v, %v", got, err)
			}
		})
	}
}

// Work that is still alive or still owed an application is never ended, even
// when its result carries the same words.
func TestEndInactiveInterruptionsLeavesLiveWork(t *testing.T) {
	raw := json.RawMessage(`{"status":"failed","infra_kind":"execution-interrupted","error":"` + inactiveText + `"}`)
	t.Run("valid lease", func(t *testing.T) {
		s, _, _ := setup(t)
		registerDurable(t, s, "w")
		task := retainInterruption(t, s, 1, string(raw))
		future := time.Now().Add(time.Hour)
		if err := s.db.Model(&Task{}).Where("id = ?", task.ID).Update("lease_expires_at", future).Error; err != nil {
			t.Fatal(err)
		}
		if ended, err := s.EndInactiveInterruptions(ctx); err != nil || len(ended) != 0 {
			t.Fatalf("ended a task with valid authority: %+v, %v", ended, err)
		}
	})
	t.Run("result awaiting application", func(t *testing.T) {
		s, _, _ := setup(t)
		registerDurable(t, s, "w")
		if _, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 1, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
			t.Fatal(err)
		}
		task := claim(t, s, "w")
		if err := s.AcceptResult(WithReportDigest(ctx, raw, true), task.ID, "w", task.FencingToken, raw); err != nil {
			t.Fatal(err)
		}
		if ended, err := s.EndInactiveInterruptions(ctx); err != nil || len(ended) != 0 {
			t.Fatalf("ended a result that was still being applied: %+v, %v", ended, err)
		}
		if err := s.FinalizeApplication(ctx, task.ID, task.FencingToken, true, func(Task) error { return nil }); err != nil {
			t.Fatalf("finalization lost: %v", err)
		}
	})
	t.Run("still executing", func(t *testing.T) {
		s, _, _ := setup(t)
		registerDurable(t, s, "w")
		if _, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 1, Kind: "implement", Operator: "implement", Payload: json.RawMessage(`{"timeout_sec":60}`)}); err != nil {
			t.Fatal(err)
		}
		task := claim(t, s, "w")
		if err := s.db.Model(&Task{}).Where("id = ?", task.ID).Update("lease_expires_at", time.Now().Add(-time.Minute)).Error; err != nil {
			t.Fatal(err)
		}
		if ended, err := s.EndInactiveInterruptions(ctx); err != nil || len(ended) != 0 {
			t.Fatalf("ended a task with no result: %+v, %v", ended, err)
		}
	})
	t.Run("not durable", func(t *testing.T) {
		s, _, _ := setup(t)
		if err := s.RegisterWorker(ctx, Worker{ID: "w", MaxParallel: 1, RunUntagged: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Enqueue(ctx, Task{Repo: "o/r", Issue: 1, Kind: "implement", Operator: "implement"}); err != nil {
			t.Fatal(err)
		}
		task := claim(t, s, "w")
		if err := s.db.Model(&Task{}).Where("id = ?", task.ID).Updates(map[string]any{"result": string(raw), "lease_expires_at": time.Now().Add(-time.Minute)}).Error; err != nil {
			t.Fatal(err)
		}
		if ended, err := s.EndInactiveInterruptions(ctx); err != nil || len(ended) != 0 {
			t.Fatalf("ended a non-durable task: %+v, %v", ended, err)
		}
	})
	t.Run("unapplied intents", func(t *testing.T) {
		s, _, _ := setup(t)
		registerDurable(t, s, "w")
		task := retainInterruption(t, s, 1, string(raw))
		if err := s.db.Create(&EffectIntent{ID: "i1", TaskID: task.ID, LeaseEpoch: task.FencingToken, Kind: "issue-comment", Repo: "o/r", Number: 1, IdempotencyKey: "k", PayloadDigest: "d", Payload: json.RawMessage(`{}`), Status: "pending", UpdatedAt: time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
		if ended, err := s.EndInactiveInterruptions(ctx); err != nil || len(ended) != 0 {
			t.Fatalf("ended a task with pending effects: %+v, %v", ended, err)
		}
	})
	t.Run("recovered meanwhile", func(t *testing.T) {
		// A worker that already received a new epoch has no committed result
		// at the old one; the sweep must not touch the new authority.
		s, _, _ := setup(t)
		registerDurable(t, s, "w")
		withSession := json.RawMessage(`{"status":"failed","infra_kind":"execution-interrupted","error":"` + inactiveText + `","provenance":{"session_id":"native-1"}}`)
		task := retainInterruption(t, s, 1, string(withSession))
		next, err := s.RecoverExecution(ctx, task.ID, "w", task.FencingToken)
		if err != nil {
			t.Fatal(err)
		}
		if ended, err := s.EndInactiveInterruptions(ctx); err != nil || len(ended) != 0 {
			t.Fatalf("ended a recovered task: %+v, %v", ended, err)
		}
		if got, _ := s.Task(ctx, task.ID); got.State != "leased" || got.FencingToken != next.FencingToken {
			t.Fatalf("recovered authority changed: %+v", got)
		}
	})
}
