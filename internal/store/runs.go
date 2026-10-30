package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Run stores queryable metadata; raw events and transcripts remain on disk.
// Payload preserves presentation and resume fields without duplicating their schema.
type Run struct {
	OffloadRunID             string
	CallerRunID              string
	SessionID                string
	ExitCode                 *int
	ErrorKind                string
	ElapsedMs                *int64
	ExecutionStatus          string
	RunDir                   string `gorm:"primaryKey"`
	Repo                     string `gorm:"index:idx_runs_repo_started,priority:1"`
	Project                  string
	IssueNumber              int
	PRURL                    string
	Operator                 string
	Status                   string
	StartedAt                time.Time `gorm:"index;index:idx_runs_repo_started,priority:2"`
	EndedAt                  *time.Time
	DurationMs               int64
	Summary                  string
	Error                    string
	Profile                  string `gorm:"index"`
	Backend                  string
	Model                    string
	Machine                  string `gorm:"index"`
	CostUSD                  *float64
	InputTokens              *int64
	OutputTokens             *int64
	CacheReadInputTokens     *int64
	CacheCreationInputTokens *int64
	UsageReported            bool
	EventsPath               string
	LogPath                  string
	Payload                  string `gorm:"type:jsonb"`
}

type PilotRun Run

func (PilotRun) TableName() string { return "pilot_runs" }

type RunModelUsage struct {
	RunDir                   string `gorm:"primaryKey"`
	Kind                     string `gorm:"primaryKey"`
	Model                    string `gorm:"primaryKey"`
	CostUSD                  *float64
	InputTokens              *int64
	OutputTokens             *int64
	CacheReadInputTokens     *int64
	CacheCreationInputTokens *int64
}

func (RunModelUsage) TableName() string { return "run_model_usage" }

func migrateRuns(tx *gorm.DB) error {
	// AutoMigrate is limited to this versioned, transactional migration.
	if err := tx.AutoMigrate(&Run{}, &RunModelUsage{}); err != nil {
		return err
	}
	// A separate table uses distinct index names, including the project index.
	return tx.Exec(`CREATE TABLE pilot_runs (LIKE runs INCLUDING DEFAULTS INCLUDING CONSTRAINTS);
 ALTER TABLE pilot_runs ADD PRIMARY KEY (run_dir);
 CREATE INDEX idx_pilot_runs_repo_started ON pilot_runs (repo, started_at);
 CREATE INDEX idx_pilot_runs_started ON pilot_runs (started_at);
 CREATE INDEX idx_pilot_runs_machine ON pilot_runs (machine);
 CREATE INDEX idx_pilot_runs_profile ON pilot_runs (profile);
 CREATE INDEX idx_pilot_runs_project_started ON pilot_runs (project, started_at);`).Error
}

func unknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

// DecodeRun accepts legacy metadata. Missing usage remains SQL NULL, never zero.
func DecodeRun(dir, kind string, raw []byte) (Run, []RunModelUsage, error) {
	var m struct {
		OffloadRunID       string     `json:"offload_run_id"`
		CallerRunID        string     `json:"caller_run_id"`
		SessionID          string     `json:"session_id"`
		ExitCode           *int       `json:"exit_code"`
		ErrorKind          string     `json:"error_kind"`
		ElapsedMs          *int64     `json:"elapsed_ms"`
		ExecutionStatus    string     `json:"execution_status"`
		Repo               string     `json:"repo"`
		Project            string     `json:"project"`
		IssueNumber        int        `json:"issue_number"`
		PRURL              string     `json:"pr_url"`
		Operator           string     `json:"operator"`
		Status             string     `json:"status"`
		StartedAt          time.Time  `json:"started_at"`
		EndedAt            *time.Time `json:"ended_at"`
		Summary            string     `json:"summary"`
		Error              string     `json:"error"`
		Profile            string     `json:"profile"`
		Backend            string     `json:"backend"`
		Model              string     `json:"model"`
		Machine            string     `json:"machine"`
		DevelopmentSession struct {
			Profile string `json:"profile"`
			Backend string `json:"backend"`
		} `json:"development_session"`
		Usage *struct {
			CostUnavailable   bool     `json:"cost_unavailable"`
			TokensUnavailable bool     `json:"tokens_unavailable"`
			DurationMs        int64    `json:"duration_ms"`
			Cost              *float64 `json:"total_cost_usd"`
			Estimated         bool     `json:"estimated"`
			Input             *int64   `json:"input_tokens"`
			Output            *int64   `json:"output_tokens"`
			Read              *int64   `json:"cache_read_input_tokens"`
			Creation          *int64   `json:"cache_creation_input_tokens"`
			Models            map[string]struct {
				CostUnavailable bool     `json:"cost_unavailable"`
				Cost            *float64 `json:"cost_usd"`
				Input           *int64   `json:"input_tokens"`
				Output          *int64   `json:"output_tokens"`
				Read            *int64   `json:"cache_read_input_tokens"`
				Creation        *int64   `json:"cache_creation_input_tokens"`
			} `json:"model_usage"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return Run{}, nil, err
	}
	if m.StartedAt.IsZero() || m.Status == "" || (kind == "run" && (m.Repo == "" || m.Operator == "")) || (kind == "pilot" && m.Project == "") {
		return Run{}, nil, errors.New("missing run identity, status or started_at")
	}
	if kind != "run" && kind != "pilot" {
		return Run{}, nil, errors.New("invalid run kind")
	}
	if m.Profile == "" {
		m.Profile = m.DevelopmentSession.Profile
	}
	if m.Backend == "" {
		m.Backend = m.DevelopmentSession.Backend
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return Run{}, nil, err
	}
	r := Run{RunDir: dir, Repo: m.Repo, Project: m.Project, IssueNumber: m.IssueNumber, PRURL: m.PRURL, Operator: m.Operator, Status: m.Status, StartedAt: m.StartedAt, EndedAt: m.EndedAt, Summary: m.Summary, Error: m.Error, Profile: unknown(m.Profile), Backend: unknown(m.Backend), Model: unknown(m.Model), Machine: unknown(m.Machine), EventsPath: filepath.Join(dir, "events.jsonl"), LogPath: filepath.Join(dir, "events.jsonl")}
	r.OffloadRunID, r.CallerRunID, r.SessionID = m.OffloadRunID, m.CallerRunID, m.SessionID
	r.ExitCode, r.ErrorKind, r.ElapsedMs, r.ExecutionStatus = m.ExitCode, m.ErrorKind, m.ElapsedMs, m.ExecutionStatus
	if kind == "pilot" {
		r.Operator = "pilot:" + m.Project
	}
	if m.EndedAt != nil {
		r.DurationMs = m.EndedAt.Sub(m.StartedAt).Milliseconds()
	}
	models := []RunModelUsage{}
	if u := m.Usage; u != nil {
		r.UsageReported = true
		r.CostUSD = u.Cost
		if u.Estimated || u.CostUnavailable {
			r.CostUSD = nil
		}
		r.InputTokens = u.Input
		r.OutputTokens = u.Output
		r.CacheReadInputTokens = u.Read
		r.CacheCreationInputTokens = u.Creation
		if u.TokensUnavailable {
			r.InputTokens = nil
			r.OutputTokens = nil
			r.CacheReadInputTokens = nil
			r.CacheCreationInputTokens = nil
		}
		if u.DurationMs != 0 {
			r.DurationMs = u.DurationMs
		}
		for name, v := range u.Models {
			cost := v.Cost
			if u.Estimated || u.CostUnavailable || v.CostUnavailable {
				cost = nil
			}
			models = append(models, RunModelUsage{RunDir: dir, Kind: kind, Model: name, CostUSD: cost, InputTokens: v.Input, OutputTokens: v.Output, CacheReadInputTokens: v.Read, CacheCreationInputTokens: v.Creation})
		}
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Run{}, nil, err
	}
	payload["profile"] = r.Profile
	payload["backend"] = r.Backend
	payload["model"] = r.Model
	payload["machine"] = r.Machine
	payload["usage_reported"] = r.UsageReported
	payload["exit_code"] = r.ExitCode
	payload["elapsed_ms"] = r.ElapsedMs
	if u, ok := payload["usage"].(map[string]any); ok {
		u["total_cost_usd"] = r.CostUSD
		u["input_tokens"] = r.InputTokens
		u["output_tokens"] = r.OutputTokens
		u["cache_read_input_tokens"] = r.CacheReadInputTokens
		u["cache_creation_input_tokens"] = r.CacheCreationInputTokens
		if mm, ok := u["model_usage"].(map[string]any); ok {
			for _, m := range models {
				if v, ok := mm[m.Model].(map[string]any); ok {
					v["cost_usd"] = m.CostUSD
					v["input_tokens"] = m.InputTokens
					v["output_tokens"] = m.OutputTokens
					v["cache_read_input_tokens"] = m.CacheReadInputTokens
					v["cache_creation_input_tokens"] = m.CacheCreationInputTokens
				}
			}
		}
	}
	b, err := json.Marshal(payload)
	r.Payload = string(b)
	return r, models, err
}

// UpsertRun replaces metadata and model breakdown atomically. Counts are based
// on normalized content, so repeating an import is a no-op.
func (s *Store) UpsertRun(ctx context.Context, dir, kind string, raw []byte) (string, error) {
	r, models, err := DecodeRun(dir, kind, raw)
	if err != nil {
		return "invalid", err
	}
	table := "runs"
	if kind == "pilot" {
		table = "pilot_runs"
	}
	result := "imported"
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize attributed replays across import paths, never by session.
		attributed := r.OffloadRunID != "" && r.Machine != "unknown"
		if attributed {
			key := kind + ":attempt:" + r.Machine + ":" + r.OffloadRunID
			if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", key).Error; err != nil {
				return err
			}
			var existing Run
			err := tx.Table(table).Where("machine = ? AND offload_run_id = ?", r.Machine, r.OffloadRunID).Take(&existing).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil {
				r.RunDir, r.EventsPath, r.LogPath = existing.RunDir, existing.EventsPath, existing.LogPath
				for i := range models {
					models[i].RunDir = r.RunDir
				}
			}
		}
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended(?, 0))", kind+":"+r.RunDir).Error; err != nil {
			return err
		}
		var old Run
		err := tx.Table(table).Where("run_dir = ?", r.RunDir).Take(&old).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err == nil {
			var a, b any
			_ = json.Unmarshal([]byte(old.Payload), &a)
			_ = json.Unmarshal([]byte(r.Payload), &b)
			if reflect.DeepEqual(a, b) {
				result = "skipped"
				return nil
			}
			result = "updated"
		}
		if err := tx.Table(table).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "run_dir"}}, UpdateAll: true}).Create(&r).Error; err != nil {
			return err
		}
		if err := tx.Where("run_dir = ? AND kind = ?", r.RunDir, kind).Delete(&RunModelUsage{}).Error; err != nil {
			return err
		}
		if len(models) > 0 {
			return tx.Create(&models).Error
		}
		return nil
	})
	return result, err
}

type ImportCounts struct{ Imported, Updated, Skipped, Invalid int }

// ImportDiagnostic reports a skipped or invalid metadata file relative to the data directory.
type ImportDiagnostic struct {
	Path, Status, Reason string
}

func (s *Store) ImportRuns(ctx context.Context, root string, report ...func(ImportDiagnostic)) (ImportCounts, error) {
	var counts ImportCounts
	diagnostic := func(path, status, reason string) {
		rel, _ := filepath.Rel(root, path)
		for _, fn := range report {
			fn(ImportDiagnostic{Path: filepath.ToSlash(rel), Status: status, Reason: reason})
		}
	}
	for _, tree := range []struct{ name, kind string }{{"runs", "run"}, {"pilot-runs", "pilot"}} {
		err := filepath.WalkDir(filepath.Join(root, tree.name), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) && path == filepath.Join(root, tree.name) {
					return nil
				}
				return err
			}
			if d.IsDir() && strings.Contains(d.Name(), ".orphaned-") {
				return filepath.SkipDir
			}
			if d.IsDir() || d.Name() != "meta.json" {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if _, _, err := DecodeRun(filepath.Dir(path), tree.kind, raw); err != nil {
				counts.Invalid++
				diagnostic(path, "invalid", err.Error())
				return nil
			}
			result, err := s.UpsertRun(ctx, filepath.Dir(path), tree.kind, raw)
			if err != nil {
				return fmt.Errorf("import run: %w", err)
			}
			switch result {
			case "imported":
				counts.Imported++
			case "updated":
				counts.Updated++
			case "skipped":
				counts.Skipped++
				diagnostic(path, "skipped", "unchanged metadata already imported")
			}
			return nil
		})
		if err != nil {
			return counts, err
		}
	}
	return counts, nil
}

type RunFilter struct {
	Repo, Project, RunDir          string
	ExecutionStatus, Role, Profile string
	AttemptID, Cursor              string
	From, Until                    *time.Time
	Limit                          int
	Finished                       bool
}

func (s *Store) Runs(ctx context.Context, kind string, f RunFilter) ([]Run, error) {
	table := "runs"
	if kind == "pilot" {
		table = "pilot_runs"
	} else if kind != "run" {
		return nil, errors.New("invalid run kind")
	}
	if err := ValidateHistoryKeys(kind, f.AttemptID, f.Cursor); err != nil {
		return nil, err
	}
	q := s.db.WithContext(ctx).Table(table)
	if f.AttemptID != "" {
		var id attemptKey
		_ = decodeHistoryKey(f.AttemptID, &id)
		if id.Dir != "" {
			q = q.Where("run_dir = ?", id.Dir)
		} else {
			q = q.Where("machine = ? AND offload_run_id = ?", id.Machine, id.RunID)
		}
	}
	if f.Cursor != "" {
		var c runCursor
		_ = decodeHistoryKey(f.Cursor, &c)
		q = q.Where("started_at < ? OR (started_at = ? AND run_dir > ?)", c.StartedAt, c.StartedAt, c.Dir)
	}
	if f.ExecutionStatus != "" {
		q = q.Where("COALESCE(NULLIF(execution_status, ''), 'unknown') = ?", f.ExecutionStatus)
	}
	if f.Profile != "" {
		q = q.Where("profile = ?", f.Profile)
	}
	if f.Role != "" {
		q = q.Where("COALESCE(NULLIF(payload->>'role', ''), NULLIF(payload->'development_session'->>'role', ''), 'unknown') = ?", f.Role)
	}
	if f.RunDir != "" {
		q = q.Where("run_dir = ?", f.RunDir)
	}
	if f.Repo != "" {
		q = q.Where("repo = ?", f.Repo)
	}
	if f.Project != "" {
		q = q.Where("project = ?", f.Project)
	}
	if f.From != nil {
		q = q.Where("started_at >= ?", *f.From)
	}
	if f.Until != nil {
		q = q.Where("started_at < ?", *f.Until)
	}
	if f.Finished {
		q = q.Where("status NOT IN ?", []string{"running", "finalizing", "pending", "queued"})
	}
	if f.Limit > 0 {
		q = q.Limit(f.Limit)
	}
	rows := []Run{}
	err := q.Order("started_at DESC, run_dir ASC").Find(&rows).Error
	return rows, err
}

// RunPayloads preserves the dashboard wire format, with paths relative to data/.
func RunPayloads(rows []Run, root string) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		var m map[string]any
		if err := json.Unmarshal([]byte(r.Payload), &m); err != nil {
			return nil, err
		}
		path, err := filepath.Rel(root, r.RunDir)
		if err != nil {
			return nil, err
		}
		m["path"] = "./data/" + filepath.ToSlash(path) + "/"
		out = append(out, m)
	}
	return out, nil
}

// migrateExecutionEvidence adds nullable evidence without backfilling success.
func migrateExecutionEvidence(tx *gorm.DB) error {
	for _, table := range []string{"runs", "pilot_runs"} {
		if err := tx.Exec("ALTER TABLE " + table + `
 ADD COLUMN IF NOT EXISTS offload_run_id text,
 ADD COLUMN IF NOT EXISTS caller_run_id text,
 ADD COLUMN IF NOT EXISTS session_id text,
 ADD COLUMN IF NOT EXISTS exit_code bigint,
 ADD COLUMN IF NOT EXISTS error_kind text,
 ADD COLUMN IF NOT EXISTS elapsed_ms bigint,
 ADD COLUMN IF NOT EXISTS execution_status text`).Error; err != nil {
			return err
		}
		if err := tx.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_" + table + "_offload_attempt ON " + table + " (machine, offload_run_id) WHERE offload_run_id <> '' AND machine <> '' AND machine <> 'unknown'").Error; err != nil {
			return err
		}
	}
	return nil
}

// Attempt IDs use the persisted machine/Offload identity when available. Legacy
// records retain their path identity; a session never identifies an attempt.
type attemptKey struct {
	Kind    string `json:"k"`
	Machine string `json:"m,omitempty"`
	RunID   string `json:"r,omitempty"`
	Dir     string `json:"d,omitempty"`
}
type runCursor struct {
	Kind      string    `json:"k"`
	StartedAt time.Time `json:"t"`
	Dir       string    `json:"d"`
}

func encodeHistoryKey(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeHistoryKey(s string, v any) error {
	if len(s) > 8192 {
		return errors.New("history key too long")
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
func AttemptID(r Run, kind string) string {
	k := attemptKey{Kind: kind}
	if r.OffloadRunID != "" && r.Machine != "" && r.Machine != "unknown" {
		k.Machine, k.RunID = r.Machine, r.OffloadRunID
	} else {
		k.Dir = r.RunDir
	}
	return encodeHistoryKey(k)
}
func RunsCursor(r Run, kind string) string {
	return encodeHistoryKey(runCursor{kind, r.StartedAt, r.RunDir})
}
func ValidateHistoryKeys(kind, attempt, cursor string) error {
	if attempt != "" {
		var k attemptKey
		if err := decodeHistoryKey(attempt, &k); err != nil || k.Kind != kind || !((k.Dir != "" && k.Machine == "" && k.RunID == "") || (k.Dir == "" && k.Machine != "" && k.Machine != "unknown" && k.RunID != "")) {
			return errors.New("invalid attempt_id")
		}
	}
	if cursor != "" {
		var c runCursor
		if err := decodeHistoryKey(cursor, &c); err != nil || c.Kind != kind || c.StartedAt.IsZero() || c.Dir == "" {
			return errors.New("invalid cursor")
		}
	}
	return nil
}

type RunAttribution struct {
	Source   string `json:"source"`
	Kind     string `json:"kind"`
	Repo     string `json:"repo"`
	Project  string `json:"project"`
	Operator string `json:"operator"`
	Role     string `json:"role"`
	Profile  string `json:"profile"`
	Machine  string `json:"machine"`
}
type RunAvailability struct {
	Attempt         bool `json:"attempt"`
	ExecutionStatus bool `json:"execution_status"`
	ExitCode        bool `json:"exit_code"`
	Elapsed         bool `json:"elapsed"`
	Cost            bool `json:"cost"`
	Tokens          bool `json:"tokens"`
}

// RunListPayloads projects only presentation metadata. Neither arbitrary payload
// extensions nor prompts, results, summaries or transcripts enter a list response.
func RunListPayloads(rows []Run, root, kind string) ([]map[string]any, error) {
	payloads, err := RunPayloads(rows, root)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for i, r := range rows {
		raw := payloads[i]
		m := map[string]any{}
		for _, key := range strings.Fields("path repo project operator issue_number issue_title issue_state started_at ended_at status stage pr_url pr_number profile backend model machine service_tier offload_run_id caller_run_id session_id exit_code error_kind elapsed_ms execution_status usage_reported") {
			if v, ok := raw[key]; ok {
				m[key] = v
			}
		}
		if u, ok := raw["usage"].(map[string]any); ok {
			clean := map[string]any{}
			for _, key := range strings.Fields("duration_ms num_turns total_cost_usd input_tokens output_tokens cache_read_input_tokens cache_creation_input_tokens cost_basis cost_is_estimate cost_unavailable tokens_unavailable estimated") {
				if v, ok := u[key]; ok {
					clean[key] = v
				}
			}
			if models, ok := u["model_usage"].(map[string]any); ok {
				breakdown := map[string]any{}
				for model, value := range models {
					if values, ok := value.(map[string]any); ok {
						metrics := map[string]any{}
						for _, key := range strings.Fields("cost_usd input_tokens output_tokens cache_read_input_tokens cache_creation_input_tokens cost_unavailable") {
							if v, ok := values[key]; ok {
								metrics[key] = v
							}
						}
						breakdown[model] = metrics
					}
				}
				clean["model_usage"] = breakdown
			}
			m["usage"] = clean
		}
		role, _ := raw["role"].(string)
		if role == "" {
			if d, ok := raw["development_session"].(map[string]any); ok {
				role, _ = d["role"].(string)
			}
		}
		m["operator"], m["repo"] = r.Operator, r.Repo
		m["profile"], m["backend"], m["model"], m["machine"] = r.Profile, r.Backend, r.Model, r.Machine
		m["attempt_id"] = AttemptID(r, kind)
		m["attribution"] = RunAttribution{"clawflow", kind, r.Repo, r.Project, r.Operator, unknown(role), r.Profile, r.Machine}
		m["availability"] = RunAvailability{r.OffloadRunID != "" && r.Machine != "" && r.Machine != "unknown", r.ExecutionStatus != "", r.ExitCode != nil, r.ElapsedMs != nil, r.CostUSD != nil, r.InputTokens != nil || r.OutputTokens != nil || r.CacheReadInputTokens != nil || r.CacheCreationInputTokens != nil}
		out = append(out, m)
	}
	return out, nil
}
