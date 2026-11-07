package coordinator

import (
	"context"
	"encoding/json"
	"errors"

	"gorm.io/gorm"
)

// ErrCoordinationInactive is the cause a worker records when the coordinated
// issue is closed or carries clawflow-skip. validateWorkerCoordination wraps it
// in ErrCoordinationChanged, so a stored report carries the joined text.
var ErrCoordinationInactive = errors.New("coordination issue is closed or owner-skipped")

// inactiveIssueError is the exact error text of such a report.
func inactiveIssueError() string {
	return ErrCoordinationChanged.Error() + ": " + ErrCoordinationInactive.Error()
}

// inactiveIssueReport reports whether a stored report is a recovery that
// stopped only because the issue is closed or owner-skipped. Two shapes exist:
// the current worker turns a blocked recovery into an execution interruption
// (no model ran, so it carries no session), while a pinned older executor
// reports the blocked coordination-changed result itself. Any other
// coordination-changed cause, and any report with executable effects, is not
// matched.
func inactiveIssueReport(raw json.RawMessage) bool {
	var r struct {
		Status    string            `json:"status"`
		InfraKind string            `json:"infra_kind"`
		Error     string            `json:"error"`
		Effects   []json.RawMessage `json:"effects"`
	}
	if json.Unmarshal(raw, &r) != nil || r.Error != inactiveIssueError() || len(r.Effects) != 0 {
		return false
	}
	return (r.Status == "failed" && r.InfraKind == "execution-interrupted") ||
		(r.Status == "blocked" && r.InfraKind == "coordination-changed")
}

// EndInactiveInterruptions finishes durable tasks that FinalizeApplication kept
// reserved after an interruption, when the recovery attempt found the issue
// closed or owner-skipped. Nothing can resume such a task: the issue no longer
// wants the work, and the report holds no session to resume. Left alone it
// would keep its repository slot forever.
//
// A kept task is a leased durable implement/repair task with an expired lease
// and a committed result. Tasks still executing have no result, and a result
// waiting for finalization has no lease expiry, so neither matches. The task
// becomes cancelled. Its result, attempts, fencing epoch and audit trail are
// left as they were, and no worker file or VCS state is touched, so the
// retained worktree and session remain available as evidence.
//
// The ended tasks are returned so the caller can log them.
func (s *Store) EndInactiveInterruptions(ctx context.Context) ([]Task, error) {
	var ended []Task
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []Task
		if err := tx.Where("state = 'leased' AND kind IN ? AND lease_expires_at IS NOT NULL AND lease_expires_at <= ? AND result IS NOT NULL AND json_extract(result, '$.error') = ?",
			[]string{"implement", "repair"}, controllerNow(), inactiveIssueError()).Order("created_at, id").Find(&candidates).Error; err != nil {
			return err
		}
		var done []Task
		for _, t := range candidates {
			if !DurableTask(&t) || !inactiveIssueReport(t.Result) {
				continue
			}
			var unsettled int64
			if err := tx.Model(&EffectIntent{}).Where("task_id = ? AND status != 'applied'", t.ID).Count(&unsettled).Error; err != nil {
				return err
			}
			if unsettled != 0 {
				continue
			}
			// The fencing epoch stays: a worker replaying its acknowledged
			// report still matches, and upgrade continuity sees no epoch change.
			res := tx.Model(&Task{}).Where("id = ? AND state = 'leased' AND fencing_token = ? AND lease_expires_at IS NOT NULL AND result IS NOT NULL", t.ID, t.FencingToken).
				Updates(map[string]any{"state": "cancelled", "lease_expires_at": nil})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				continue
			}
			if err := tx.Model(&TaskAttempt{}).Where("task_id = ? AND ended_at IS NULL", t.ID).Updates(map[string]any{"status": "cancelled", "ended_at": controllerNow()}).Error; err != nil {
				return err
			}
			if err := audit(tx, t.LeaseOwner, t.ID, "interruption-ended:issue-inactive"); err != nil {
				return err
			}
			t.State, t.LeaseExpiresAt = "cancelled", nil
			done = append(done, t)
		}
		ended = done
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ended, nil
}
