package coordinator

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"
)

// RecoveryExhausted reports whether a durable development or repair task has
// spent the recovery budget that RecoverExecution enforces
// (Attempt >= MaxInfrastructureAttempts-1), so no further resume can ever be
// authorized. A report that only says the issue is closed or owner-skipped is
// excluded: EndInactiveInterruptions cancels those without writing to the VCS.
func RecoveryExhausted(t *Task) bool {
	return DurableTask(t) && (t.Kind == "implement" || t.Kind == "repair") &&
		t.Attempt >= MaxInfrastructureAttempts-1 && !inactiveIssueReport(t.Result)
}

// retainedReport reports whether a stored result has the shape
// FinalizeApplication reserves: an execution interruption, or the blocked
// launch / coordination-changed report an older pinned executor sends when a
// resume fails its launch checks.
func retainedReport(raw json.RawMessage) bool {
	if InterruptedResult(raw) {
		return true
	}
	var r struct {
		InfraKind string `json:"infra_kind"`
	}
	return json.Unmarshal(raw, &r) == nil && (r.InfraKind == "launch" || r.InfraKind == "coordination-changed")
}

// maxAnnounceFailures bounds how long a failing announcement may hold a slot,
// matching the three-application bound RecordApplicationFailure applies.
const maxAnnounceFailures = 3

// EndExhaustedInterruptions fails durable tasks that were reserved for recovery
// but have no recovery budget left. FinalizeApplication ends such tasks itself;
// this sweep ends the ones an earlier controller left in `leased`, where the
// worker's recovery request only ever returned 409 and the task kept its worker
// and repository slot.
//
// A stuck task is a durable implement/repair task in `leased` with an expired
// lease, a committed interruption result and no unsettled effect intent.
// Tasks still executing have no result, and a result waiting for finalization
// has no lease expiry, so neither matches.
//
// announce makes the exhaustion visible (the caller posts an idempotent issue
// comment) and runs before the state change, outside any SQLite transaction. If
// it fails, the task stays reserved and the failure is counted on the row; the
// third failure ends the task anyway so a VCS outage cannot hold the slot.
//
// The task becomes `failed`. Its result, attempts, fencing epoch, receipts and
// audit trail stay as they were, no task is created, and no worker file or
// session is touched. The ended tasks are returned for logging.
func (s *Store) EndExhaustedInterruptions(ctx context.Context, announce func(Task) error) ([]Task, error) {
	var candidates []Task
	if err := s.db.WithContext(ctx).Where("state = 'leased' AND kind IN ? AND lease_expires_at IS NOT NULL AND lease_expires_at <= ? AND result IS NOT NULL",
		[]string{"implement", "repair"}, controllerNow()).Order("created_at, id").Find(&candidates).Error; err != nil {
		return nil, err
	}
	var ended []Task
	var first error
	for _, c := range candidates {
		if !RecoveryExhausted(&c) || !retainedReport(c.Result) {
			continue
		}
		t, err := s.endExhausted(ctx, c.ID, c.FencingToken, announce)
		if err != nil && first == nil {
			first = err
		}
		if t != nil {
			ended = append(ended, *t)
		}
	}
	return ended, first
}

func (s *Store) endExhausted(ctx context.Context, id string, epoch int64, announce func(Task) error) (*Task, error) {
	// Serialize with result application so FinalizeApplication cannot end the
	// same task, or announce it twice, at the same time.
	unlock := applicationLocks.lock(id)
	defer unlock()

	var t Task
	var unsettled int64
	if err := s.db.WithContext(ctx).First(&t, "id = ?", id).Error; err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Model(&EffectIntent{}).Where("task_id = ? AND status != 'applied'", id).Count(&unsettled).Error; err != nil {
		return nil, err
	}
	if t.State != "leased" || t.FencingToken != epoch || t.LeaseExpiresAt == nil || len(t.Result) == 0 || unsettled != 0 || !RecoveryExhausted(&t) || !retainedReport(t.Result) {
		return nil, nil
	}

	values := map[string]any{"state": "failed", "lease_expires_at": nil, "application_error": ""}
	var announceErr error
	if announce != nil {
		announceErr = announce(t)
	}
	if announceErr != nil {
		attempts := t.ApplicationAttempts + 1
		values["application_attempts"], values["application_error"] = attempts, announceErr.Error()
		if attempts < maxAnnounceFailures {
			err := s.db.WithContext(ctx).Model(&Task{}).Where("id = ? AND state = 'leased' AND fencing_token = ?", id, epoch).
				Updates(map[string]any{"application_attempts": attempts, "application_error": announceErr.Error()}).Error
			return nil, errors.Join(announceErr, err)
		}
	}

	var done bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Task{}).Where("id = ? AND state = 'leased' AND fencing_token = ? AND lease_expires_at IS NOT NULL AND result IS NOT NULL", id, epoch).Updates(values)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return nil
		}
		done = true
		return audit(tx, t.LeaseOwner, id, "recovery-exhausted")
	})
	if err != nil || !done {
		return nil, err
	}
	t.State, t.LeaseExpiresAt = "failed", nil
	return &t, announceErr
}
