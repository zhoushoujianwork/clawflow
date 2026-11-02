package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AdoptLegacyInterruption is an offline migration, not a normal retry API.
// The caller holds controller ownership and has verified retained local sessions.
// Never discard a successful result, explicit cancellation, or any effect intent.
func (s *Store) AdoptLegacyInterruption(ctx context.Context, id, worker string, epoch int64, expected json.RawMessage) (*Task, error) {
	var task Task
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&task, "id = ?", id).Error; err != nil {
			return err
		}
		if DurableTask(&task) && task.LeaseOwner == worker && task.FencingToken == epoch+1 && task.State == "leased" && task.Result == nil {
			var count int64
			if err := tx.Model(&TaskAttempt{}).Where("task_id = ? AND lease_epoch = ? AND error_kind = 'legacy-interruption'", id, epoch).Count(&count).Error; err != nil {
				return err
			}
			if count == 1 {
				return nil
			}
		}
		if task.State != "leased" || task.LeaseOwner != worker || task.FencingToken != epoch || task.LeaseExpiresAt != nil || task.Result == nil || DurableTask(&task) || task.Attempt >= MaxInfrastructureAttempts-1 {
			return ErrStaleLease
		}
		if !bytes.Equal(bytes.TrimSpace(task.Result), bytes.TrimSpace(expected)) {
			// PostgreSQL JSONB formatting is not a report identity change.
			if reportDigest(task.Result, true) != reportDigest(expected, true) {
				return ErrInvalid
			}
		}
		var report struct {
			Status  string            `json:"status"`
			Error   string            `json:"error"`
			Effects []json.RawMessage `json:"effects"`
		}
		if json.Unmarshal(task.Result, &report) != nil || report.Status != "failed" || report.Error != "Offload execution: canceled" || len(report.Effects) != 0 {
			return ErrInvalid
		}
		var effects int64
		if err := tx.Model(&EffectIntent{}).Where("task_id = ?", id).Count(&effects).Error; err != nil {
			return err
		}
		if effects != 0 {
			return ErrInvalid
		}
		var payload map[string]json.RawMessage
		if json.Unmarshal(task.Payload, &payload) != nil {
			return ErrInvalid
		}
		var timeout int64
		if json.Unmarshal(payload["timeout_sec"], &timeout) != nil || timeout < 1 || timeout > 86400 {
			return ErrInvalid
		}
		now := controllerNow()
		payload["durable_execution"] = json.RawMessage("true")
		payload["legacy_recovery"] = json.RawMessage("true")
		task.Payload, _ = json.Marshal(payload)
		deadline := now.Add(time.Duration(timeout)*time.Second + DurableReportGrace)
		task.LeaseExpiresAt = &deadline
		task.Attempt++
		task.FencingToken++
		task.Result = nil
		task.ResultDigest = ""
		task.ApplicationError = ""
		task.ApplicationAttempts = 0
		if err := tx.Model(&TaskAttempt{}).Where("task_id = ? AND lease_epoch = ?", id, epoch).Updates(map[string]any{"status": "interrupted", "error_kind": "legacy-interruption", "kind": "infra", "ended_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Save(&task).Error; err != nil {
			return err
		}
		if err := tx.Create(&TaskAttempt{TaskID: id, AttemptNumber: int64(task.Attempt + 1), LeaseEpoch: task.FencingToken, WorkerID: worker, Kind: "product", Status: "running", StartedAt: now}).Error; err != nil {
			return err
		}
		return audit(tx, worker, id, "adopt-legacy-interruption")
	})
	return &task, err
}
