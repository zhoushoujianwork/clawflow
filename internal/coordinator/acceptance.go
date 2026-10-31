package coordinator

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"time"

	"gorm.io/gorm"
)

// AcceptanceRouting is coordinator-owned task policy, never a worker assertion.
type AcceptanceRouting struct {
	AcceptanceWorker  string `json:"acceptance_worker"`
	DevelopmentWorker string `json:"development_worker"`
}

func AcceptanceEligible(tags []string, w Worker) bool {
	return slices.Contains(w.Roles, "acceptance") && eligible(tags, w)
}

func acceptanceAllowed(t Task, w Worker) bool {
	if t.Kind != "acceptance" {
		return len(w.Roles) == 0 || slices.Contains(w.Roles, "operator")
	}
	var p AcceptanceRouting
	if json.Unmarshal(t.Payload, &p) != nil || !AcceptanceEligible(t.Tags, w) {
		return false
	}
	return (p.AcceptanceWorker == "" || p.AcceptanceWorker == "any" || p.AcceptanceWorker == "different") && p.DevelopmentWorker != "" && p.DevelopmentWorker != w.ID
}

// LatestDevelopment ties the developer worker to the same PR and submitted head.
func (s *Store) LatestDevelopment(ctx context.Context, repo string, issue, pr int, head string) (*Task, error) {
	var task Task
	err := s.db.WithContext(ctx).Select("id", "lease_owner").Where("repo = ? AND issue = ? AND kind IN ? AND state = 'done' AND json_extract(result, '$.status') = 'success' AND json_extract(result, '$.head_sha') = ? AND (pr = ? OR CAST(json_extract(result, '$.pr_number') AS TEXT) = ?)", repo, issue, []string{"implement", "repair"}, head, pr, strconv.Itoa(pr)).Order("created_at DESC, id DESC").First(&task).Error
	return &task, err
}

// Resolve legacy queued tasks to the same independent default as config loading.
func resolveAcceptancePolicy(t *Task) error {
	if t.Kind != "acceptance" {
		return nil
	}
	var p map[string]json.RawMessage
	if json.Unmarshal(t.Payload, &p) != nil || p == nil {
		return ErrInvalid
	}
	var policy string
	if raw := p["acceptance_worker"]; len(raw) > 0 && json.Unmarshal(raw, &policy) != nil {
		return ErrInvalid
	}
	if policy != "" {
		if policy != "any" && policy != "different" {
			return ErrInvalid
		}
		return nil
	}
	policy = "different"
	p["acceptance_worker"], _ = json.Marshal(policy)
	var err error
	t.Payload, err = json.Marshal(p)
	return err
}

// Changed contracts fence out obsolete reviews so deduplication cannot retain
// a stale task forever. Concurrent uploads serialize against this row update.
func (s *Store) CancelSupersededAcceptance(ctx context.Context, repo string, pr int, contract json.RawMessage) error {
	if !json.Valid(contract) {
		return ErrInvalid
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []Task
		if err := tx.Where("repo = ? AND pr = ? AND kind = 'acceptance' AND state IN ?", repo, pr, []string{"pending", "leased"}).Find(&candidates).Error; err != nil {
			return err
		}
		// JSON documents are compared semantically (key order and spacing are
		// irrelevant), as the former jsonb comparison did. The transaction holds
		// the writer lock, so the set cannot change before the update below.
		var want any
		if err := json.Unmarshal(contract, &want); err != nil {
			return ErrInvalid
		}
		var cancelled []Task
		for _, task := range candidates {
			var envelope struct {
				Acceptance json.RawMessage `json:"acceptance"`
			}
			var have any
			present := json.Unmarshal(task.Payload, &envelope) == nil && len(envelope.Acceptance) > 0
			if present {
				_ = json.Unmarshal(envelope.Acceptance, &have)
			}
			if present && reflect.DeepEqual(have, want) {
				continue
			}
			if err := tx.Model(&Task{}).Where("id = ?", task.ID).Updates(map[string]any{"state": "cancelled", "lease_expires_at": nil, "fencing_token": gorm.Expr("fencing_token + 1")}).Error; err != nil {
				return err
			}
			cancelled = append(cancelled, task)
		}
		for _, task := range cancelled {
			if err := failIntents(tx, task.ID); err != nil {
				return err
			}
			if err := tx.Model(&TaskAttempt{}).Where("task_id = ? AND ended_at IS NULL", task.ID).Updates(map[string]any{"status": "cancelled", "ended_at": time.Now().UTC()}).Error; err != nil {
				return err
			}
			if err := audit(tx, "", task.ID, "acceptance-superseded"); err != nil {
				return err
			}
		}
		return nil
	})
}
