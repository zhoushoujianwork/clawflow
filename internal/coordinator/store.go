// Package coordinator provides the durable worker task and lease store.
package coordinator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const LeaseTTL = 120 * time.Second

var (
	ErrNoTask     = errors.New("no eligible task or worker at capacity")
	ErrStaleLease = errors.New("stale or expired task lease")
	ErrInvalid    = errors.New("invalid coordinator input")
)

type Task struct {
	ID             string `gorm:"primaryKey"`
	Kind           string
	Repo           string
	Issue          int
	PR             int
	Operator       string
	Payload        json.RawMessage `gorm:"type:jsonb"`
	Tags           []string        `gorm:"serializer:json;type:jsonb"`
	Priority       int
	QueuePosition  int64
	State          string
	Attempt        int
	LeaseOwner     string
	LeaseExpiresAt *time.Time
	FencingToken   int64
	Result         json.RawMessage `gorm:"type:jsonb"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (Task) TableName() string { return "coordinator_tasks" }

type Worker struct {
	ID          string   `gorm:"primaryKey"`
	Tags        []string `gorm:"serializer:json;type:jsonb"`
	RunUntagged bool
	MaxParallel int
	Roles       []string `gorm:"serializer:json;type:jsonb"`
	Backend     string
	Model       string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (Worker) TableName() string { return "coordinator_workers" }

type Audit struct {
	ID        int64 `gorm:"primaryKey"`
	Worker    string
	Task      string
	Action    string
	CreatedAt time.Time
}

func (Audit) TableName() string { return "coordinator_audit" }

type Store struct{ db *gorm.DB }

func New(db *gorm.DB) *Store { return &Store{db: db} }

func randomID() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func audit(tx *gorm.DB, worker, task, action string) error {
	return tx.Create(&Audit{Worker: worker, Task: task, Action: action, CreatedAt: time.Now().UTC()}).Error
}
func (s *Store) AppendAudit(ctx context.Context, worker, task, action string) error {
	return audit(s.db.WithContext(ctx), worker, task, action)
}
func (s *Store) RegisterWorker(ctx context.Context, w Worker) error {
	if w.ID == "" || w.MaxParallel < 1 {
		return ErrInvalid
	}
	if w.Tags == nil {
		w.Tags = []string{}
	}
	if w.Roles == nil {
		w.Roles = []string{}
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"tags", "run_untagged", "max_parallel", "roles", "backend", "model", "updated_at"})}).Create(&w).Error
		if err != nil {
			return err
		}
		return audit(tx, w.ID, "", "register")
	})
}

// Enqueue returns the existing active task on a dedupe conflict, without replacing its payload.
func (s *Store) Enqueue(ctx context.Context, input Task) (*Task, error) {
	if input.Repo == "" || input.Issue < 0 || input.PR < 0 || (input.Issue == 0 && input.PR == 0) || input.Operator == "" {
		return nil, ErrInvalid
	}
	switch input.Kind {
	case "implement", "repair", "acceptance", "reply":
	default:
		return nil, ErrInvalid
	}
	if len(input.Payload) == 0 {
		input.Payload = json.RawMessage("{}")
	}
	if !json.Valid(input.Payload) {
		return nil, ErrInvalid
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	t := Task{ID: id, Kind: input.Kind, Repo: input.Repo, Issue: input.Issue, PR: input.PR, Operator: input.Operator, Payload: input.Payload, Tags: input.Tags, Priority: input.Priority, QueuePosition: input.QueuePosition, State: "pending"}
	if t.Tags == nil {
		t.Tags = []string{}
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&t)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected == 1 {
			return audit(tx, "", t.ID, "enqueue")
		}
		q := tx.Where("repo = ? AND kind = ? AND state IN ?", t.Repo, t.Kind, []string{"pending", "leased"})
		if t.PR > 0 {
			q = q.Where("pr = ?", t.PR)
		} else {
			q = q.Where("pr = 0 AND issue = ?", t.Issue)
		}
		t = Task{}
		return q.First(&t).Error
	})
	return &t, err
}

func eligible(tags []string, w Worker) bool {
	if len(tags) == 0 {
		return w.RunUntagged
	}
	have := map[string]bool{}
	for _, tag := range w.Tags {
		have[tag] = true
	}
	for _, tag := range tags {
		if !have[tag] {
			return false
		}
	}
	return true
}

// Claim serializes capacity checks on the worker row, and skips tasks locked by other claimers.
func (s *Store) Claim(ctx context.Context, worker string) (*Task, error) {
	var result Task
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var w Worker
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&w, "id = ?", worker).Error; err != nil {
			return err
		}
		var n int64
		if err := tx.Model(&Task{}).Where("state = 'leased' AND lease_owner = ?", worker).Count(&n).Error; err != nil {
			return err
		}
		if n >= int64(w.MaxParallel) {
			return ErrNoTask
		}
		// Walk one row at a time so unrelated eligible tasks are not all locked by one claimant.
		var skipped []string
		for {
			q := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("state = 'pending'").Order("priority ASC, queue_position ASC, created_at ASC, id ASC")
			if len(skipped) > 0 {
				q = q.Where("id NOT IN ?", skipped)
			}
			var t Task
			if err := q.First(&t).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrNoTask
				}
				return err
			}
			if !eligible(t.Tags, w) {
				skipped = append(skipped, t.ID)
				continue
			}
			var now time.Time
			if err := tx.Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
				return err
			}
			expires := now.Add(LeaseTTL)
			t.State = "leased"
			t.LeaseOwner = worker
			t.LeaseExpiresAt = &expires
			t.FencingToken++
			if err := tx.Save(&t).Error; err != nil {
				return err
			}
			result = t
			return audit(tx, worker, t.ID, "claim")
		}
	})
	return &result, err
}

func (s *Store) leaseUpdate(ctx context.Context, id, worker string, fence int64, action string, values map[string]interface{}) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock first so expiry is checked after any wait, even if the other
		// transaction only locked the row without modifying it.
		var current Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrStaleLease
			}
			return err
		}
		r := tx.Model(&Task{}).Where("id = ? AND lease_owner = ? AND fencing_token = ? AND state = 'leased' AND lease_expires_at > clock_timestamp()", id, worker, fence).Updates(values)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrStaleLease
		}
		return audit(tx, worker, id, action)
	})
}
func (s *Store) Heartbeat(ctx context.Context, id, worker string, fence int64) error {
	return s.leaseUpdate(ctx, id, worker, fence, "heartbeat", map[string]interface{}{"lease_expires_at": gorm.Expr("clock_timestamp() + interval '120 seconds'")})
}
func (s *Store) finish(ctx context.Context, id, worker string, fence int64, state string, result json.RawMessage) error {
	if len(result) == 0 {
		result = json.RawMessage("{}")
	}
	if !json.Valid(result) {
		return ErrInvalid
	}
	return s.leaseUpdate(ctx, id, worker, fence, state, map[string]interface{}{"state": state, "result": result, "lease_expires_at": nil})
}
func (s *Store) Complete(ctx context.Context, id, worker string, fence int64, result json.RawMessage) error {
	return s.finish(ctx, id, worker, fence, "done", result)
}
func (s *Store) Fail(ctx context.Context, id, worker string, fence int64, result json.RawMessage) error {
	return s.finish(ctx, id, worker, fence, "failed", result)
}
func (s *Store) ExpireLeases(ctx context.Context, now time.Time) ([]Task, error) {
	var tasks []Task
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r := tx.Model(&tasks).Clauses(clause.Returning{}).Where("state = 'leased' AND lease_expires_at <= ?", now).Updates(map[string]interface{}{"state": "pending", "attempt": gorm.Expr("attempt + 1"), "lease_owner": "", "lease_expires_at": nil})
		if r.Error != nil {
			return r.Error
		}
		for _, t := range tasks {
			if err := audit(tx, "", t.ID, "expire"); err != nil {
				return err
			}
		}
		return nil
	})
	return tasks, err
}
