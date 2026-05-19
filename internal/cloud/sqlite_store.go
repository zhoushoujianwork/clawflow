package cloud

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// compile-time check
var _ Store = (*SQLiteStore)(nil)

// SQLiteStore is a persistent Store implementation backed by a SQLite database.
// Use NewSQLiteStore to open or create a database at the given path.
// Use ":memory:" for an ephemeral in-process database suitable for tests.
//
// The driver used is modernc.org/sqlite (pure Go, no CGO required).
type SQLiteStore struct {
	// leaseMu serialises the read-then-write sequence inside Lease so that
	// two concurrent goroutines cannot both find the same pending job eligible
	// and both attempt to lease it. The SQL transaction inside Lease provides
	// DB-level atomicity; leaseMu prevents the interleaving at the Go level.
	leaseMu sync.Mutex
	db      *sql.DB
}

// NewSQLiteStore opens (or creates) a SQLite database at path and applies any
// pending schema migrations before returning. Use ":memory:" for tests.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	// Single connection keeps WAL ordering predictable and avoids
	// "database is locked" errors under concurrent goroutines.
	db.SetMaxOpenConns(1)
	// Best-effort pragmas; ignore errors (e.g. WAL unsupported on :memory:).
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA foreign_keys=ON")

	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite migrate: %w", err)
	}
	return s, nil
}

// Close releases the underlying database connection.
func (s *SQLiteStore) Close() error { return s.db.Close() }

// migrate applies any pending numbered *.sql files from the embedded
// migrations directory. It is idempotent: already-applied versions are skipped.
func (s *SQLiteStore) migrate() error {
	if _, err := s.db.Exec(
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY)`,
	); err != nil {
		return err
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		var version int
		if _, err := fmt.Sscanf(name, "%04d", &version); err != nil {
			continue
		}

		var count int
		if err := s.db.QueryRow(
			"SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version,
		).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue // already applied
		}

		data, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(string(data)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := s.db.Exec(
			"INSERT INTO schema_migrations (version) VALUES (?)", version,
		); err != nil {
			return err
		}
	}
	return nil
}

// RegisterWorker creates a new machine + worker row and returns their IDs and
// the worker token.
func (s *SQLiteStore) RegisterWorker(req RegisterWorkerRequest) (RegisterWorkerResponse, error) {
	if req.Hostname == "" {
		return RegisterWorkerResponse{}, fmt.Errorf("hostname is required")
	}
	now := sqliteTime(time.Now().UTC())
	capsJSON, _ := json.Marshal(req.Capabilities)

	machineID := newID("machine")
	if _, err := s.db.Exec(
		`INSERT INTO machines (id, hostname, display_name, version, capabilities_json, last_seen_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		machineID, req.Hostname, req.DisplayName, req.Version, string(capsJSON), now,
	); err != nil {
		return RegisterWorkerResponse{}, fmt.Errorf("insert machine: %w", err)
	}

	workerID := newID("worker")
	token := newID("wtoken")
	if _, err := s.db.Exec(
		`INSERT INTO workers (id, machine_id, token, status, capacity, active_runs_json, last_seen_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		workerID, machineID, token, WorkerStatusOnline, 1, "[]", now,
	); err != nil {
		return RegisterWorkerResponse{}, fmt.Errorf("insert worker: %w", err)
	}

	return RegisterWorkerResponse{
		MachineID:   machineID,
		WorkerID:    workerID,
		WorkerToken: token,
	}, nil
}

// Heartbeat updates the worker's status, capacity, and active-run list.
func (s *SQLiteStore) Heartbeat(req HeartbeatRequest) (HeartbeatResponse, error) {
	now := time.Now().UTC()
	nowStr := sqliteTime(now)

	var machineID string
	err := s.db.QueryRow(
		"SELECT machine_id FROM workers WHERE id = ?", req.WorkerID,
	).Scan(&machineID)
	if err == sql.ErrNoRows || machineID != req.MachineID {
		return HeartbeatResponse{}, fmt.Errorf("worker not registered")
	}
	if err != nil {
		return HeartbeatResponse{}, err
	}

	status := req.Status
	if status == "" {
		status = WorkerStatusOnline
	}
	activeRunsJSON, _ := json.Marshal(req.ActiveRun)

	if _, err := s.db.Exec(
		`UPDATE workers SET status = ?, capacity = ?, active_runs_json = ?, last_seen_at = ? WHERE id = ?`,
		status, req.Capacity, string(activeRunsJSON), nowStr, req.WorkerID,
	); err != nil {
		return HeartbeatResponse{}, err
	}
	// Best-effort machine touch.
	s.db.Exec("UPDATE machines SET last_seen_at = ? WHERE id = ?", nowStr, req.MachineID)

	return HeartbeatResponse{ServerTime: now}, nil
}

// EnqueueJob inserts a new job unless an existing job with the same DedupeKey
// is already in the store (in which case it returns that job unchanged).
func (s *SQLiteStore) EnqueueJob(spec JobSpec, boundMachineID string) (*JobRecord, error) {
	if spec.Repo == "" || spec.Operator == "" || spec.Number == 0 {
		return nil, fmt.Errorf("job spec requires repo, operator, and number")
	}
	now := sqliteTime(time.Now().UTC())

	if spec.DedupeKey != "" {
		var existingID string
		err := s.db.QueryRow(
			"SELECT id FROM jobs WHERE dedupe_key = ?", spec.DedupeKey,
		).Scan(&existingID)
		if err == nil {
			return s.GetJob(existingID), nil
		} else if err != sql.ErrNoRows {
			return nil, err
		}
	}

	spec.JobID = newID("job")
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}

	if _, err := s.db.Exec(
		`INSERT INTO jobs
		 (id, spec_json, status, bound_machine_id, lease_worker_id, attempt_count, dedupe_key, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		spec.JobID, string(specJSON), JobStatusPending,
		boundMachineID, "", 0, spec.DedupeKey, now, now,
	); err != nil {
		return nil, fmt.Errorf("insert job: %w", err)
	}

	return s.GetJob(spec.JobID), nil
}

// Lease atomically:
//  1. Expires any stale leases whose lease_expires_at has passed.
//  2. Finds the oldest pending job eligible for this machine (matching bound
//     machine and platform capabilities).
//  3. Marks the job as leased, creates a run row, and returns the JobSpec with
//     the assigned RunID.
//
// Returns nil, nil when no eligible job is available.
func (s *SQLiteStore) Lease(req LeaseRequest, leaseFor time.Duration) (*JobSpec, error) {
	if leaseFor <= 0 {
		leaseFor = DefaultLeaseDuration
	}
	now := time.Now().UTC()
	nowStr := sqliteTime(now)

	s.leaseMu.Lock()
	defer s.leaseMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}

	// Verify the requesting worker belongs to the claimed machine.
	var machineID string
	err = tx.QueryRow(
		"SELECT machine_id FROM workers WHERE id = ?", req.WorkerID,
	).Scan(&machineID)
	if err == sql.ErrNoRows || machineID != req.MachineID {
		tx.Rollback()
		return nil, fmt.Errorf("worker not registered")
	}
	if err != nil {
		tx.Rollback()
		return nil, err
	}

	// Expire stale leases — reset them to pending so they can be re-leased.
	if _, err = tx.Exec(
		`UPDATE jobs
		 SET status = ?, lease_worker_id = '', lease_expires_at = NULL, updated_at = ?
		 WHERE status = ? AND lease_expires_at IS NOT NULL AND lease_expires_at < ?`,
		JobStatusPending, nowStr, JobStatusLeased, nowStr,
	); err != nil {
		tx.Rollback()
		return nil, err
	}

	// Fetch pending jobs eligible for this machine ordered by FIFO.
	rows, err := tx.Query(
		`SELECT id, spec_json FROM jobs
		 WHERE status = ? AND (bound_machine_id = '' OR bound_machine_id = ?)
		 ORDER BY created_at ASC`,
		JobStatusPending, req.MachineID,
	)
	if err != nil {
		tx.Rollback()
		return nil, err
	}

	type cand struct {
		id   string
		spec JobSpec
	}
	var chosen *cand
	for rows.Next() {
		var id, specJSON string
		if err = rows.Scan(&id, &specJSON); err != nil {
			rows.Close()
			tx.Rollback()
			return nil, err
		}
		var spec JobSpec
		if err = json.Unmarshal([]byte(specJSON), &spec); err != nil {
			rows.Close()
			tx.Rollback()
			return nil, err
		}
		if !supportsPlatform(req.Capabilities, spec.Platform) {
			continue
		}
		chosen = &cand{id: id, spec: spec}
		break
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		tx.Rollback()
		return nil, err
	}

	if chosen == nil {
		tx.Rollback()
		return nil, nil
	}

	runID := newID("run")
	leaseExpStr := sqliteTime(now.Add(leaseFor))

	if _, err = tx.Exec(
		`UPDATE jobs
		 SET status = ?, lease_worker_id = ?, lease_expires_at = ?,
		     attempt_count = attempt_count + 1, updated_at = ?
		 WHERE id = ?`,
		JobStatusLeased, req.WorkerID, leaseExpStr, nowStr, chosen.id,
	); err != nil {
		tx.Rollback()
		return nil, err
	}

	// Persist the RunID into the stored spec so GetJob returns it.
	chosen.spec.RunID = runID
	specJSON, _ := json.Marshal(chosen.spec)
	if _, err = tx.Exec(
		`UPDATE jobs SET spec_json = ? WHERE id = ?`, string(specJSON), chosen.id,
	); err != nil {
		tx.Rollback()
		return nil, err
	}

	if _, err = tx.Exec(
		`INSERT INTO runs (id, job_id, status, started_at) VALUES (?, ?, ?, ?)`,
		runID, chosen.id, JobStatusRunning, nowStr,
	); err != nil {
		tx.Rollback()
		return nil, err
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}

	spec := chosen.spec
	return &spec, nil
}

// AppendRunEvents appends streaming events to an existing run.
func (s *SQLiteStore) AppendRunEvents(runID string, events []RunEvent) error {
	var count int
	if err := s.db.QueryRow(
		"SELECT COUNT(*) FROM runs WHERE id = ?", runID,
	).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("run not found")
	}
	for _, ev := range events {
		fieldsJSON, _ := json.Marshal(ev.Fields)
		if _, err := s.db.Exec(
			`INSERT INTO run_events (run_id, time, level, message, fields_json) VALUES (?, ?, ?, ?, ?)`,
			runID, sqliteTime(ev.Time.UTC()), ev.Level, ev.Message, string(fieldsJSON),
		); err != nil {
			return fmt.Errorf("insert run event: %w", err)
		}
	}
	return nil
}

// FinishRun marks a run as completed (succeeded, failed, etc.) and updates the
// parent job status accordingly. If req.Usage is non-nil it is also
// persisted to run_usage with denormalized repo/operator from the job spec
// so the aggregation endpoint (sub 4) can GROUP BY without joins.
func (s *SQLiteStore) FinishRun(runID string, req FinishRunRequest) error {
	nowT := time.Now().UTC()
	now := sqliteTime(nowT)
	status := req.Status
	if status == "" {
		status = JobStatusFailed
	}

	var jobID string
	if err := s.db.QueryRow(
		"SELECT job_id FROM runs WHERE id = ?", runID,
	).Scan(&jobID); err == sql.ErrNoRows {
		return fmt.Errorf("run not found")
	} else if err != nil {
		return err
	}

	if _, err := s.db.Exec(
		`UPDATE runs SET status = ?, outcome = ?, summary = ?, error = ?, ended_at = ? WHERE id = ?`,
		status, req.Outcome, req.Summary, req.Error, now, runID,
	); err != nil {
		return err
	}

	if _, err := s.db.Exec(
		`UPDATE jobs SET status = ?, lease_worker_id = '', lease_expires_at = NULL, updated_at = ? WHERE id = ?`,
		status, now, jobID,
	); err != nil {
		return err
	}

	if req.Usage != nil {
		var (
			specJSON         string
			repo, operator   string
			boundMachineID   string
		)
		if err := s.db.QueryRow(
			`SELECT spec_json, bound_machine_id FROM jobs WHERE id = ?`, jobID,
		).Scan(&specJSON, &boundMachineID); err == nil {
			var spec JobSpec
			if json.Unmarshal([]byte(specJSON), &spec) == nil {
				repo = spec.Repo
				operator = spec.Operator
			}
		}
		if err := s.upsertRunUsage(runID, repo, operator, boundMachineID, nowT, req.Usage); err != nil {
			return err
		}
	}

	return nil
}

// upsertRunUsage inserts or replaces a run_usage row. Idempotent on
// run_id; a worker retry overwrites with the (presumably more complete)
// later upload. user_id is left NULL — derived from machine→owner in a
// follow-up PR once machines carry an owner column.
func (s *SQLiteStore) upsertRunUsage(runID, repo, operator, machineID string, endedAt time.Time, u *Usage) error {
	modelJSON, err := json.Marshal(u.ModelUsage)
	if err != nil {
		modelJSON = []byte("{}")
	}
	_, err = s.db.Exec(
		`INSERT INTO run_usage (
			run_id, machine_id, repo, operator,
			duration_ms, num_turns, total_cost_usd,
			input_tokens, output_tokens,
			cache_read_input_tokens, cache_creation_input_tokens,
			model_usage_json, ended_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET
			machine_id = excluded.machine_id,
			repo = excluded.repo,
			operator = excluded.operator,
			duration_ms = excluded.duration_ms,
			num_turns = excluded.num_turns,
			total_cost_usd = excluded.total_cost_usd,
			input_tokens = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			cache_read_input_tokens = excluded.cache_read_input_tokens,
			cache_creation_input_tokens = excluded.cache_creation_input_tokens,
			model_usage_json = excluded.model_usage_json,
			ended_at = excluded.ended_at`,
		runID, machineID, repo, operator,
		u.DurationMs, u.NumTurns, u.TotalCostUSD,
		u.InputTokens, u.OutputTokens,
		u.CacheReadInputTokens, u.CacheCreationInputTokens,
		string(modelJSON), sqliteTime(endedAt),
	)
	return err
}

// AddChatUsage persists one chat session's token / cost breakdown.
// Idempotent on session_id.
func (s *SQLiteStore) AddChatUsage(in AddChatUsageInput) error {
	if in.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	if in.Usage == nil {
		return fmt.Errorf("usage is required")
	}
	if in.UserID == "" {
		return fmt.Errorf("user_id is required")
	}
	endedAt := in.EndedAt
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	}
	modelJSON, err := json.Marshal(in.Usage.ModelUsage)
	if err != nil {
		modelJSON = []byte("{}")
	}
	_, err = s.db.Exec(
		`INSERT INTO chat_usage (
			session_id, user_id, machine_id, repo,
			duration_ms, num_turns, total_cost_usd,
			input_tokens, output_tokens,
			cache_read_input_tokens, cache_creation_input_tokens,
			model_usage_json, ended_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			user_id = excluded.user_id,
			machine_id = excluded.machine_id,
			repo = excluded.repo,
			duration_ms = excluded.duration_ms,
			num_turns = excluded.num_turns,
			total_cost_usd = excluded.total_cost_usd,
			input_tokens = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			cache_read_input_tokens = excluded.cache_read_input_tokens,
			cache_creation_input_tokens = excluded.cache_creation_input_tokens,
			model_usage_json = excluded.model_usage_json,
			ended_at = excluded.ended_at`,
		in.SessionID, in.UserID, in.MachineID, in.Repo,
		in.Usage.DurationMs, in.Usage.NumTurns, in.Usage.TotalCostUSD,
		in.Usage.InputTokens, in.Usage.OutputTokens,
		in.Usage.CacheReadInputTokens, in.Usage.CacheCreationInputTokens,
		string(modelJSON), sqliteTime(endedAt),
	)
	return err
}

// GetRunUsage returns the run_usage row for a given run_id, or nil.
func (s *SQLiteStore) GetRunUsage(runID string) *UsageRecord {
	return s.queryUsage(true, runID)
}

// GetChatUsage returns the chat_usage row for a given session_id, or nil.
func (s *SQLiteStore) GetChatUsage(sessionID string) *UsageRecord {
	return s.queryUsage(false, sessionID)
}

func (s *SQLiteStore) queryUsage(isRun bool, id string) *UsageRecord {
	var (
		userID, machineID, repo, operator string
		modelJSON, endedAt                string
		durationMs                        int64
		numTurns                          int
		totalCost                         float64
		inTokens, outTokens               int64
		cacheRead, cacheCreate            int64
		row                               *sql.Row
	)
	if isRun {
		row = s.db.QueryRow(
			`SELECT COALESCE(user_id,''), machine_id, repo, operator,
			        duration_ms, num_turns, total_cost_usd,
			        input_tokens, output_tokens,
			        cache_read_input_tokens, cache_creation_input_tokens,
			        model_usage_json, ended_at
			 FROM run_usage WHERE run_id = ?`, id)
	} else {
		row = s.db.QueryRow(
			`SELECT user_id, machine_id, repo, '' AS operator,
			        duration_ms, num_turns, total_cost_usd,
			        input_tokens, output_tokens,
			        cache_read_input_tokens, cache_creation_input_tokens,
			        model_usage_json, ended_at
			 FROM chat_usage WHERE session_id = ?`, id)
	}
	if err := row.Scan(&userID, &machineID, &repo, &operator,
		&durationMs, &numTurns, &totalCost,
		&inTokens, &outTokens,
		&cacheRead, &cacheCreate,
		&modelJSON, &endedAt); err != nil {
		return nil
	}
	rec := &UsageRecord{
		UserID:                   userID,
		MachineID:                machineID,
		Repo:                     repo,
		Operator:                 operator,
		DurationMs:               durationMs,
		NumTurns:                 numTurns,
		TotalCostUSD:             totalCost,
		InputTokens:              inTokens,
		OutputTokens:             outTokens,
		CacheReadInputTokens:     cacheRead,
		CacheCreationInputTokens: cacheCreate,
	}
	rec.EndedAt, _ = time.Parse(time.RFC3339Nano, endedAt)
	if isRun {
		rec.RunID = id
	} else {
		rec.SessionID = id
	}
	if modelJSON != "" && modelJSON != "{}" && modelJSON != "null" {
		var mu map[string]ModelUsage
		if json.Unmarshal([]byte(modelJSON), &mu) == nil {
			rec.ModelUsage = mu
		}
	}
	return rec
}

// GetJob returns the current state of a job by ID, or nil if not found.
func (s *SQLiteStore) GetJob(id string) *JobRecord {
	var specJSON, status, boundMachineID, leaseWorkerID string
	var leaseExpiresAt sql.NullString
	var attemptCount int
	var createdAt, updatedAt string

	err := s.db.QueryRow(
		`SELECT spec_json, status, bound_machine_id, lease_worker_id,
		        lease_expires_at, attempt_count, created_at, updated_at
		 FROM jobs WHERE id = ?`,
		id,
	).Scan(&specJSON, &status, &boundMachineID, &leaseWorkerID,
		&leaseExpiresAt, &attemptCount, &createdAt, &updatedAt)
	if err != nil {
		return nil
	}

	var spec JobSpec
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return nil
	}

	rec := &JobRecord{
		Spec:           spec,
		Status:         status,
		BoundMachineID: boundMachineID,
		LeaseWorkerID:  leaseWorkerID,
		AttemptCount:   attemptCount,
	}
	rec.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	rec.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if leaseExpiresAt.Valid {
		rec.LeaseExpiresAt, _ = time.Parse(time.RFC3339Nano, leaseExpiresAt.String)
	}
	return rec
}

// GetRun returns a run record including all appended events, or nil if not found.
func (s *SQLiteStore) GetRun(id string) *RunRecord {
	var jobID, status, outcome, summary, errStr, startedAt string
	var endedAt sql.NullString

	err := s.db.QueryRow(
		`SELECT job_id, status, outcome, summary, error, started_at, ended_at
		 FROM runs WHERE id = ?`,
		id,
	).Scan(&jobID, &status, &outcome, &summary, &errStr, &startedAt, &endedAt)
	if err != nil {
		return nil
	}

	rec := &RunRecord{
		ID:      id,
		JobID:   jobID,
		Status:  status,
		Outcome: outcome,
		Summary: summary,
		Error:   errStr,
	}
	rec.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
	if endedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, endedAt.String)
		rec.EndedAt = &t
	}

	rows, err := s.db.Query(
		`SELECT time, level, message, fields_json FROM run_events WHERE run_id = ? ORDER BY id ASC`,
		id,
	)
	if err != nil {
		return rec
	}
	defer rows.Close()
	for rows.Next() {
		var timeStr, level, message, fieldsJSON string
		if err := rows.Scan(&timeStr, &level, &message, &fieldsJSON); err != nil {
			continue
		}
		ev := RunEvent{Level: level, Message: message}
		ev.Time, _ = time.Parse(time.RFC3339Nano, timeStr)
		if fieldsJSON != "" && fieldsJSON != "{}" && fieldsJSON != "null" {
			_ = json.Unmarshal([]byte(fieldsJSON), &ev.Fields)
		}
		rec.Events = append(rec.Events, ev)
	}
	return rec
}

// sqliteTime formats t as RFC3339Nano for storage in SQLite TEXT columns.
func sqliteTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// Cloud config (projects / repos / bindings / vcs_connections) is implemented
// in sqlite_store_cloud_config.go against real SQL tables added in
// migrations/0003_cloud_config.sql.
