package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
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
	// Phases records how long each stage of Store.Board took. It is diagnostic only
	// and never serialized.
	Phases []BoardPhase `json:"-"`
}

// BoardPhase is the elapsed time of one named stage of a board read.
type BoardPhase struct {
	Name     string
	Duration time.Duration
}

// PhaseSummary renders phases as "name=1.2s name2=3ms"; empty when none were recorded.
func (b Board) PhaseSummary() string { return formatPhases(b.Phases) }

func formatPhases(phases []BoardPhase) string {
	parts := make([]string, 0, len(phases))
	for _, p := range phases {
		parts = append(parts, fmt.Sprintf("%s=%s", p.Name, p.Duration.Round(time.Millisecond)))
	}
	return strings.Join(parts, " ")
}

// phaseTrace times consecutive stages; enter closes the running stage and opens the next.
type phaseTrace struct {
	done    []BoardPhase
	current string
	started time.Time
}

func (t *phaseTrace) enter(name string) {
	t.finish()
	t.current, t.started = name, time.Now()
}

func (t *phaseTrace) finish() {
	if t.current == "" {
		return
	}
	t.done = append(t.done, BoardPhase{Name: t.current, Duration: time.Since(t.started)})
	t.current = ""
}

// wrap adds completed and in-progress stages to err. The in-progress stage is
// the one that was running when the error surfaced.
func (t *phaseTrace) wrap(err error) error {
	if err == nil {
		return nil
	}
	running := t.current
	if running == "" {
		running = "none"
	} else {
		running = fmt.Sprintf("%s (running %s)", running, time.Since(t.started).Round(time.Millisecond))
	}
	return fmt.Errorf("%w [board phases done: %s; in progress: %s]", err, formatPhases(t.done), running)
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
	trace := &phaseTrace{}
	limits := dispatchLimits{known: true}
	if s.LeaseLimits != nil {
		trace.enter("lease_limits")
		total, repos, err := s.LeaseLimits()
		limits = dispatchLimits{known: err == nil, total: total, repos: repos}
	}
	// Cached outside the transaction: board reads never wait on VCS.
	orphans := s.cachedOrphanedPRs()
	// The store DSN makes ordinary GORM transactions BEGIN IMMEDIATE. Board is
	// read-only, so use an explicit deferred transaction on one connection: WAL
	// readers get a consistent snapshot without acquiring the writer lock.
	err := deferredReadTransactionTraced(s.db.WithContext(ctx), trace.enter, func(tx *gorm.DB) error {
		now := controllerNow()
		trace.enter("query_workers")
		var workers []Worker
		if err := tx.Select("id, tags, roles, run_untagged, max_parallel, paused").Order("id").Find(&workers).Error; err != nil {
			return err
		}
		trace.enter("query_tokens")
		var tokens []Token
		if err := tx.Select("name, last_seen, revoked").Find(&tokens).Error; err != nil {
			return err
		}
		trace.enter("query_heartbeats")
		// Per-worker subquery uses the (worker, action, created_at) index instead
		// of scanning every heartbeat row. MAX() drops the column type, so the
		// value may arrive as text and is parsed explicitly.
		rows, err := tx.Raw("SELECT w.id AS id, (SELECT MAX(a.created_at) FROM coordinator_audit a WHERE a.worker = w.id AND a.action = 'heartbeat') AS at FROM coordinator_workers w").Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		heartbeats := map[string]time.Time{}
		for rows.Next() {
			var id string
			var raw any
			if err := rows.Scan(&id, &raw); err != nil {
				return err
			}
			at, ok, err := parseHeartbeatTime(raw)
			if err != nil {
				return err
			}
			if ok {
				heartbeats[id] = at
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		trace.enter("query_tasks")
		tasks := []BoardTask{}
		if err := tx.Model(&Task{}).Select("(SELECT started_at FROM task_attempts WHERE task_id = coordinator_tasks.id AND lease_epoch = coordinator_tasks.fencing_token LIMIT 1) AS started_at, (SELECT ended_at FROM task_attempts WHERE task_id = coordinator_tasks.id AND lease_epoch = coordinator_tasks.fencing_token LIMIT 1) AS ended_at, id, COALESCE(json_extract(payload, '$.durable_execution') = 1, 0) AS durable, COALESCE(json_extract(payload, '$.title'), '') AS subject, repo, issue, pr, kind, state, tags, json_object('acceptance_worker', json_extract(payload, '$.acceptance_worker'), 'development_worker', json_extract(payload, '$.development_worker')) AS acceptance_routing, priority, lease_owner, lease_expires_at, last_event_at, attempt, application_attempts, application_error, updated_at").Order("priority, queue_position, created_at, id").Find(&tasks).Error; err != nil {
			return err
		}
		trace.enter("derive_board")
		out = deriveBoard(now, workers, tokens, heartbeats, tasks)
		out.applyFindings(now, workers, limits)
		cfg := s.Watchdog.withDefaults()
		trace.enter("query_history")
		var history []FinishedTask
		if err := tx.Model(&Task{}).Select("repo, issue, pr, state, kind, updated_at, COALESCE(json_extract(result, '$.infra_kind'), '') AS infra_kind, COALESCE(json_extract(result, '$.error'), '') AS err, COALESCE(json_extract(result, '$.head_sha'), '') AS head").
			Where("state IN ('failed', 'done') AND updated_at > ?", now.Add(-cfg.FailureWindow)).Order("updated_at DESC").Scan(&history).Error; err != nil {
			return err
		}
		trace.enter("findings")
		out.Findings = append(out.Findings, repeatedFailureFindings(now, history, cfg)...)
		out.Findings = append(out.Findings, sameHeadCycleFindings(now, history, cfg)...)
		out.Findings = append(out.Findings, orphanedPRFindings(now, orphans, out.Tasks, cfg)...)
		return nil
	})
	err = trace.wrap(err)
	trace.finish()
	out.Phases = trace.done
	return out, err
}

func deferredReadTransaction(db *gorm.DB, read func(*gorm.DB) error) error {
	return deferredReadTransactionTraced(db, func(string) {}, read)
}

// deferredReadTransactionTraced reports stage names to enter as it goes:
// connection acquisition, BEGIN, then (via read) its own stages, then COMMIT.
func deferredReadTransactionTraced(db *gorm.DB, enter func(string), read func(*gorm.DB) error) error {
	enter("acquire_connection")
	return db.Connection(func(conn *gorm.DB) error {
		enter("begin")
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
		enter("commit")
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

// parseHeartbeatTime converts a MAX(created_at) result, which may be a
// time.Time, text or bytes depending on the driver, into a time. A nil value
// (worker without heartbeats) reports ok=false.
func parseHeartbeatTime(v any) (time.Time, bool, error) {
	var raw string
	switch x := v.(type) {
	case nil:
		return time.Time{}, false, nil
	case time.Time:
		return x, true, nil
	case string:
		raw = x
	case []byte:
		raw = string(x)
	default:
		return time.Time{}, false, fmt.Errorf("unsupported heartbeat time type %T", v)
	}
	if raw == "" {
		return time.Time{}, false, nil
	}
	// Strip the monotonic suffix some drivers serialize ("m=+0.1").
	if i := strings.Index(raw, " m="); i >= 0 {
		raw = raw[:i]
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05.999999999-07:00", time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("unparseable heartbeat time %q", raw)
}
