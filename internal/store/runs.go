package store

import (
	"context"
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

func (s *Store) ImportRuns(ctx context.Context, root string) (ImportCounts, error) {
	var counts ImportCounts
	for _, tree := range []struct{ name, kind string }{{"runs", "run"}, {"pilot-runs", "pilot"}} {
		err := filepath.WalkDir(filepath.Join(root, tree.name), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) && path == filepath.Join(root, tree.name) {
					return nil
				}
				return err
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
	Repo, Project, RunDir string
	From, Until           *time.Time
	Limit                 int
	Finished              bool
}

func (s *Store) Runs(ctx context.Context, kind string, f RunFilter) ([]Run, error) {
	table := "runs"
	if kind == "pilot" {
		table = "pilot_runs"
	} else if kind != "run" {
		return nil, errors.New("invalid run kind")
	}
	q := s.db.WithContext(ctx).Table(table)
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
