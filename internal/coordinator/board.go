package coordinator

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Board contains only public scheduling metadata, never payloads, results or credentials.
type Board struct {
	GeneratedAt time.Time     `json:"generated_at"`
	Workers     []BoardWorker `json:"workers"`
	Tasks       []BoardTask   `json:"tasks"`
	Throughput  Throughput    `json:"throughput"`
}
type Throughput struct {
	Since  time.Time `json:"since"`
	Done   int       `json:"done"`
	Failed int       `json:"failed"`
}
type BoardWorker struct {
	Name          string     `json:"name"`
	Tags          []string   `json:"tags"`
	Roles         []string   `json:"roles"`
	Live          bool       `json:"live"`
	LastSeen      *time.Time `json:"last_seen"`
	LastHeartbeat *time.Time `json:"last_heartbeat"`
	RunningTasks  []string   `json:"running_tasks"`
}
type BoardTask struct {
	// Routing contains only private acceptance routing fields, never the full payload.
	Routing             json.RawMessage `json:"-" gorm:"column:acceptance_routing;type:jsonb"`
	ApplicationAttempts int             `json:"application_attempts"`
	ApplicationError    string          `json:"application_error,omitempty"`
	WaitingReason       string          `json:"waiting_reason,omitempty" gorm:"-"`
	ID                  string          `json:"id"`
	Repo                string          `json:"repo"`
	Issue               int             `json:"issue"`
	PR                  int             `json:"pr"`
	Kind                string          `json:"kind"`
	State               string          `json:"state"`
	Stage               string          `json:"stage" gorm:"-"`
	Tags                []string        `json:"tags" gorm:"serializer:json"`
	Priority            int             `json:"priority"`
	Worker              string          `json:"worker" gorm:"column:lease_owner"`
	LeaseExpiresAt      *time.Time      `json:"lease_expires_at"`
	Attempt             int             `json:"attempt"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

// Board reads a consistent snapshot without renewing or expiring any lease.
func (s *Store) Board(ctx context.Context) (Board, error) {
	var out Board
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var now time.Time
		if err := tx.Raw("SELECT transaction_timestamp()").Scan(&now).Error; err != nil {
			return err
		}
		var workers []Worker
		if err := tx.Select("id, tags, roles, run_untagged").Order("id").Find(&workers).Error; err != nil {
			return err
		}
		var tokens []Token
		if err := tx.Select("name, last_seen, revoked").Find(&tokens).Error; err != nil {
			return err
		}
		var beats []struct {
			Worker string
			At     time.Time
		}
		if err := tx.Model(&Audit{}).Select("worker, MAX(created_at) AS at").Where("action = ?", "heartbeat").Group("worker").Scan(&beats).Error; err != nil {
			return err
		}
		heartbeats := map[string]time.Time{}
		for _, beat := range beats {
			heartbeats[beat.Worker] = beat.At
		}
		tasks := []BoardTask{}
		if err := tx.Model(&Task{}).Select("id, repo, issue, pr, kind, state, tags, jsonb_build_object('acceptance_worker', payload->'acceptance_worker', 'development_worker', payload->'development_worker') AS acceptance_routing, priority, lease_owner, lease_expires_at, attempt, application_attempts, application_error, updated_at").Order("priority, queue_position, created_at, id").Find(&tasks).Error; err != nil {
			return err
		}
		out = deriveBoard(now, workers, tokens, heartbeats, tasks)
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return out, err
}

func deriveBoard(now time.Time, workers []Worker, tokens []Token, heartbeats map[string]time.Time, tasks []BoardTask) Board {
	out := Board{GeneratedAt: now, Workers: []BoardWorker{}, Tasks: tasks, Throughput: Throughput{Since: now.Add(-time.Hour)}}
	if out.Tasks == nil {
		out.Tasks = []BoardTask{}
	}
	seen := map[string]Token{}
	for _, token := range tokens {
		seen[token.Name] = token
	}
	running := map[string][]string{}
	for i := range out.Tasks {
		t := &out.Tasks[i]
		if t.Tags == nil {
			t.Tags = []string{}
		}
		t.Stage = t.State
		if t.State == "leased" {
			switch {
			case t.LeaseExpiresAt == nil:
				t.Stage = "finalizing"
			case !t.LeaseExpiresAt.After(now):
				t.Stage = "lease_expired"
			default:
				t.Stage = "running"
				running[t.Worker] = append(running[t.Worker], t.ID)
			}
		}
		if t.UpdatedAt.After(out.Throughput.Since) && !t.UpdatedAt.After(now) {
			switch t.State {
			case "done":
				out.Throughput.Done++
			case "failed":
				out.Throughput.Failed++
			}
		}
	}
	for _, w := range workers {
		token := seen[w.ID]
		row := BoardWorker{Name: w.ID, Tags: w.Tags, Roles: w.Roles, LastSeen: token.LastSeen, RunningTasks: running[w.ID]}
		row.Live = !token.Revoked && token.LastSeen != nil && token.LastSeen.After(now.Add(-LeaseTTL)) && !token.LastSeen.After(now)
		if beat, ok := heartbeats[w.ID]; ok {
			row.LastHeartbeat = &beat
		}
		if row.Tags == nil {
			row.Tags = []string{}
		}
		if row.Roles == nil {
			row.Roles = []string{}
		}
		if row.RunningTasks == nil {
			row.RunningTasks = []string{}
		}
		out.Workers = append(out.Workers, row)
	}
	for i := range out.Tasks {
		task := &out.Tasks[i]
		if task.State != "pending" || (task.Kind != "acceptance" && len(task.Tags) == 0) {
			continue
		}
		matched := false
		for j, w := range workers {
			if out.Workers[j].Live && eligible(task.Tags, w) && acceptanceAllowed(Task{Kind: task.Kind, Tags: task.Tags, Payload: task.Routing}, w) {
				matched = true
				break
			}
		}
		if !matched {
			if task.Kind == "acceptance" {
				task.WaitingReason = "waiting for an independent reviewer"
			} else {
				task.WaitingReason = "waiting for a worker with tags " + strings.Join(task.Tags, ", ")
			}
		}
	}
	return out
}
