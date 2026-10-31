package coordinator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EffectRequest is the only worker-authorized VCS write vocabulary.
type EffectRequest struct {
	Kind   string   `json:"kind"`
	Repo   string   `json:"repo"`
	Number int      `json:"number"`
	Body   string   `json:"body,omitempty"`
	RunID  string   `json:"run_id,omitempty"`
	Labels []string `json:"labels,omitempty"`
	Head   string   `json:"head,omitempty"`
	Base   string   `json:"base,omitempty"`
	Title  string   `json:"title,omitempty"`
}
type EffectIntent struct {
	ID               string `gorm:"primaryKey"`
	TaskID           string
	LeaseEpoch       int64
	Position         int
	Kind             string
	Repo             string
	Number           int
	IdempotencyKey   string
	PayloadDigest    string
	Payload          json.RawMessage `gorm:"serializer:rawjson"`
	Status           string
	AppliedReference string
	UpdatedAt        time.Time
}
type TaskAttempt struct {
	ResultDigest  string
	TaskID        string `gorm:"primaryKey"`
	AttemptNumber int64  `gorm:"primaryKey"`
	LeaseEpoch    int64
	WorkerID      string
	Kind          string
	Status        string
	StartedAt     time.Time
	EndedAt       *time.Time
	ErrorKind     string
}
type InboxEvent struct {
	Source      string `gorm:"primaryKey"`
	ExternalID  string `gorm:"primaryKey"`
	TaskID      string
	ReceivedAt  time.Time
	ProcessedAt time.Time
}

// EffectStore serializes application with cancellation on the authoritative task.
// The callback MUST reconcile the remote marker before issuing any write.
type EffectStore interface {
	PendingResults(context.Context) ([]Task, error)
	RecordApplicationFailure(context.Context, string, int64, string) error
	ApplyEffects(context.Context, string, int64, func(EffectIntent) (EffectResult, error)) error
	Cancel(context.Context, string) error
	FinalizeApplication(context.Context, string, int64, bool, func(Task) error) error
}
type EffectResult struct {
	Reference    string
	PR           int
	HeadMismatch bool
}

func acceptIntents(tx *gorm.DB, t Task, result json.RawMessage) error {
	var r struct {
		Effects   []EffectRequest `json:"effects"`
		HeadSHA   string          `json:"head_sha"`
		InfraKind string          `json:"infra_kind"`
		Status    string          `json:"status"`
	}
	if err := json.Unmarshal(result, &r); err != nil {
		return ErrInvalid
	}
	if len(r.Effects) > 100 {
		return ErrInvalid
	}
	prHead := ""
	for i, e := range r.Effects {
		if e.Repo != t.Repo {
			return ErrInvalid
		}
		switch e.Kind {
		case "pr-create":
			if prHead != "" && prHead != e.Head {
				return ErrInvalid
			}
			prHead = e.Head
			if e.Head == "" || e.Base == "" || e.Title == "" || r.HeadSHA == "" {
				return ErrInvalid
			}
		case "issue-comment", "pr-comment", "add-label", "remove-label", "close-issue":
			if e.Number <= 0 || (e.Number != t.Issue && e.Number != t.PR) {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		key := fmt.Sprintf("%s/%d/%d", t.ID, t.FencingToken, i)
		if e.Kind == "pr-create" {
			key = t.ID + "/pr/" + e.Head
		}
		// The intent key is coordinator-owned; preserve the runner's stable
		// comment marker when one was already assigned.
		sum := sha256.Sum256([]byte(key))
		id := hex.EncodeToString(sum[:])
		if e.RunID == "" {
			e.RunID = "effect-" + id
		}
		payload, err := json.Marshal(e)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(payload)
		intent := EffectIntent{ID: id, TaskID: t.ID, LeaseEpoch: t.FencingToken, Position: i, Kind: e.Kind, Repo: e.Repo, Number: e.Number, IdempotencyKey: key, PayloadDigest: hex.EncodeToString(digest[:]), Payload: payload, Status: "pending", UpdatedAt: time.Now().UTC()}
		if e.Kind == "pr-create" {
			intent.Position = -1
		}
		var existing EffectIntent
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&intent)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			if err := tx.First(&existing, "id = ?", id).Error; err != nil {
				return err
			}
			if e.Kind == "pr-create" && existing.LeaseEpoch < t.FencingToken {
				// Infrastructure retries reuse the logical PR, but must verify
				// the newly submitted head under the new lease epoch.
				if err := tx.Model(&existing).Updates(map[string]any{"lease_epoch": intent.LeaseEpoch, "payload": intent.Payload, "payload_digest": intent.PayloadDigest, "status": "pending", "updated_at": intent.UpdatedAt}).Error; err != nil {
					return err
				}
				continue
			}
			if existing.PayloadDigest != intent.PayloadDigest {
				return ErrInvalid
			}
		}
	}
	kind := "product"
	if r.InfraKind != "" && r.InfraKind != "no-change" {
		kind = "infra"
	}
	return tx.Model(&TaskAttempt{}).Where("task_id = ? AND lease_epoch = ?", t.ID, t.FencingToken).Updates(map[string]any{"status": r.Status, "kind": kind, "ended_at": time.Now().UTC(), "error_kind": r.InfraKind}).Error
}

func (s *Store) PendingResults(ctx context.Context) ([]Task, error) {
	var tasks []Task
	err := s.db.WithContext(ctx).Where("state = 'leased' AND lease_expires_at IS NULL AND result IS NOT NULL").Order("created_at, id").Find(&tasks).Error
	return tasks, err
}

func (s *Store) ApplyEffects(ctx context.Context, id string, epoch int64, apply func(EffectIntent) (EffectResult, error)) error {
	var remoteErr error
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t Task
		if err := tx.First(&t, "id = ?", id).Error; err != nil {
			return err
		}
		if t.FencingToken != epoch || t.State != "leased" || t.LeaseExpiresAt != nil || len(t.Result) == 0 {
			return ErrStaleLease
		}
		// Reports sealed before this migration may have already written an
		// unmarked comment. Preserve them for explicit reconciliation rather
		// than inventing a new marker and potentially duplicating that write.
		var count int64
		if err := tx.Model(&EffectIntent{}).Where("task_id = ? AND lease_epoch = ?", id, epoch).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			var report struct {
				Effects []EffectRequest `json:"effects"`
			}
			if err := json.Unmarshal(t.Result, &report); err != nil {
				return err
			}
			if len(report.Effects) > 0 {
				return fmt.Errorf("legacy report has no durable intents; reconcile existing effects before recovery")
			}
		}
		var intents []EffectIntent
		if err := tx.Where("task_id = ? AND status IN ?", id, []string{"pending", "uncertain"}).Order("position, id").Find(&intents).Error; err != nil {
			return err
		}
		for _, intent := range intents {
			if intent.LeaseEpoch != t.FencingToken {
				return ErrStaleLease
			}
			res, err := apply(intent)
			intent.Status = "applied"
			intent.AppliedReference = res.Reference
			intent.UpdatedAt = time.Now().UTC()
			if err != nil {
				intent.Status = "uncertain"
				remoteErr = err
			}
			if err := tx.Save(&intent).Error; err != nil {
				return err
			}
			if remoteErr != nil {
				break
			}
			if res.PR > 0 {
				t.PR = res.PR
				if err := tx.Model(&t).Update("pr", res.PR).Error; err != nil {
					return err
				}
			}
			if res.HeadMismatch {
				if err := tx.Model(&t).Update("state", "head-mismatch").Error; err != nil {
					return err
				}
				if err := tx.Model(&TaskAttempt{}).Where("task_id = ? AND lease_epoch = ?", id, epoch).Update("status", "head-mismatch").Error; err != nil {
					return err
				}
				return failIntents(tx, id)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return remoteErr
}
func failIntents(tx *gorm.DB, id string) error {
	return tx.Model(&EffectIntent{}).Where("task_id = ? AND status IN ?", id, []string{"pending", "uncertain"}).Updates(map[string]any{"status": "failed", "updated_at": time.Now().UTC()}).Error
}
func (s *Store) Cancel(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t Task
		if err := tx.First(&t, "id = ?", id).Error; err != nil {
			return err
		}
		if t.State == "cancelled" {
			return nil
		}
		if err := tx.Model(&t).Updates(map[string]any{"state": "cancelled", "fencing_token": gorm.Expr("fencing_token + 1"), "lease_expires_at": nil}).Error; err != nil {
			return err
		}
		if err := failIntents(tx, id); err != nil {
			return err
		}
		if err := tx.Model(&TaskAttempt{}).Where("task_id = ? AND ended_at IS NULL", id).Updates(map[string]any{"status": "cancelled", "ended_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		return audit(tx, "", id, "cancel")
	})
}

// FinalizeApplication prevents cancellation or a second applier from racing
// coordinator post-processing. External writes in finalize must be idempotent.
func (s *Store) FinalizeApplication(ctx context.Context, id string, epoch int64, failed bool, finalize func(Task) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t Task
		if err := tx.First(&t, "id = ?", id).Error; err != nil {
			return err
		}
		if t.FencingToken != epoch || t.State != "leased" || t.LeaseExpiresAt != nil || len(t.Result) == 0 {
			return ErrStaleLease
		}
		var count int64
		if err := tx.Model(&EffectIntent{}).Where("task_id = ? AND status != 'applied'", id).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return ErrInvalid
		}
		if err := finalize(t); err != nil {
			return err
		}
		var r struct {
			InfraKind string `json:"infra_kind"`
		}
		if err := json.Unmarshal(t.Result, &r); err != nil {
			return err
		}
		state := "done"
		if failed {
			state = "failed"
		}
		values := map[string]any{"state": state, "application_error": ""}
		if r.InfraKind != "" && r.InfraKind != "no-change" {
			t.Attempt++
			values["attempt"] = t.Attempt
			// Repairs must return through the scheduler so ledger cooldown and a
			// fresh enqueue reservation govern the next attempt.
			if t.Attempt < MaxInfrastructureAttempts && t.Kind != "repair" {
				values["state"] = "pending"
				values["result"] = nil
				values["lease_owner"] = ""
				values["fencing_token"] = gorm.Expr("fencing_token + 1")
			}
		}
		if err := tx.Model(&t).Updates(values).Error; err != nil {
			return err
		}
		return audit(tx, t.LeaseOwner, id, "result-applied")
	})
}

// PRBlocked holds a mismatched PR until a newer successful attempt has
// verified that same PR. Merely enqueueing another task cannot clear the gate.
func (s *Store) PRBlocked(ctx context.Context, repo string, pr int) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&Task{}).Where("repo = ? AND pr = ? AND state = 'head-mismatch' AND NOT EXISTS (SELECT 1 FROM coordinator_tasks newer WHERE newer.repo = coordinator_tasks.repo AND newer.pr = coordinator_tasks.pr AND newer.kind IN ('implement','repair') AND newer.state = 'done' AND newer.created_at > coordinator_tasks.created_at)", repo, pr).Count(&count).Error
	return count > 0, err
}

// RecordApplicationFailure quarantines a poisoned report after three applications.
// Its result and diagnostic remain available for inspection across restarts.
func (s *Store) RecordApplicationFailure(ctx context.Context, id string, epoch int64, message string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var t Task
		if err := tx.First(&t, "id = ?", id).Error; err != nil {
			return err
		}
		if t.State != "leased" || t.FencingToken != epoch || t.LeaseExpiresAt != nil {
			return ErrStaleLease
		}
		values := map[string]any{"application_attempts": t.ApplicationAttempts + 1, "application_error": message}
		if t.ApplicationAttempts+1 >= 3 {
			values["state"] = "failed"
			if err := failIntents(tx, id); err != nil {
				return err
			}
		}
		if err := tx.Model(&t).Updates(values).Error; err != nil {
			return err
		}
		return audit(tx, t.LeaseOwner, id, "result-apply-failed")
	})
}
