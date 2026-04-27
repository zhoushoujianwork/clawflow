// Package snapshot writes JSON snapshots of ClawFlow state to
// ~/.clawflow/dashboard/data/ so the local web dashboard can render
// "what did ClawFlow do" without a backend server. Each operator run
// gets its own directory containing an append-only events.jsonl
// (raw claude stream-json events) plus a meta.json with
// start/end/status/pr_url.
//
// The contract is deliberately file-based: CLI writes, dashboard reads.
// If a user hates the bundled dashboard they can `cd` into the data
// directory, `jq` the JSON themselves, or point any static file server
// at it.
package snapshot

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
)

// DashboardRoot is ~/.clawflow/dashboard. The SPA assets live at the root;
// data lives under DashboardRoot/data.
func DashboardRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "dashboard")
}

// DataDir is the JSON snapshot root. Dashboard fetches are relative to this.
func DataDir() string {
	return filepath.Join(DashboardRoot(), "data")
}

// RunDir is the directory for a single operator run. Callers mkdir'p before
// writing into it.
func RunDir(repo string, issueNum int, startedAt time.Time) string {
	return filepath.Join(
		DataDir(),
		"runs",
		strings.ReplaceAll(repo, "/", "__"),
		fmt.Sprintf("issue-%d", issueNum),
		startedAt.UTC().Format("2006-01-02T15-04-05Z"),
	)
}

// RepoView is the dashboard-facing view of one monitored repo. Credentials
// are deliberately NOT included — the dashboard is local but still renders
// in a browser and we don't want tokens in the DOM.
type RepoView struct {
	FullName   string `json:"full_name"`
	Platform   string `json:"platform"`
	BaseURL    string `json:"base_url,omitempty"`
	BaseBranch string `json:"base_branch"`
	LocalPath  string `json:"local_path,omitempty"`
	Enabled    bool   `json:"enabled"`
	AutoFix    bool   `json:"auto_fix"`
	AutoMerge  bool   `json:"auto_merge"`
}

// OperatorView is the dashboard-facing view of one loaded operator.
type OperatorView struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Target         string   `json:"target"`
	LabelsRequired []string `json:"labels_required"`
	LabelsExcluded []string `json:"labels_excluded"`
	LockLabel      string   `json:"lock_label"`
	Source         string   `json:"source"`
}

// Meta is the top-level snapshot metadata; the dashboard reads it on load
// to show "last updated" + version.
type Meta struct {
	ClawFlowVersion string    `json:"clawflow_version"`
	LastRefresh     time.Time `json:"last_refresh"`
}

// RunMeta describes one operator run. events.jsonl lives alongside it.
type RunMeta struct {
	Operator    string    `json:"operator"`
	Repo        string    `json:"repo"`
	IssueNumber int       `json:"issue_number"`
	IssueTitle  string    `json:"issue_title,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at,omitempty"`
	// Status is one of "running", "success", "failed", "skipped".
	Status  string `json:"status"`
	PRUrl   string `json:"pr_url,omitempty"`
	Error   string `json:"error,omitempty"`
	Summary string `json:"summary,omitempty"` // operator's final text output
	// Usage is populated from events.jsonl's terminal "result" event after
	// the run finishes. Nil while the run is still in flight.
	Usage *Usage `json:"usage,omitempty"`
}

// Usage captures token + cost + model breakdown from a single run's terminal
// "result" event. All fields are summable across runs so callers can build
// aggregates without re-parsing events.jsonl.
type Usage struct {
	DurationMs               int64                  `json:"duration_ms"`
	NumTurns                 int                    `json:"num_turns"`
	TotalCostUSD             float64                `json:"total_cost_usd"`
	InputTokens              int64                  `json:"input_tokens"`
	OutputTokens             int64                  `json:"output_tokens"`
	CacheReadInputTokens     int64                  `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64                  `json:"cache_creation_input_tokens"`
	ModelUsage               map[string]ModelUsage  `json:"model_usage,omitempty"`
}

// ModelUsage is the per-model slice of a single run. The keys mirror Usage
// so the dashboard can render the same column set at any aggregation level.
type ModelUsage struct {
	InputTokens              int64   `json:"input_tokens"`
	OutputTokens             int64   `json:"output_tokens"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens"`
	CostUSD                  float64 `json:"cost_usd"`
}

// rawResultEvent is the private projection of the terminal "result" line in
// events.jsonl. Top-level fields are snake_case (claude-cli convention) but
// modelUsage values are camelCase, so we declare a sibling struct with
// camelCase JSON tags for them.
type rawResultEvent struct {
	Type         string  `json:"type"`
	DurationMs   int64   `json:"duration_ms"`
	NumTurns     int     `json:"num_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	ModelUsage map[string]rawModelUsage `json:"modelUsage"`
}

type rawModelUsage struct {
	InputTokens              int64   `json:"inputTokens"`
	OutputTokens             int64   `json:"outputTokens"`
	CacheReadInputTokens     int64   `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64   `json:"cacheCreationInputTokens"`
	CostUSD                  float64 `json:"costUSD"`
}

// ExtractUsage scans an events.jsonl file for the LAST `"type":"result"` line
// and parses out usage data. Returns (nil, nil) if no result line exists yet
// — the run is still in flight and the caller should retry on the next refresh.
// File-not-found is treated the same way (the run was created without an
// events sink, e.g. tests).
func ExtractUsage(eventsPath string) (*Usage, error) {
	f, err := os.Open(eventsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Match parseClaudeStream's 4MB cap so a long final result line is not
	// silently truncated and missed.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var lastResult *rawResultEvent
	for sc.Scan() {
		line := sc.Bytes()
		// Cheap pre-check before unmarshal — most lines are not result events.
		if !strings.Contains(string(line), `"type":"result"`) {
			continue
		}
		var ev rawResultEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type != "result" {
			continue
		}
		copy := ev
		lastResult = &copy
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if lastResult == nil {
		return nil, nil
	}

	u := &Usage{
		DurationMs:               lastResult.DurationMs,
		NumTurns:                 lastResult.NumTurns,
		TotalCostUSD:             lastResult.TotalCostUSD,
		InputTokens:              lastResult.Usage.InputTokens,
		OutputTokens:             lastResult.Usage.OutputTokens,
		CacheReadInputTokens:     lastResult.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: lastResult.Usage.CacheCreationInputTokens,
	}
	if len(lastResult.ModelUsage) > 0 {
		u.ModelUsage = make(map[string]ModelUsage, len(lastResult.ModelUsage))
		for name, m := range lastResult.ModelUsage {
			u.ModelUsage[name] = ModelUsage{
				InputTokens:              m.InputTokens,
				OutputTokens:             m.OutputTokens,
				CacheReadInputTokens:     m.CacheReadInputTokens,
				CacheCreationInputTokens: m.CacheCreationInputTokens,
				CostUSD:                  m.CostUSD,
			}
		}
	}
	return u, nil
}

// UsageAggregate is a summable totals row used in the dashboard's per-operator,
// per-repo, and grand-total slices. Tokens and cost roll up across runs.
type UsageAggregate struct {
	Runs                     int     `json:"runs"`
	TotalCostUSD             float64 `json:"total_cost_usd"`
	InputTokens              int64   `json:"input_tokens"`
	OutputTokens             int64   `json:"output_tokens"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens"`
	DurationMs               int64   `json:"duration_ms"`
}

// ModelAggregate is the per-model slice across runs. No DurationMs because a
// single run's duration spans every model it called — attributing it to one
// would double-count.
type ModelAggregate struct {
	CostUSD                  float64 `json:"cost_usd"`
	InputTokens              int64   `json:"input_tokens"`
	OutputTokens             int64   `json:"output_tokens"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens"`
}

// UsageSummary is the dashboard-facing payload at data/usage.json. It bundles
// the grand total plus per-operator, per-repo, and per-model breakdowns so
// /usage can render every panel from a single fetch.
type UsageSummary struct {
	GeneratedAt time.Time                 `json:"generated_at"`
	Totals      UsageAggregate            `json:"totals"`
	ByOperator  map[string]UsageAggregate `json:"by_operator"`
	ByRepo      map[string]UsageAggregate `json:"by_repo"`
	ByModel     map[string]ModelAggregate `json:"by_model"`
}

// WriteUsageSummary aggregates usage across the supplied entries and writes
// data/usage.json. Entries without usage (run still in flight, or pre-feature
// data on disk) are simply skipped — the summary reflects what's known.
func WriteUsageSummary(entries []RunIndexEntry) error {
	sum := UsageSummary{
		GeneratedAt: time.Now().UTC(),
		ByOperator:  map[string]UsageAggregate{},
		ByRepo:      map[string]UsageAggregate{},
		ByModel:     map[string]ModelAggregate{},
	}
	for _, e := range entries {
		if e.Usage == nil {
			continue
		}
		u := e.Usage
		addUsage(&sum.Totals, u)
		op := sum.ByOperator[e.Operator]
		addUsage(&op, u)
		sum.ByOperator[e.Operator] = op
		repo := sum.ByRepo[e.Repo]
		addUsage(&repo, u)
		sum.ByRepo[e.Repo] = repo
		for name, m := range u.ModelUsage {
			cur := sum.ByModel[name]
			cur.CostUSD += m.CostUSD
			cur.InputTokens += m.InputTokens
			cur.OutputTokens += m.OutputTokens
			cur.CacheReadInputTokens += m.CacheReadInputTokens
			cur.CacheCreationInputTokens += m.CacheCreationInputTokens
			sum.ByModel[name] = cur
		}
	}
	return writeJSON(filepath.Join(DataDir(), "usage.json"), sum)
}

func addUsage(agg *UsageAggregate, u *Usage) {
	agg.Runs++
	agg.TotalCostUSD += u.TotalCostUSD
	agg.InputTokens += u.InputTokens
	agg.OutputTokens += u.OutputTokens
	agg.CacheReadInputTokens += u.CacheReadInputTokens
	agg.CacheCreationInputTokens += u.CacheCreationInputTokens
	agg.DurationMs += u.DurationMs
}

// WriteRepos writes data/repos.json.
func WriteRepos(cfg *config.Config) error {
	views := make([]RepoView, 0, len(cfg.Repos))
	for name, r := range cfg.Repos {
		views = append(views, RepoView{
			FullName:   name,
			Platform:   r.Platform,
			BaseURL:    r.BaseURL,
			BaseBranch: r.BaseBranch,
			LocalPath:  r.LocalPath,
			Enabled:    r.Enabled,
			AutoFix:    r.AutoFix,
			AutoMerge:  r.AutoMerge,
		})
	}
	return writeJSON(filepath.Join(DataDir(), "repos.json"), views)
}

// WriteOperators writes data/operators.json.
func WriteOperators(reg *operator.Registry) error {
	ops := reg.All()
	views := make([]OperatorView, 0, len(ops))
	for _, op := range ops {
		views = append(views, OperatorView{
			Name:           op.Name,
			Description:    op.Description,
			Target:         op.Trigger.Target,
			LabelsRequired: op.Trigger.LabelsRequired,
			LabelsExcluded: op.Trigger.LabelsExcluded,
			LockLabel:      op.LockLabel,
			Source:         op.Source,
		})
	}
	return writeJSON(filepath.Join(DataDir(), "operators.json"), views)
}

// WriteMeta writes data/meta.json.
func WriteMeta(version string) error {
	return writeJSON(filepath.Join(DataDir(), "meta.json"), Meta{
		ClawFlowVersion: version,
		LastRefresh:     time.Now().UTC(),
	})
}

// WriteRunMeta writes meta.json inside an already-created run directory.
func WriteRunMeta(runDir string, m RunMeta) error {
	return writeJSON(filepath.Join(runDir, "meta.json"), m)
}

// RunIndexEntry is a flattened row for the dashboard's "recent runs" list.
// Path is a dashboard-relative URL so fetches work directly against the
// static file server (e.g. "./data/runs/foo__bar/issue-7/2026-04-24T.../").
type RunIndexEntry struct {
	RunMeta
	Path string `json:"path"`
}

// PendingEntry is one (issue, operator) pair that matched the operator's
// trigger rules but had not yet been processed when the snapshot was taken.
// One issue can produce multiple entries if it matches multiple operators;
// the dashboard renders each as its own row so the user sees every queued
// action.
type PendingEntry struct {
	Repo        string    `json:"repo"`
	IssueNumber int       `json:"issue_number"`
	IssueTitle  string    `json:"issue_title,omitempty"`
	Operator    string    `json:"operator"`
	Labels      []string  `json:"labels,omitempty"`
	CapturedAt  time.Time `json:"captured_at"`
}

// WritePending writes data/pending.json with the supplied entries. The list
// is replaced wholesale on every refresh so stale entries (issues that just
// got processed) drop off automatically.
func WritePending(entries []PendingEntry) error {
	if entries == nil {
		// JSON [] over null so the dashboard skips a nullability check.
		entries = []PendingEntry{}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Repo != entries[j].Repo {
			return entries[i].Repo < entries[j].Repo
		}
		if entries[i].IssueNumber != entries[j].IssueNumber {
			return entries[i].IssueNumber < entries[j].IssueNumber
		}
		return entries[i].Operator < entries[j].Operator
	})
	return writeJSON(filepath.Join(DataDir(), "pending.json"), entries)
}

// WriteRunsIndex walks data/runs/* and writes data/runs.json containing the
// most recent `limit` runs sorted by StartedAt desc. Used by the dashboard
// to render its home page without having to crawl the filesystem over
// HTTP.
//
// Returns the FULL collected entry set (post-sort, pre-limit) so the caller
// can pipe it into WriteUsageSummary without re-walking the tree.
func WriteRunsIndex(limit int) ([]RunIndexEntry, error) {
	runsRoot := filepath.Join(DataDir(), "runs")
	entries := collectRunEntries(runsRoot)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].StartedAt.After(entries[j].StartedAt)
	})
	indexed := entries
	if limit > 0 && len(indexed) > limit {
		indexed = indexed[:limit]
	}
	if err := writeJSON(filepath.Join(DataDir(), "runs.json"), indexed); err != nil {
		return entries, err
	}
	return entries, nil
}

// collectRunEntries walks data/runs/* and reads every meta.json it finds.
// Malformed files are skipped silently — we'd rather render an incomplete
// list than fail the entire index write.
//
// Side effect: if a run has terminated (status != "running") and meta.Usage
// is nil, we attempt to extract usage from the sibling events.jsonl and
// rewrite meta.json. This backfills historical runs the first time the
// summary is built without forcing a full re-walk on every refresh.
func collectRunEntries(root string) []RunIndexEntry {
	// Non-nil empty slice so JSON renders as [] instead of null when there
	// are no runs yet — saves the dashboard a nullability check.
	out := []RunIndexEntry{}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "meta.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var m RunMeta
		if err := json.Unmarshal(data, &m); err != nil {
			return nil
		}
		// Backfill Usage on terminated runs so historical data on disk gets
		// reflected in /usage on the next refresh. Best-effort: any error
		// here is non-fatal — we still want the index entry.
		if m.Usage == nil && m.Status != "" && m.Status != "running" {
			runDir := filepath.Dir(path)
			if u, err := ExtractUsage(filepath.Join(runDir, "events.jsonl")); err == nil && u != nil {
				m.Usage = u
				_ = WriteRunMeta(runDir, m)
			}
		}
		relDir := strings.TrimPrefix(filepath.Dir(path), DataDir())
		out = append(out, RunIndexEntry{
			RunMeta: m,
			Path:    "./data" + filepath.ToSlash(relDir) + "/",
		})
		return nil
	})
	return out
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	return os.WriteFile(path, data, 0o644)
}
