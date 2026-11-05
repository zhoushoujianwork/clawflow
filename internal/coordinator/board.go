package coordinator

import (
	"context"
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
	Findings    []Finding     `json:"findings"`
}
type Throughput struct {
	Since  time.Time `json:"since"`
	Done   int       `json:"done"`
	Failed int       `json:"failed"`
}
type BoardWorker struct {
	Revoked       bool       `json:"revoked"`
	Paused        bool       `json:"paused"`
	MaxParallel   int        `json:"max_parallel"`
	Name          string     `json:"name"`
	Tags          []string   `json:"tags"`
	Roles         []string   `json:"roles"`
	Live          bool       `json:"live"`
	LastSeen      *time.Time `json:"last_seen"`
	LastHeartbeat *time.Time `json:"last_heartbeat"`
	RunningTasks  []string   `json:"running_tasks"`
}
type BoardTask struct {
	StartedAt *time.Time `json:"started_at,omitempty" gorm:"->"`
	EndedAt   *time.Time `json:"ended_at,omitempty" gorm:"->"`
	Subject   string     `json:"subject"`
	Durable   bool       `json:"durable" gorm:"->"`
	// Routing contains only private acceptance routing fields, never the full payload.
	Routing             json.RawMessage `json:"-" gorm:"column:acceptance_routing;serializer:rawjson"`
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
	LastEventAt         *time.Time      `json:"last_event_at,omitempty"`
	Attempt             int             `json:"attempt"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

// Board reads a consistent snapshot without renewing or expiring any lease.
func (s *Store) Board(ctx context.Context) (Board, error) {
	var out Board
	limits := dispatchLimits{known: true}
	if s.LeaseLimits != nil {
		total, repos, err := s.LeaseLimits()
		limits = dispatchLimits{known: err == nil, total: total, repos: repos}
	}
	// The store DSN makes ordinary GORM transactions BEGIN IMMEDIATE. Board is
	// read-only, so use an explicit deferred transaction on one connection: WAL
	// readers get a consistent snapshot without acquiring the writer lock.
	err := deferredReadTransaction(s.db.WithContext(ctx), func(tx *gorm.DB) error {
		now := controllerNow()
		var workers []Worker
		if err := tx.Select("id, tags, roles, run_untagged, max_parallel, paused").Order("id").Find(&workers).Error; err != nil {
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
		if err := tx.Model(&Task{}).Select("(SELECT started_at FROM task_attempts WHERE task_id = coordinator_tasks.id AND lease_epoch = coordinator_tasks.fencing_token LIMIT 1) AS started_at, (SELECT ended_at FROM task_attempts WHERE task_id = coordinator_tasks.id AND lease_epoch = coordinator_tasks.fencing_token LIMIT 1) AS ended_at, id, COALESCE(json_extract(payload, '$.durable_execution') = 1, 0) AS durable, COALESCE(json_extract(payload, '$.title'), '') AS subject, repo, issue, pr, kind, state, tags, json_object('acceptance_worker', json_extract(payload, '$.acceptance_worker'), 'development_worker', json_extract(payload, '$.development_worker')) AS acceptance_routing, priority, lease_owner, lease_expires_at, last_event_at, attempt, application_attempts, application_error, updated_at").Order("priority, queue_position, created_at, id").Find(&tasks).Error; err != nil {
			return err
		}
		out = deriveBoard(now, workers, tokens, heartbeats, tasks)
		out.applyFindings(now, workers, limits)
		cfg := s.Watchdog.withDefaults()
		var history []FinishedTask
		if err := tx.Model(&Task{}).Select("repo, issue, pr, state, updated_at, COALESCE(json_extract(result, '$.infra_kind'), '') AS infra_kind, COALESCE(json_extract(result, '$.error'), '') AS err, COALESCE(json_extract(result, '$.head_sha'), '') AS head").
			Where("state IN ('failed', 'done') AND updated_at > ?", now.Add(-cfg.FailureWindow)).Order("updated_at DESC").Scan(&history).Error; err != nil {
			return err
		}
		out.Findings = append(out.Findings, repeatedFailureFindings(now, history, cfg)...)
		if s.OrphanedPRs != nil {
			// A provider error disables only this rule for the snapshot.
			if prs, err := s.OrphanedPRs(ctx); err == nil {
				out.Findings = append(out.Findings, orphanedPRFindings(now, prs, out.Tasks, cfg)...)
			}
		}
		return nil
	})
	return out, err
}

func deferredReadTransaction(db *gorm.DB, read func(*gorm.DB) error) error {
	return db.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec("BEGIN DEFERRED").Error; err != nil {
			return err
		}
		committed := false
		defer func() {
			if !committed {
			cleanup := conn.Session(&gorm.Session{NewDB: true, Context: context.Background()})
				_ = cleanup.Exec("ROLLBACK").Error
			}
		}()
		if err := read(conn.Session(&gorm.Session{NewDB: true})); err != nil {
			return err
		}
		if err := conn.Exec("COMMIT").Error; err != nil {
			return err
		}
		committed = true
		return nil
	})
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
				if t.Durable {
					t.Stage = "recovery_pending"
					t.WaitingReason = "Execution authority expired. Work remains reserved; reconcile retained worktree/session before recovery."
				}
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
		row := BoardWorker{Name: w.ID, Tags: w.Tags, Roles: w.Roles, LastSeen: token.LastSeen, RunningTasks: running[w.ID], Paused: w.Paused, MaxParallel: w.MaxParallel}
		row.Live = !token.Revoked && token.LastSeen != nil && token.LastSeen.After(now.Add(-LeaseTTL)) && !token.LastSeen.After(now)
		row.Revoked = token.Revoked
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
			if out.Workers[j].Live && !w.Paused && eligible(task.Tags, w) && acceptanceAllowed(Task{Kind: task.Kind, Tags: task.Tags, Payload: task.Routing}, w) {
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
