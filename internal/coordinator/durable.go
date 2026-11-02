package coordinator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"gorm.io/gorm"
)

// DurableReportGrace reserves time after the execution deadline for result upload.
// Heartbeats do not extend this authority; partitions cannot authorize infinite work.
const DurableReportGrace = 10 * time.Minute

type reportDigestKey struct{}

func reportDigest(raw json.RawMessage, failed bool) string {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return ""
	}
	canonical, _ := json.Marshal(value)
	if failed {
		canonical = append([]byte("fail:"), canonical...)
	} else {
		canonical = append([]byte("complete:"), canonical...)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func WithReportDigest(ctx context.Context, raw json.RawMessage, failed bool) context.Context {
	return context.WithValue(ctx, reportDigestKey{}, reportDigest(raw, failed))
}

// MatchingReport acknowledges only the exact committed report from its original owner.
// It remains valid after finalization but cannot accept another attempt or payload.
func MatchingReport(t *Task, worker string, epoch int64, raw json.RawMessage, failed bool) bool {
	return len(t.Result) > 0 && t.ResultDigest != "" && t.ResultDigest == reportDigest(raw, failed) && t.LeaseOwner == worker && t.FencingToken == epoch
}

// Historical receipts survive infrastructure retries advancing the task epoch.
func (s *Store) ReportAcknowledged(ctx context.Context, id, worker string, epoch int64, raw json.RawMessage, failed bool) (bool, error) {
	var count int64
	digest := reportDigest(raw, failed)
	if digest == "" {
		return false, ErrInvalid
	}
	err := s.db.WithContext(ctx).Model(&TaskAttempt{}).Where("task_id = ? AND worker_id = ? AND lease_epoch = ? AND result_digest = ?", id, worker, epoch, digest).Count(&count).Error
	return count == 1, err
}

func DurableTask(t *Task) bool {
	var payload struct {
		Durable bool `json:"durable_execution"`
	}
	return json.Unmarshal(t.Payload, &payload) == nil && payload.Durable
}

// RecoverExecution reauthorizes only the same worker after the old authority
// has ended. Running, cancelled, reported and superseded tasks cannot be resumed.
func (s *Store) RecoverExecution(ctx context.Context, id, worker string, epoch int64) (*Task, error) {
	var task Task
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&task, "id = ?", id).Error; err != nil {
			return err
		}
		now := controllerNow()
		if DurableTask(&task) && task.State == "leased" && task.LeaseOwner == worker && task.FencingToken == epoch+1 && task.Result == nil {
			var previous TaskAttempt
			if err := tx.First(&previous, "task_id = ? AND lease_epoch = ? AND worker_id = ? AND status = 'interrupted'", id, epoch, worker).Error; err == nil {
				return nil
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		if !DurableTask(&task) || task.State != "leased" || task.LeaseOwner != worker || task.FencingToken != epoch || task.Result != nil || task.LeaseExpiresAt == nil || task.LeaseExpiresAt.After(now) || task.Attempt >= MaxInfrastructureAttempts-1 {
			return ErrStaleLease
		}
		var payload struct {
			Timeout int64 `json:"timeout_sec"`
		}
		if json.Unmarshal(task.Payload, &payload) != nil || payload.Timeout < 1 || payload.Timeout > 86400 {
			return ErrInvalid
		}
		if err := tx.Model(&TaskAttempt{}).Where("task_id = ? AND lease_epoch = ?", id, epoch).Updates(map[string]any{"status": "interrupted", "ended_at": now, "kind": "infra", "error_kind": "executor-interrupted"}).Error; err != nil {
			return err
		}
		task.Attempt++
		task.FencingToken++
		deadline := now.Add(time.Duration(payload.Timeout)*time.Second + DurableReportGrace)
		task.LeaseExpiresAt = &deadline
		if err := tx.Save(&task).Error; err != nil {
			return err
		}
		if err := tx.Create(&TaskAttempt{TaskID: id, AttemptNumber: int64(task.Attempt + 1), LeaseEpoch: task.FencingToken, WorkerID: worker, Kind: "product", Status: "running", StartedAt: now}).Error; err != nil {
			return err
		}
		return audit(tx, worker, id, "recover-execution")
	})
	return &task, err
}

var claimIDPattern = regexp.MustCompile(`^[a-zA-Z0-9-]{1,80}$`)

// ClaimWithID preserves assignment across a lost response or manager crash.
func (s *Store) ClaimWithID(ctx context.Context, worker, id string) (*Task, error) {
	if !claimIDPattern.MatchString(id) {
		return nil, ErrInvalid
	}
	var task *Task
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing Task
		err := tx.Where("lease_owner = ? AND json_extract(payload, '$.worker_claim_id') = ?", worker, id).First(&existing).Error
		if err == nil {
			if existing.State != "leased" || existing.Result != nil {
				return ErrNoTask
			}
			task = &existing
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var registered Worker
		if err := tx.First(&registered, "id = ?", worker).Error; err != nil {
			return err
		}
		if !registered.Durable {
			return ErrInvalid
		}
		store := &Store{db: tx, LeaseLimits: s.LeaseLimits}
		task, err = store.claim(ctx, worker, id)
		return err
	})
	return task, err
}
