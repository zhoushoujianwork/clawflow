// Package coordinator provides the durable worker task and lease store.
package coordinator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordination"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

const LeaseTTL = 120 * time.Second

var (
	ErrCoordinationChanged = errors.New("coordination context changed")
	ErrNoTask              = errors.New("no eligible task or worker at capacity")
	ErrStaleLease          = errors.New("stale or expired task lease")
	ErrInvalid             = errors.New("invalid coordinator input")
)

type Task struct {
	ResultDigest        string          `json:"-"`
	ApplicationAttempts int             `json:"application_attempts"`
	ApplicationError    string          `json:"application_error,omitempty"`
	EventSource         string          `gorm:"-" json:"-"`
	ExternalID          string          `gorm:"-" json:"-"`
	ID                  string          `gorm:"primaryKey" json:"id"`
	Kind                string          `json:"kind"`
	Repo                string          `json:"repo"`
	Issue               int             `json:"issue"`
	PR                  int             `json:"pr"`
	Operator            string          `json:"operator"`
	Payload             json.RawMessage `gorm:"serializer:rawjson" json:"payload"`
	Tags                []string        `gorm:"serializer:json" json:"tags"`
	Priority            int             `json:"priority"`
	QueuePosition       int64           `json:"queue_position"`
	State               string          `json:"state"`
	Attempt             int             `json:"attempt"`
	LeaseOwner          string          `json:"lease_owner"`
	LeaseExpiresAt      *time.Time      `json:"lease_expires_at"`
	FencingToken        int64           `json:"fencing_token"`
	Result              json.RawMessage `gorm:"serializer:rawjson" json:"result"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

func (Task) TableName() string { return "coordinator_tasks" }

type Worker struct {
	Durable     bool
	Paused      bool
	ID          string   `gorm:"primaryKey"`
	Tags        []string `gorm:"serializer:json"`
	RunUntagged bool
	MaxParallel int
	Roles       []string `gorm:"serializer:json"`
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

type Store struct {
	db *gorm.DB
	// LeaseLimits supplies current platform capacity; nil leaves standalone stores unrestricted.
	LeaseLimits func() (int, map[string]int, error)
}

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
		err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"tags", "run_untagged", "max_parallel", "roles", "backend", "model", "durable", "updated_at"})}).Create(&w).Error
		if err != nil {
			return err
		}
		return audit(tx, w.ID, "", "register")
	})
}

// Enqueue returns the existing active task on a dedupe conflict, without replacing its payload.
func (s *Store) Enqueue(ctx context.Context, input Task) (*Task, error) {
	return s.EnqueuePrepared(ctx, input, nil)
}

// EnqueuePrepared prepares a new payload only after active-task deduplication.
// Every write transaction is BEGIN IMMEDIATE, so producers are serialized across
// coordinator and CLI processes by the database writer lock.
func (s *Store) EnqueuePrepared(ctx context.Context, input Task, prepare func(*Task) error) (*Task, error) {
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
		if input.ExternalID != "" {
			if input.EventSource == "" {
				return ErrInvalid
			}
			var event InboxEvent
			if err := tx.First(&event, "source = ? AND external_id = ?", input.EventSource, input.ExternalID).Error; err == nil {
				t = Task{}
				return tx.First(&t, "id = ?", event.TaskID).Error
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		remember := func() error {
			if input.ExternalID == "" {
				return nil
			}
			now := time.Now().UTC()
			return tx.Create(&InboxEvent{Source: input.EventSource, ExternalID: input.ExternalID, TaskID: t.ID, ReceivedAt: now, ProcessedAt: now}).Error
		}
		q := tx.Where("repo = ? AND kind = ? AND state IN ?", t.Repo, t.Kind, []string{"pending", "leased"})
		if t.PR > 0 {
			q = q.Where("pr = ?", t.PR)
		} else {
			q = q.Where("(pr = 0 OR kind = 'implement') AND issue = ?", t.Issue)
		}
		var existing Task
		if err := q.First(&existing).Error; err == nil {
			t = existing
			return remember()
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if prepare != nil {
			if err := prepare(&t); err != nil {
				return err
			}
		}
		if !json.Valid(t.Payload) {
			return ErrInvalid
		}
		// The writer lock held by this transaction makes this check atomic with
		// enqueue. Two concurrent assessments of an empty queue cannot both
		// enqueue from the same stale snapshot.
		var envelope struct {
			Coordination *coordination.Snapshot `json:"coordination"`
		}
		if err := json.Unmarshal(t.Payload, &envelope); err != nil {
			return ErrInvalid
		}
		if envelope.Coordination != nil {
			s := envelope.Coordination
			if s.Version != coordination.Version || s.Repository != t.Repo || s.Issue.Number != t.Issue || s.Operation != t.Operator {
				return ErrInvalid
			}
			var active []coordination.Task
			if err := tx.Model(&Task{}).Select("id, issue, pr, kind, state, priority").Where("repo = ? AND state IN ?", t.Repo, []string{"pending", "leased"}).Find(&active).Error; err != nil {
				return err
			}
			expected := coordination.Normalize(coordination.Snapshot{Tasks: envelope.Coordination.Tasks})
			actual := coordination.Normalize(coordination.Snapshot{Tasks: active})
			if coordination.Digest(expected) != coordination.Digest(actual) {
				return ErrCoordinationChanged
			}
		}
		r := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&t)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected == 1 {
			if err := remember(); err != nil {
				return err
			}
			return audit(tx, "", t.ID, "enqueue")
		}
		q = tx.Where("repo = ? AND kind = ? AND state IN ?", t.Repo, t.Kind, []string{"pending", "leased"})
		if t.PR > 0 {
			q = q.Where("pr = ?", t.PR)
		} else {
			q = q.Where("(pr = 0 OR kind = 'implement') AND issue = ?", t.Issue)
		}
		t = Task{}
		if err := q.First(&t).Error; err != nil {
			return err
		}
		return remember()
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
	return s.claim(ctx, worker, "")
}
func (s *Store) claim(ctx context.Context, worker, claimID string) (*Task, error) {
	var result Task
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var repoLimits map[string]int
		if s.LeaseLimits != nil {
			// The immediate transaction already serializes platform-wide
			// capacity checks across all workers and API processes.
			total, limits, err := s.LeaseLimits()
			if err != nil {
				return err
			}
			repoLimits = limits
			var leased int64
			if err := tx.Model(&Task{}).Where("state = 'leased'").Count(&leased).Error; err != nil {
				return err
			}
			if total > 0 && leased >= int64(total) {
				return ErrNoTask
			}
		}
		var w Worker
		if err := tx.First(&w, "id = ?", worker).Error; err != nil {
			return err
		}
		if w.Paused {
			return ErrNoTask
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
			q := tx.Where("state = 'pending'").Order("priority ASC, queue_position ASC, created_at ASC, id ASC")
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
			if limit := repoLimits[t.Repo]; limit > 0 {
				var leased int64
				if err := tx.Model(&Task{}).Where("repo = ? AND state = 'leased'", t.Repo).Count(&leased).Error; err != nil {
					return err
				}
				if leased >= int64(limit) {
					skipped = append(skipped, t.ID)
					continue
				}
			}
			if t.Kind == "acceptance" {
				if err := resolveAcceptancePolicy(&t); err != nil {
					return err
				}
			}
			if !eligible(t.Tags, w) || !acceptanceAllowed(t, w) {
				skipped = append(skipped, t.ID)
				continue
			}
			now := controllerNow()
			expires := now.Add(LeaseTTL)
			if w.Durable {
				var payload map[string]json.RawMessage
				if json.Unmarshal(t.Payload, &payload) != nil {
					return ErrInvalid
				}
				var seconds int64
				if json.Unmarshal(payload["timeout_sec"], &seconds) != nil || seconds < 1 || seconds > 86400 {
					return ErrInvalid
				}
				expires = now.Add(time.Duration(seconds)*time.Second + DurableReportGrace)
				payload["durable_execution"] = json.RawMessage("true")
				if claimID != "" {
					payload["worker_claim_id"], _ = json.Marshal(claimID)
				}
				t.Payload, _ = json.Marshal(payload)
			}
			t.State = "leased"
			t.LeaseOwner = worker
			t.LeaseExpiresAt = &expires
			t.FencingToken++
			if err := tx.Save(&t).Error; err != nil {
				return err
			}
			if err := tx.Create(&TaskAttempt{TaskID: t.ID, AttemptNumber: int64(t.Attempt + 1), LeaseEpoch: t.FencingToken, WorkerID: worker, Kind: "product", Status: "running", StartedAt: now}).Error; err != nil {
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
		if err := tx.First(&current, "id = ?", id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrStaleLease
			}
			return err
		}
		// Controller time owns leases: the comparison uses the controller's
		// clock, never a worker-supplied time.
		now := controllerNow()
		query := tx.Model(&Task{}).Where("id = ? AND lease_owner = ? AND fencing_token = ? AND state = 'leased' AND lease_expires_at IS NOT NULL", id, worker, fence)
		if action == "result-accepted" {
			query = query.Where("(lease_expires_at > ? OR json_extract(payload, '$.durable_execution') = 1)", now)
		} else {
			query = query.Where("lease_expires_at > ?", now)
		}
		r := query.Updates(values)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrStaleLease
		}
		if action == "done" || action == "failed" {
			if err := tx.Model(&TaskAttempt{}).Where("task_id = ? AND lease_epoch = ?", id, fence).Updates(map[string]any{"status": action, "ended_at": time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		if action == "result-accepted" {
			if digest, _ := ctx.Value(reportDigestKey{}).(string); digest != "" {
				if err := tx.Model(&Task{}).Where("id = ?", id).Update("result_digest", digest).Error; err != nil {
					return err
				}
				if err := tx.Model(&TaskAttempt{}).Where("task_id = ? AND lease_epoch = ?", id, fence).Update("result_digest", digest).Error; err != nil {
					return err
				}
			}
			if err := acceptIntents(tx, current, values["result"].(json.RawMessage)); err != nil {
				return err
			}
		}
		return audit(tx, worker, id, action)
	})
}
func (s *Store) Heartbeat(ctx context.Context, id, worker string, fence int64) error {
	return s.leaseUpdate(ctx, id, worker, fence, "heartbeat", map[string]interface{}{"lease_expires_at": gorm.Expr("CASE WHEN json_extract(payload, '$.durable_execution') = 1 THEN lease_expires_at ELSE ? END", controllerNow().Add(LeaseTTL))})
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

// MaxInfrastructureAttempts bounds retries independently of product repairs.
const MaxInfrastructureAttempts = 3

func (s *Store) ExpireLeases(ctx context.Context, now time.Time) ([]Task, error) {
	var tasks []Task
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("state = 'leased' AND lease_expires_at <= ?", now.UTC()).Find(&tasks).Error; err != nil {
			return err
		}
		expired := tasks[:0]
		for i := range tasks {
			t := &tasks[i]
			// Durable execution is quarantined, never blindly requeued after a partition.
			if DurableTask(t) {
				continue
			}
			if err := tx.Model(&TaskAttempt{}).Where("task_id = ? AND lease_epoch = ?", t.ID, t.FencingToken).Updates(map[string]any{"kind": "infra", "status": "expired", "error_kind": "lease-expired", "ended_at": now}).Error; err != nil {
				return err
			}
			t.Attempt++
			t.State = "pending"
			t.LeaseOwner = ""
			t.LeaseExpiresAt = nil
			if t.Attempt >= MaxInfrastructureAttempts {
				t.State = "failed"
			}
			if err := tx.Save(t).Error; err != nil {
				return err
			}
			if err := failIntents(tx, t.ID); err != nil {
				return err
			}
			if err := audit(tx, "", t.ID, "expire"); err != nil {
				return err
			}
			expired = append(expired, *t)
		}
		tasks = expired
		return nil
	})
	return tasks, err
}

// Task returns coordinator-owned routing metadata, never worker-supplied identity.
func (s *Store) Task(ctx context.Context, id string) (*Task, error) {
	var t Task
	err := s.db.WithContext(ctx).First(&t, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrStaleLease
	}
	return &t, err
}

func (s *Store) Active(ctx context.Context) ([]Task, error) {
	var tasks []Task
	err := s.db.WithContext(ctx).Where("state IN ?", []string{"pending", "leased"}).Find(&tasks).Error
	return tasks, err
}

// AcceptResult atomically fences out further reports and lease expiry while
// coordinator effects are in flight. The task remains active until FinishResult.
// Effect intents are committed with the report and reconciled before replay.
func (s *Store) AcceptResult(ctx context.Context, id, worker string, fence int64, result json.RawMessage) error {
	if !json.Valid(result) {
		return ErrInvalid
	}
	return s.leaseUpdate(ctx, id, worker, fence, "result-accepted", map[string]interface{}{"result": result, "lease_expires_at": nil})
}
func (s *Store) FinishResult(ctx context.Context, id, worker string, fence int64, failed bool) error {
	state := "done"
	if failed {
		state = "failed"
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r := tx.Model(&Task{}).Where("id = ? AND lease_owner = ? AND fencing_token = ? AND state = 'leased' AND lease_expires_at IS NULL AND result IS NOT NULL", id, worker, fence).Update("state", state)
		if r.Error != nil {
			return r.Error
		}
		if r.RowsAffected != 1 {
			return ErrStaleLease
		}
		return audit(tx, worker, id, "result-applied")
	})
}

// controllerNow is the single clock for lease authority. Worker clocks never
// extend a lease; every comparison and expiry is computed here, in UTC.
var controllerNow = func() time.Time { return time.Now().UTC() }

// rawJSONSerializer stores json.RawMessage as TEXT (never BLOB, which SQLite's
// JSON functions would treat as JSONB) and returns NULL for an empty value so
// "no result yet" stays distinguishable from "{}".
type rawJSONSerializer struct{}

func (rawJSONSerializer) Scan(ctx context.Context, field *schema.Field, dst reflect.Value, dbValue interface{}) error {
	var out json.RawMessage
	switch v := dbValue.(type) {
	case nil:
	case []byte:
		out = append(json.RawMessage(nil), v...)
	case string:
		out = json.RawMessage(v)
	default:
		return fmt.Errorf("unsupported JSON column type %T", dbValue)
	}
	return field.Set(ctx, dst, out)
}

func (rawJSONSerializer) Value(_ context.Context, _ *schema.Field, _ reflect.Value, fieldValue interface{}) (interface{}, error) {
	raw, _ := fieldValue.(json.RawMessage)
	if len(raw) == 0 {
		return nil, nil
	}
	return string(raw), nil
}

func init() { schema.RegisterSerializer("rawjson", rawJSONSerializer{}) }
