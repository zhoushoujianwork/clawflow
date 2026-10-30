package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cron"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ScheduledCheck struct {
	ID             string     `json:"id" gorm:"primaryKey"`
	Cron           string     `json:"cron"`
	Timezone       string     `json:"timezone"`
	Prompt         string     `json:"prompt"`
	Ops            int        `json:"ops"`
	TimeoutSeconds int        `json:"timeout_seconds"`
	Enabled        bool       `json:"enabled"`
	Owner          string     `json:"owner"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastRunAt      *time.Time `json:"last_run_at"`
	LastStatus     string     `json:"last_status"`
	LastSummary    string     `json:"last_summary"`
	Failures       int        `json:"failures"`
	LastTick       *time.Time `json:"-" `
}
type ScheduledCheckRun struct {
	Check     *ScheduledCheck `json:"-" gorm:"-"`
	ID        string          `json:"id" gorm:"primaryKey"`
	CheckID   string          `json:"check_id" gorm:"index"`
	StartedAt time.Time       `json:"started_at"`
	EndedAt   *time.Time      `json:"ended_at"`
	Deadline  time.Time       `json:"deadline"`
	Status    string          `json:"status"`
	Summary   string          `json:"summary"`
	Actions   json.RawMessage `json:"actions" gorm:"serializer:json"`
	Usage     json.RawMessage `json:"usage" gorm:"serializer:json"`
}

func migrateSchedules(tx *gorm.DB) error {
	return execAll(tx, `CREATE TABLE scheduled_checks (
 id text PRIMARY KEY, cron text, timezone text, prompt text, ops integer, timeout_seconds integer, enabled boolean, owner text,
 created_at timestamp, updated_at timestamp, last_run_at timestamp, last_status text, last_summary text, failures integer, last_tick timestamp
)`, `CREATE TABLE scheduled_check_runs (
 id text PRIMARY KEY, check_id text, started_at timestamp, ended_at timestamp, deadline timestamp, status text, summary text,
 actions text CHECK (actions IS NULL OR json_valid(actions)), usage text CHECK (usage IS NULL OR json_valid(usage))
)`, `CREATE INDEX idx_scheduled_check_runs_check_id ON scheduled_check_runs (check_id)`)
}

func (j ScheduledCheck) Validate() error {
	if j.ID == "" || strings.TrimSpace(j.Prompt) == "" || strings.TrimSpace(j.Owner) == "" || j.Ops <= 0 || j.TimeoutSeconds <= 0 || j.TimeoutSeconds > 86400 {
		return fmt.Errorf("id, prompt, owner, positive ops and timeout (at most 86400 seconds) required")
	}
	_, err := cron.Parse(j.Cron, j.Timezone)
	return err
}
func (s *Store) SaveScheduledCheck(ctx context.Context, j *ScheduledCheck, create bool) error {
	if err := j.Validate(); err != nil {
		return err
	}
	if create {
		return s.db.WithContext(ctx).Create(j).Error
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var old ScheduledCheck
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&old, "id = ?", j.ID).Error; err != nil {
			return err
		}
		values := map[string]any{"cron": j.Cron, "timezone": j.Timezone, "prompt": j.Prompt, "ops": j.Ops, "timeout_seconds": j.TimeoutSeconds, "enabled": j.Enabled, "updated_at": time.Now().UTC()}
		if j.Enabled && !old.Enabled {
			values["failures"] = 0
		}
		return tx.Model(&old).Updates(values).Error
	})
}
func (s *Store) ScheduledChecks(ctx context.Context) ([]ScheduledCheck, error) {
	rows := []ScheduledCheck{}
	err := s.db.WithContext(ctx).Order("created_at, id").Find(&rows).Error
	return rows, err
}
func (s *Store) ScheduledCheck(ctx context.Context, id string) (ScheduledCheck, error) {
	var j ScheduledCheck
	err := s.db.WithContext(ctx).First(&j, "id = ?", id).Error
	return j, err
}
func (s *Store) RemoveScheduledCheck(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Delete(&ScheduledCheck{}, "id = ?", id).Error
}
func (s *Store) ScheduledCheckRuns(ctx context.Context, id string) ([]ScheduledCheckRun, error) {
	rows := []ScheduledCheckRun{}
	q := s.db.WithContext(ctx).Order("started_at desc").Limit(50)
	if id != "" {
		q = q.Where("check_id = ?", id)
	}
	err := q.Find(&rows).Error
	return rows, err
}

// ClaimScheduledChecks evaluates only the current minute; old slots are never replayed.
// Row locks serialize multiple scheduler instances, including overlaps and completion.
func (s *Store) ClaimScheduledChecks(ctx context.Context, now time.Time) ([]ScheduledCheckRun, error) {
	claimed := []ScheduledCheckRun{}
	minute := now.UTC().Truncate(time.Minute)
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var jobs []ScheduledCheck
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("enabled = ?", true).Order("id").Find(&jobs).Error; err != nil {
			return err
		}
		for _, j := range jobs {
			if j.LastTick != nil && !j.LastTick.Before(minute) {
				continue
			}
			schedule, err := cron.Parse(j.Cron, j.Timezone)
			if err != nil {
				return err
			}
			if !schedule.Matches(minute) {
				continue
			}
			// A process lost during a run is failed after its enforced invocation deadline.
			var active []ScheduledCheckRun
			if err := tx.Where("check_id = ? AND status = ?", j.ID, "running").Find(&active).Error; err != nil {
				return err
			}
			busy := false
			for _, r := range active {
				if now.Before(r.Deadline) {
					busy = true
					continue
				}
				end := now
				if err := tx.Model(&r).Updates(map[string]any{"status": "failed", "summary": "invocation deadline expired", "ended_at": &end}).Error; err != nil {
					return err
				}
				j.Failures++
			}
			status := "running"
			if busy {
				status = "skipped"
			}
			run := ScheduledCheckRun{ID: fmt.Sprintf("%s-%d", j.ID, minute.Unix()), CheckID: j.ID, StartedAt: now, Deadline: now.Add(time.Duration(j.TimeoutSeconds) * time.Second)}
			if j.Failures >= 3 {
				status = "skipped"
				j.Enabled = false
			}
			run.Status = status
			if status != "running" {
				run.EndedAt = &now
				run.Summary = "previous invocation still running"
				if !j.Enabled {
					run.Summary = "disabled after three consecutive failures"
				}
			}
			if err := tx.Create(&run).Error; err != nil {
				return err
			}
			updates := map[string]any{"last_tick": minute, "last_run_at": now, "last_status": status, "last_summary": run.Summary, "failures": j.Failures, "enabled": j.Enabled}
			if err := tx.Model(&j).Updates(updates).Error; err != nil {
				return err
			}
			if status == "running" {
				run.Check = &j
				claimed = append(claimed, run)
			}
		}
		return nil
	})
	return claimed, err
}

func (s *Store) FinishScheduledCheck(ctx context.Context, id, status, summary string, actions, usage json.RawMessage) error {
	if status != "success" && status != "failed" {
		return fmt.Errorf("invalid terminal status")
	}
	summary = string([]rune(summary)[:min(len([]rune(summary)), 500)])
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var run ScheduledCheckRun
		if err := tx.First(&run, "id = ?", id).Error; err != nil {
			return err
		}
		var job ScheduledCheck
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&job, "id = ?", run.CheckID).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		// A deleted job retains its run history.
		result := tx.Model(&ScheduledCheckRun{}).Where("id = ? AND status = ?", id, "running").Updates(map[string]any{"status": status, "summary": summary, "actions": jsonText(actions), "usage": jsonText(usage), "ended_at": time.Now().UTC()})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 || err == gorm.ErrRecordNotFound {
			return nil
		}
		failures := 0
		if status == "failed" {
			failures = job.Failures + 1
		}
		values := map[string]any{"last_status": status, "last_summary": summary, "failures": failures}
		if failures >= 3 {
			values["enabled"] = false
		}
		return tx.Model(&job).Updates(values).Error
	})
}
