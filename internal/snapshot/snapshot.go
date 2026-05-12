// Package snapshot writes JSON snapshots of ClawFlow state to
// ~/.clawflow/data/ so the local web dashboard can render "what did
// ClawFlow do" without a backend server. Each operator run gets its
// own directory containing an append-only events.jsonl (raw claude
// stream-json events) plus a meta.json with start/end/status/pr_url.
//
// The contract is deliberately file-based: CLI writes, dashboard reads.
// If a user hates the bundled dashboard they can `cd` into the data
// directory, `jq` the JSON themselves, or point any static file server
// at it.
//
// Layout note (changed in v0.x): runtime data lives at ~/.clawflow/data/,
// SEPARATE from the SPA assets at ~/.clawflow/dashboard/. Earlier
// versions co-located them at ~/.clawflow/dashboard/data/, which made
// "refresh the SPA bundle" and "blow away run history" look like the
// same operation. MigrateLegacyDataDir handles a one-shot move on
// startup so existing installs upgrade transparently.
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
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

// DashboardRoot is ~/.clawflow/dashboard — strictly the SPA bundle
// (assets/, index.html, favicon.svg, logo.svg). Treated as a build
// artifact: `clawflow web` overwrites it from the embedded FS on every
// start. Safe to delete; nothing in here needs to persist across
// upgrades.
func DashboardRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "dashboard")
}

// DataDir is the JSON snapshot root — append-only run history, Pilot
// activity, and aggregate views the dashboard reads. Lives at
// ~/.clawflow/data/, deliberately OUTSIDE DashboardRoot so the SPA's
// "delete and re-extract" cycle can never touch it.
func DataDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "data")
}

// LegacyDataDir is the pre-split location (~/.clawflow/dashboard/data).
// Only consulted by MigrateLegacyDataDir during startup.
func LegacyDataDir() string {
	return filepath.Join(DashboardRoot(), "data")
}

// MigrateLegacyDataDir is a one-shot no-op-if-already-done move from
// ~/.clawflow/dashboard/data/ → ~/.clawflow/data/. Called from
// `clawflow web` start before anything reads or writes data.
//
// Conditions for the move:
//   - legacy directory exists and is non-empty
//   - new directory does not exist (or exists but is empty)
//
// Both same-FS rename and cross-FS copy are handled. Failures are
// returned to the caller, which logs and continues with the new
// location empty — no dataloss, just a missed migration the user can
// retry by manually moving the directory.
func MigrateLegacyDataDir() (moved bool, err error) {
	legacy := LegacyDataDir()
	if _, statErr := os.Stat(legacy); os.IsNotExist(statErr) {
		return false, nil
	} else if statErr != nil {
		return false, fmt.Errorf("stat legacy data dir: %w", statErr)
	}

	current := DataDir()
	if entries, statErr := os.ReadDir(current); statErr == nil && len(entries) > 0 {
		// New location already populated — assume the migration ran
		// previously (or the user populated it manually). Leave both
		// in place rather than risk merging.
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(current), 0o755); err != nil {
		return false, fmt.Errorf("mkdir parent: %w", err)
	}
	// Drop any empty pre-created new dir so Rename has a clean target.
	_ = os.Remove(current)
	if renameErr := os.Rename(legacy, current); renameErr == nil {
		return true, nil
	}
	// Cross-filesystem fallback: walk-copy then remove. Unusual on the
	// home dir but possible if ~/.clawflow is on a different mount than
	// the parent home — at least we don't lose data.
	if copyErr := copyTree(legacy, current); copyErr != nil {
		return false, fmt.Errorf("copy %s → %s: %w", legacy, current, copyErr)
	}
	if rmErr := os.RemoveAll(legacy); rmErr != nil {
		// Copy succeeded but cleanup failed — not fatal; warn and move on.
		return true, fmt.Errorf("copied to new location but failed to remove legacy %s: %w", legacy, rmErr)
	}
	return true, nil
}

// CleanupLegacyDashboardDir removes the now-obsolete ~/.clawflow/dashboard/
// directory. As of the v0.x SPA-from-embed.FS refactor, `clawflow web` no
// longer extracts dashboard assets to disk — the binary serves them
// directly from the embedded FS — so the dashboard directory is dead
// weight after data migration. Called from `clawflow web` startup right
// after MigrateLegacyDataDir.
//
// Safe to call repeatedly: missing directory is a no-op. The caller logs
// any error but should not fail startup over it; cleanup of a stray
// directory is purely cosmetic.
func CleanupLegacyDashboardDir() error {
	root := DashboardRoot()
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("stat dashboard dir: %w", err)
	}
	// Sanity-check: only proceed if the directory looks like an old
	// extracted SPA (assets/, index.html, …) and NOT a populated data
	// dir. The migration already moved data out, but defensive coding
	// is cheap insurance against future contributors who repurpose the
	// path without realizing this cleanup runs.
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read dashboard dir: %w", err)
	}
	for _, e := range entries {
		switch e.Name() {
		case "assets", "index.html", "favicon.svg", "logo.svg":
			// known SPA artifact — fine to remove
		case "data":
			// Migration should have emptied this. If it's still here
			// non-empty something went wrong; bail rather than nuke it.
			subEntries, _ := os.ReadDir(filepath.Join(root, "data"))
			if len(subEntries) > 0 {
				return fmt.Errorf("legacy data subdir is non-empty (%d entries) — refusing to clean dashboard dir", len(subEntries))
			}
		default:
			return fmt.Errorf("unexpected entry %q in legacy dashboard dir — refusing to clean", e.Name())
		}
	}
	return os.RemoveAll(root)
}

// copyTree is a minimal recursive copier used only by MigrateLegacyDataDir's
// cross-FS fallback. Preserves file mode but not ownership/timestamps —
// fine for our use, the data inside is already content-addressed by name.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
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
	FullName     string `json:"full_name"`
	Platform     string `json:"platform"`
	BaseURL      string `json:"base_url,omitempty"`
	BaseBranch   string `json:"base_branch"`
	LocalPath    string `json:"local_path,omitempty"`
	Enabled      bool   `json:"enabled"`
	AutoApprove  bool   `json:"auto_approve"`
	AutoMerge    bool   `json:"auto_merge"`
	// BoundMachine is the hostname this repo is restricted to, or empty if
	// unbound. Exposed to the dashboard so the bind/unbind button can show
	// the current binding state without a separate API call.
	BoundMachine string `json:"bound_machine,omitempty"`
}

// OperatorView is the dashboard-facing view of one loaded operator.
type OperatorView struct {
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Target            string   `json:"target"`
	LabelsRequired    []string `json:"labels_required"`
	LabelsRequiredAny []string `json:"labels_required_any,omitempty"`
	LabelsExcluded    []string `json:"labels_excluded"`
	LockLabel         string   `json:"lock_label"`
	Source            string   `json:"source"`
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
	IssueState  string    `json:"issue_state,omitempty"` // "open" | "closed"
	StartedAt   time.Time `json:"started_at"`
	// EndedAt is a pointer so a nil value omits the JSON key for a still-
	// running run. With a value type, Go's `omitempty` does NOT skip a zero
	// time.Time (`omitempty` only skips nil/0/""/empty), so the wire format
	// would carry "0001-01-01T00:00:00Z" and the dashboard's duration math
	// would render it as a giant negative offset.
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	// Status is one of "running", "finalizing", "success", "failed", "skipped",
	// "cancelled", "no-marker", "skipped-empty".
	// "cancelled" is set only by /api/run/cancel after the runner process
	// is killed — it lets the dashboard distinguish a user-initiated kill
	// from an organic crash ("failed").
	// "finalizing" is written immediately after operator.Run() returns
	// successfully, before usage extraction and meta cleanup. If the process
	// is killed in this window, the reconciler treats it as completed (not
	// stale) and promotes it to "success" without re-queuing the issue.
	// "no-marker" means claude produced output but omitted the outcome marker;
	// the issue stays unlabeled and the circuit breaker counts this run.
	// "skipped-empty" means claude produced empty output; same treatment.
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
	Periods     []PeriodSummary           `json:"periods,omitempty"`
}

// PeriodSummary holds usage aggregated within a single billing period.
type PeriodSummary struct {
	PeriodStart string                    `json:"period_start"`
	PeriodEnd   string                    `json:"period_end"`
	Totals      UsageAggregate            `json:"totals"`
	ByOperator  map[string]UsageAggregate `json:"by_operator"`
	ByRepo      map[string]UsageAggregate `json:"by_repo"`
	ByModel     map[string]ModelAggregate `json:"by_model"`
	DailyTrend  []DailyPoint             `json:"daily_trend"`
}

// DailyPoint is one day's aggregated usage within a billing period.
type DailyPoint struct {
	Date         string                    `json:"date"`
	Runs         int                       `json:"runs"`
	TotalCostUSD float64                   `json:"total_cost_usd"`
	InputTokens  int64                     `json:"input_tokens"`
	OutputTokens int64                     `json:"output_tokens"`
	ByOperator   map[string]UsageAggregate `json:"by_operator,omitempty"`
	ByRepo       map[string]UsageAggregate `json:"by_repo,omitempty"`
	ByModel      map[string]ModelAggregate `json:"by_model,omitempty"`
}

// WriteUsageSummary aggregates usage across the supplied entries and writes
// data/usage.json. billingCycleDay (1-28) controls when monthly periods start;
// 0 defaults to 1 (calendar month). Entries without usage (run still in flight,
// or pre-feature data on disk) are simply skipped.
func WriteUsageSummary(entries []RunIndexEntry, billingCycleDay int) error {
	if billingCycleDay < 1 || billingCycleDay > 28 {
		billingCycleDay = 1
	}

	sum := UsageSummary{
		GeneratedAt: time.Now().UTC(),
		ByOperator:  map[string]UsageAggregate{},
		ByRepo:      map[string]UsageAggregate{},
		ByModel:     map[string]ModelAggregate{},
	}

	// periodKey → *PeriodSummary for grouping
	type periodKey struct{ start, end time.Time }
	periods := map[periodKey]*PeriodSummary{}

	for _, e := range entries {
		if e.Usage == nil {
			continue
		}
		u := e.Usage

		// All-time aggregation (unchanged)
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

		// Period aggregation
		pStart, pEnd := billingPeriod(e.StartedAt, billingCycleDay)
		pk := periodKey{pStart, pEnd}
		ps, ok := periods[pk]
		if !ok {
			ps = &PeriodSummary{
				PeriodStart: pStart.Format("2006-01-02"),
				PeriodEnd:   pEnd.Format("2006-01-02"),
				ByOperator:  map[string]UsageAggregate{},
				ByRepo:      map[string]UsageAggregate{},
				ByModel:     map[string]ModelAggregate{},
			}
			periods[pk] = ps
		}
		addUsage(&ps.Totals, u)
		pop := ps.ByOperator[e.Operator]
		addUsage(&pop, u)
		ps.ByOperator[e.Operator] = pop
		prepo := ps.ByRepo[e.Repo]
		addUsage(&prepo, u)
		ps.ByRepo[e.Repo] = prepo
		for name, m := range u.ModelUsage {
			cur := ps.ByModel[name]
			cur.CostUSD += m.CostUSD
			cur.InputTokens += m.InputTokens
			cur.OutputTokens += m.OutputTokens
			cur.CacheReadInputTokens += m.CacheReadInputTokens
			cur.CacheCreationInputTokens += m.CacheCreationInputTokens
			ps.ByModel[name] = cur
		}
	}

	// Build daily trends and collect periods sorted newest-first
	var sortedPeriods []PeriodSummary
	for pk, ps := range periods {
		ps.DailyTrend = buildDailyTrend(entries, pk.start, pk.end)
		sortedPeriods = append(sortedPeriods, *ps)
	}
	sort.Slice(sortedPeriods, func(i, j int) bool {
		return sortedPeriods[i].PeriodStart > sortedPeriods[j].PeriodStart
	})
	sum.Periods = sortedPeriods

	return writeJSON(filepath.Join(DataDir(), "usage.json"), sum)
}

// billingPeriod returns the start and end dates of the billing period that
// contains t, given a cycle start day (1-28).
func billingPeriod(t time.Time, cycleDay int) (start, end time.Time) {
	y, m, _ := t.UTC().Date()
	if t.Day() >= cycleDay {
		start = time.Date(y, m, cycleDay, 0, 0, 0, 0, time.UTC)
	} else {
		start = time.Date(y, m-1, cycleDay, 0, 0, 0, 0, time.UTC)
	}
	end = time.Date(start.Year(), start.Month()+1, cycleDay-1, 0, 0, 0, 0, time.UTC)
	return
}

// buildDailyTrend produces a DailyPoint for every day in [periodStart, periodEnd],
// filling in zeros for days with no runs. Each point includes per-operator,
// per-repo, and per-model breakdowns so the frontend can drill down on click.
func buildDailyTrend(entries []RunIndexEntry, periodStart, periodEnd time.Time) []DailyPoint {
	dayMap := map[string]*DailyPoint{}
	for d := periodStart; !d.After(periodEnd); d = d.AddDate(0, 0, 1) {
		key := d.Format("2006-01-02")
		dayMap[key] = &DailyPoint{
			Date:       key,
			ByOperator: map[string]UsageAggregate{},
			ByRepo:     map[string]UsageAggregate{},
			ByModel:    map[string]ModelAggregate{},
		}
	}
	for _, e := range entries {
		if e.Usage == nil {
			continue
		}
		day := e.StartedAt.UTC().Format("2006-01-02")
		dp, ok := dayMap[day]
		if !ok {
			continue
		}
		dp.Runs++
		dp.TotalCostUSD += e.Usage.TotalCostUSD
		dp.InputTokens += e.Usage.InputTokens
		dp.OutputTokens += e.Usage.OutputTokens

		op := dp.ByOperator[e.Operator]
		addUsage(&op, e.Usage)
		dp.ByOperator[e.Operator] = op

		repo := dp.ByRepo[e.Repo]
		addUsage(&repo, e.Usage)
		dp.ByRepo[e.Repo] = repo

		for name, m := range e.Usage.ModelUsage {
			cur := dp.ByModel[name]
			cur.CostUSD += m.CostUSD
			cur.InputTokens += m.InputTokens
			cur.OutputTokens += m.OutputTokens
			cur.CacheReadInputTokens += m.CacheReadInputTokens
			cur.CacheCreationInputTokens += m.CacheCreationInputTokens
			dp.ByModel[name] = cur
		}
	}
	out := make([]DailyPoint, 0, len(dayMap))
	for d := periodStart; !d.After(periodEnd); d = d.AddDate(0, 0, 1) {
		out = append(out, *dayMap[d.Format("2006-01-02")])
	}
	return out
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
			FullName:     name,
			Platform:     r.Platform,
			BaseURL:      r.BaseURL,
			BaseBranch:   r.BaseBranch,
			LocalPath:    r.LocalPath,
			Enabled:      r.Enabled,
			AutoApprove:  r.AutoApprove,
			AutoMerge:    r.AutoMerge,
			BoundMachine: r.BoundMachine,
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
			Name:              op.Name,
			Description:       op.Description,
			Target:            op.Trigger.Target,
			LabelsRequired:    op.Trigger.LabelsRequired,
			LabelsRequiredAny: op.Trigger.LabelsRequiredAny,
			LabelsExcluded:    op.Trigger.LabelsExcluded,
			LockLabel:         op.LockLabel,
			Source:            op.Source,
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
// A zero StartedAt is repaired to time.Now() before serialization — Go's
// zero time.Time JSON-marshals to "0001-01-01T00:00:00Z" which the
// dashboard rendered as "-63913033671349s ago". Past callers already
// pass time.Now(), but a defensive backstop here means a future caller
// that forgets won't poison the index.
func WriteRunMeta(runDir string, m RunMeta) error {
	if m.StartedAt.IsZero() {
		m.StartedAt = time.Now().UTC()
	}
	return writeJSON(filepath.Join(runDir, "meta.json"), m)
}

// MigrateFailedToCancelled walks every run dir and rewrites meta.json's
// Status from "failed" to "cancelled" when the recorded Error makes it
// unambiguous that the run was killed via /api/run/cancel rather than
// crashing on its own. This is a one-shot heal-old-data migration:
// before the dedicated "cancelled" status existed, cancelled rows were
// written as failed with an error string we can pattern-match on.
//
// Conservative match list — only error texts the cancel path itself
// emits. Reconciler-generated text like "events.jsonl quiet for >…;
// runner PID is dead/missing (interrupted/killed)" is intentionally
// NOT matched because it overlaps with genuine crashes (OOM kill,
// machine sleep, etc.) and we don't want to silently relabel those.
//
// Returns the number of rows migrated. Idempotent: a second call sees
// every row as already "cancelled" and does nothing.
func MigrateFailedToCancelled() int {
	runsRoot := filepath.Join(DataDir(), "runs")
	if _, err := os.Stat(runsRoot); os.IsNotExist(err) {
		return 0
	}
	cancelMarkers := []string{
		"cancelled by user",            // /api/run/cancel after kill
		"cleared by cancel",            // /api/run/cancel after dead-PID branch
	}
	var migrated int
	_ = filepath.WalkDir(runsRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		metaPath := filepath.Join(path, "meta.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			return nil
		}
		var m RunMeta
		if json.Unmarshal(data, &m) != nil {
			return nil
		}
		if m.Status != "failed" || m.Error == "" {
			return nil
		}
		matched := false
		for _, marker := range cancelMarkers {
			if strings.Contains(m.Error, marker) {
				matched = true
				break
			}
		}
		if !matched {
			return nil
		}
		m.Status = "cancelled"
		if err := WriteRunMeta(path, m); err == nil {
			migrated++
		}
		return nil
	})
	return migrated
}

// MarkRunningAsCancelled finds any in-flight run for (repo, issue) — i.e. a
// run dir whose meta.json has Status="running" — and rewrites it to
// Status="cancelled" with Error="<reason>" and EndedAt=now. Returns the
// number of runs flipped (normally 0 or 1; the lock guarantees at most
// one running run per (repo, issue), but stray rows from prior crashes
// would also be reaped here).
//
// Called from /api/run/cancel after the process tree is killed and the
// lock released, so the dashboard's runs.json reflects the cancellation
// on the next poll instead of waiting for the per-operator quiet-window
// reconciler to fire (up to 10 min for "implement").
func MarkRunningAsCancelled(repo string, issueNum int, reason string) int {
	issueRoot := filepath.Join(
		DataDir(),
		"runs",
		strings.ReplaceAll(repo, "/", "__"),
		fmt.Sprintf("issue-%d", issueNum),
	)
	entries, err := os.ReadDir(issueRoot)
	if err != nil {
		return 0
	}
	now := time.Now().UTC()
	var fixed int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		runDir := filepath.Join(issueRoot, e.Name())
		metaPath := filepath.Join(runDir, "meta.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var m RunMeta
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		if m.Status != "running" && m.Status != "finalizing" {
			continue
		}
		m.Status = "cancelled"
		if reason != "" {
			m.Error = reason
		} else {
			m.Error = "cancelled by user"
		}
		end := now
		m.EndedAt = &end
		if err := WriteRunMeta(runDir, m); err == nil {
			fixed++
		}
	}
	return fixed
}

// RunIndexEntry is a flattened row for the dashboard's "recent runs" list.
// Path is a dashboard-relative URL so fetches work directly against the
// static file server (e.g. "./data/runs/foo__bar/issue-7/2026-04-24T.../").
//
// RunnerAlive is set only for rows with Status=="running". It reports
// whether the runner's PID lockfile is held by a live process AND the
// lock matches this row's StartedAt (so the lock isn't from a different
// run that happened to recycle the same issue). nil means "not checked"
// (terminal status) or "no live runner found"; true means the dashboard
// can show a "live" badge instead of treating long-quiet runs as suspect.
type RunIndexEntry struct {
	RunMeta
	Path        string `json:"path"`
	RunnerAlive *bool  `json:"runner_alive,omitempty"`
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


// IssueEntry represents a single issue from the repository, regardless of
// whether it has pending operators. Used by the dashboard to show all issues
// with their current state and labels.
//
// CreatedAt is the RFC3339 timestamp the issue was opened on the
// upstream VCS. Optional in the wire format (older snapshots don't
// have it) but always populated by the scanner — consumed by Pilot's
// issue_digest to compute "new in last 24h" without an extra API call.
type IssueEntry struct {
	Repo        string    `json:"repo"`
	IssueNumber int       `json:"issue_number"`
	IssueTitle  string    `json:"issue_title,omitempty"`
	Labels      []string  `json:"labels,omitempty"`
	State       string    `json:"state"` // "open" | "closed"
	CapturedAt  time.Time `json:"captured_at"`
	CreatedAt   string    `json:"created_at,omitempty"`
	ClosedAt    string    `json:"closed_at,omitempty"`
}

// WriteIssues writes data/issues.json with all issues from monitored repos.
func WriteIssues(entries []IssueEntry) error {
	if entries == nil {
		entries = []IssueEntry{}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Repo != entries[j].Repo {
			return entries[i].Repo < entries[j].Repo
		}
		if entries[i].State != entries[j].State {
			// open issues first
			return entries[i].State == "open" && entries[j].State == "closed"
		}
		return entries[i].IssueNumber > entries[j].IssueNumber // newest first
	})
	return writeJSON(filepath.Join(DataDir(), "issues.json"), entries)
}

// PrunePending rewrites data/pending.json to drop entries that this
// machine will never act on. Called from `clawflow web` startup so a
// long-paused install (or a freshly re-bound repo) doesn't keep
// surfacing rows that next-scan would have removed anyway.
//
// An entry is pruned when ANY of the following holds:
//   - its repo is no longer in the config
//   - its repo is pinned to a different machine via bound_machine
//     (we only prune when we both have a hostname AND a non-empty
//     bound_machine that differs — otherwise we can't be confident)
//   - the issue is known closed in data/issues.json
//
// Idempotent: a clean pending.json comes through unchanged. Returns
// the number of entries removed so the caller can log it.
func PrunePending() int {
	path := filepath.Join(DataDir(), "pending.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var pending []PendingEntry
	if err := json.Unmarshal(raw, &pending); err != nil {
		return 0
	}
	if len(pending) == 0 {
		return 0
	}

	// Build the prune predicates from current state.
	cfg, _ := config.Load()
	repoBound := make(map[string]string)
	repoExists := make(map[string]bool)
	if cfg != nil {
		for fullName, r := range cfg.Repos {
			repoExists[fullName] = true
			repoBound[fullName] = r.BoundMachine
		}
	}
	hostname, _ := os.Hostname()

	closedIssues := make(map[string]bool) // key: "repo#num"
	if data, err := os.ReadFile(filepath.Join(DataDir(), "issues.json")); err == nil {
		var issues []IssueEntry
		if json.Unmarshal(data, &issues) == nil {
			for _, i := range issues {
				if i.State == "closed" {
					closedIssues[fmt.Sprintf("%s#%d", i.Repo, i.IssueNumber)] = true
				}
			}
		}
	}

	kept := pending[:0]
	for _, p := range pending {
		if !repoExists[p.Repo] {
			continue
		}
		if bound := repoBound[p.Repo]; bound != "" && hostname != "" && bound != hostname {
			continue
		}
		if closedIssues[fmt.Sprintf("%s#%d", p.Repo, p.IssueNumber)] {
			continue
		}
		kept = append(kept, p)
	}

	removed := len(pending) - len(kept)
	if removed == 0 {
		return 0
	}
	_ = WritePending(kept)
	return removed
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

// DefaultQuietWindow is how long events.jsonl can sit untouched before a
// status="running" meta is considered orphaned. Live runs flush
// stream-json events every few hundred ms; if nothing has hit the
// file for a couple of minutes the runner process is gone (kill,
// SIGTERM, crash, lid close). Tuned to be longer than any normal
// inter-event gap (claude can pause ~30 s mid-tool-use) and shorter
// than the default per-operator timeout (60 min) so dashboard
// reflects reality long before the timeout-based path triggers.
var DefaultQuietWindow = 2 * time.Minute

// OperatorQuietWindows overrides DefaultQuietWindow for specific operators
// whose workloads routinely produce long gaps between stream events (e.g.
// running tests, large file writes, extended reasoning chains). Keys are
// operator names as stored in RunMeta.Operator (the SKILL.md filename stem).
var OperatorQuietWindows = map[string]time.Duration{
	// implement tasks can pause 5+ minutes during `go test`, large file
	// writes, or multi-step reasoning — 10 min avoids false-positive kills.
	"implement": 10 * time.Minute,
}

// quietWindowFor returns the quiet-window threshold for the given operator,
// falling back to DefaultQuietWindow when no override is registered.
func quietWindowFor(operator string) time.Duration {
	if d, ok := OperatorQuietWindows[operator]; ok {
		return d
	}
	return DefaultQuietWindow
}

// LogSink is the small subset of internal/log.Logger that the reconciler
// needs to record events. Defined here as an interface so the snapshot
// package doesn't import internal/log (which would be a cycle once any
// log call site reaches back into snapshot).
//
// The default is a no-op so production code that forgets to install a
// real sink still works. cmd/clawflow/commands/run.go and web.go install
// their logger right after Open.
type LogSink interface {
	Info(area string, kv ...any)
	Warn(area string, kv ...any)
	Error(area string, kv ...any)
}

type nopSink struct{}

func (nopSink) Info(string, ...any)  {}
func (nopSink) Warn(string, ...any)  {}
func (nopSink) Error(string, ...any) {}

// ReconcileLog is the sink the reconciler writes its diagnostic events to.
// Override at process startup; safe to leave nil-equivalent (noop default).
var ReconcileLog LogSink = nopSink{}

// runnerStillAlive reports whether the (repo, issue) lockfile is held by a
// running process whose start time matches metaStart closely enough to be
// the same run instance. Used by the reconciler to skip mis-flagging a
// healthy runner as dead just because events.jsonl went briefly quiet
// (long API retry, slow `go test`, `Task` subagent, etc.).
//
// Declared as a var so tests can stub it without spawning real processes.
var runnerStillAlive = func(repo string, issueNum int, metaStart time.Time) bool {
	info, err := ReadLock(LockPath(repo, issueNum))
	if err != nil {
		return false
	}
	if !processAlive(info.PID) {
		return false
	}
	// AcquireLock and the meta.json write happen back-to-back at runner
	// startup, so a same-run lock is sub-second off from metaStart. 30s
	// of slop tolerates slow filesystems while rejecting a brand-new run
	// for the same issue (which would have a much later StartedAt).
	delta := info.StartedAt.Sub(metaStart)
	if delta < 0 {
		delta = -delta
	}
	return delta < 30*time.Second
}

// extractTerminalResult scans events.jsonl for the last "type":"result" line
// and returns its subtype ("success" or "error") and the result text. If no
// result line exists, returns ("", "", nil). This lets the reconciler
// distinguish "runner died mid-stream" from "claude finished but runner
// crashed before writing final meta".
func extractTerminalResult(eventsPath string) (subtype string, resultText string, err error) {
	f, err := os.Open(eventsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	type resultLine struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Result  string `json:"result"`
	}
	var last *resultLine
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), `"type":"result"`) {
			continue
		}
		var ev resultLine
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Type != "result" {
			continue
		}
		copy := ev
		last = &copy
	}
	if err := sc.Err(); err != nil {
		return "", "", err
	}
	if last == nil {
		return "", "", nil
	}
	return last.Subtype, last.Result, nil
}

// ReconcileStaleRuns walks data/runs/* and patches up runs whose on-disk
// state is inconsistent with reality:
//
//   - meta.json with status="running" whose started_at is older than
//     `staleAfter`: rewrite to "failed". The runner exited (crash, kill,
//     SIGTERM) before finalizing the meta. Without this, the dashboard
//     would show a frozen "running" row forever.
//   - meta.json with status="running" whose events.jsonl hasn't been
//     written to in the operator's quiet window (DefaultQuietWindow for
//     most operators, longer for complex ones like "implement"): rewrite
//     to "failed" (interrupted process). Catches an interrupt well before
//     the full per-operator staleAfter timeout.
//   - run dir that contains events.jsonl but no meta.json: synthesize a
//     "failed" meta from the dir layout (repo / issue / timestamp) and
//     events.jsonl mtime. Possible when WriteRunMeta itself failed for
//     the initial running placeholder.
//
// Idempotent. Safe (and intended) to call at the top of every clawflow run
// AND at clawflow web startup. Returns the number of runs reconciled so
// the caller can log it.
//
// Also cleans up stale lockfiles (~/.clawflow/locks/) whose owner PID
// is no longer running, so crashed processes don't permanently block
// issues from being re-processed.
func ReconcileStaleRuns(staleAfter time.Duration) (int, error) {
	if n := CleanStaleLocks(); n > 0 {
		fmt.Fprintf(os.Stderr, "✓ cleaned %d stale lockfile(s)\n", n)
	}
	return reconcileStaleRunsAt(filepath.Join(DataDir(), "runs"), staleAfter)
}

// reconcileStaleRunsAt is the testable core of ReconcileStaleRuns. Tests
// pass an arbitrary runsRoot pointing at a tempdir layout so they don't
// mutate the user's actual ~/.clawflow tree.
func reconcileStaleRunsAt(runsRoot string, staleAfter time.Duration) (int, error) {
	if _, err := os.Stat(runsRoot); os.IsNotExist(err) {
		return 0, nil
	}
	cutoff := time.Now().UTC().Add(-staleAfter)
	var fixed int

	_ = filepath.WalkDir(runsRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		// A "run dir" is a leaf directory containing meta.json and/or
		// events.jsonl. Skip the intermediate runs/<repo>/issue-<N> tree.
		metaPath := filepath.Join(path, "meta.json")
		eventsPath := filepath.Join(path, "events.jsonl")
		_, metaErr := os.Stat(metaPath)
		_, eventsErr := os.Stat(eventsPath)
		hasMeta := metaErr == nil
		hasEvents := eventsErr == nil
		if !hasMeta && !hasEvents {
			return nil
		}

		if hasMeta {
			data, err := os.ReadFile(metaPath)
			if err != nil {
				return nil
			}
			var m RunMeta
			if err := json.Unmarshal(data, &m); err != nil {
				return nil
			}

			// A run stuck in "finalizing" means operator.Run() completed
			// (VCS side-effects done: comment posted, outcome label applied,
			// trigger labels removed) but the process was killed before
			// WriteRunMeta could record the terminal status. The issue will
			// not re-trigger because its trigger labels are already gone.
			// Just backfill usage and promote to "success".
			if m.Status == "finalizing" {
				if !runnerStillAlive(m.Repo, m.IssueNumber, m.StartedAt) {
					if u, uErr := ExtractUsage(eventsPath); uErr == nil && u != nil {
						m.Usage = u
					}
					m.Status = "success"
					if m.EndedAt == nil || m.EndedAt.IsZero() {
						t := time.Now().UTC()
						m.EndedAt = &t
					}
					m.Error = "reconciled: runner exited after finalizing VCS side-effects but before writing terminal meta"
					if err := WriteRunMeta(path, m); err == nil {
						fixed++
					}
				}
				return nil
			}

			if m.Status != "running" {
				return nil
			}
			// A run is orphaned if EITHER it's been "running" longer than
			// the per-operator timeout OR its events.jsonl has gone quiet.
			// The events-quiet check fires much faster than staleAfter and
			// matches the actual failure mode users hit — they Ctrl-C or
			// kill the process and want the dashboard to reflect that
			// without waiting an hour. The threshold is per-operator so
			// long-running operators (e.g. "implement") get a wider window.
			qw := quietWindowFor(m.Operator)
			tooOld := !m.StartedAt.After(cutoff)
			eventsQuiet := false
			var lastTouch time.Time
			if st, err := os.Stat(eventsPath); err == nil {
				lastTouch = st.ModTime().UTC()
				eventsQuiet = time.Since(lastTouch) > qw
			} else {
				// No events.jsonl at all: a healthy runner opens the
				// file and writes claude's `system-init` event within
				// seconds of starting, so anything past the quiet window
				// with no file means the runner died before launching
				// claude — flag without waiting the full staleAfter
				// timeout (default 1h) to land here.
				eventsQuiet = time.Since(m.StartedAt) > qw
			}
			if !tooOld && !eventsQuiet {
				return nil
			}
			// events.jsonl can stall past the quiet window even on a healthy
			// runner — claude's API retries on upstream 5xx, a long
			// `go test` or `Task` subagent call, etc. Before declaring
			// the runner dead, consult the lockfile written by
			// AcquireLock: it carries the runner's PID and start time.
			// A live PID whose lock matches this meta's StartedAt means
			// the runner is just temporarily quiet, not gone.
			if runnerStillAlive(m.Repo, m.IssueNumber, m.StartedAt) {
				// Diagnostic crumb so the user can later confirm "this
				// run wasn't reconciled because we saw the lock". The
				// stale-lock and stale-events story used to be an
				// invisible race that misreported live runs as failed.
				quietFor := time.Duration(0)
				if !lastTouch.IsZero() {
					quietFor = time.Since(lastTouch).Round(time.Second)
				}
				ReconcileLog.Info("web/reconcile_skipped",
					"repo", m.Repo,
					"issue", m.IssueNumber,
					"op", m.Operator,
					"reason", "runner_alive",
					"quiet_for", quietFor,
				)
				return nil
			}
			// Before marking as failed, check whether claude actually
			// completed successfully. The runner may have crashed after
			// claude finished but before it could write the final meta.
			// In that case we should reconcile as "success" not "failed".
			termSubtype, termResult, _ := extractTerminalResult(eventsPath)
			if termSubtype == "success" {
				m.Status = "success"
				m.Summary = termResult
				m.Error = "reconciled: runner exited after claude completed successfully but before finalizing meta"
				// Backfill usage from the result event so the dashboard
				// shows cost/tokens even though the runner never wrote them.
				if u, uErr := ExtractUsage(eventsPath); uErr == nil && u != nil {
					m.Usage = u
				}
			} else {
				m.Status = "failed"
				switch {
				case tooOld && eventsQuiet:
					m.Error = fmt.Sprintf(
						"reconciled: stuck in running for >%s and events.jsonl quiet for >%s; runner is gone",
						staleAfter, qw,
					)
				case tooOld:
					m.Error = fmt.Sprintf(
						"reconciled: stuck in running for >%s; runner exited without finalizing this run",
						staleAfter,
					)
				default:
					m.Error = fmt.Sprintf(
						"reconciled: events.jsonl quiet for >%s and runner PID is dead/missing (interrupted/killed)",
						qw,
					)
				}
			}
			// Treat both nil and a pointer to Go's zero time as "missing":
			// older meta.json on disk (pre-pointer-type fix) literally
			// contains "0001-01-01T00:00:00Z", which unmarshals into a
			// non-nil zero-value pointer. Reconciliation should still
			// stamp a real EndedAt in that case.
			if m.EndedAt == nil || m.EndedAt.IsZero() {
				if !lastTouch.IsZero() {
					t := lastTouch
					m.EndedAt = &t
				} else {
					t := time.Now().UTC()
					m.EndedAt = &t
				}
			}
			if err := WriteRunMeta(path, m); err == nil {
				fixed++
			}
			return nil
		}

		// hasEvents && !hasMeta — synthesize a meta from the dir layout.
		// Layout: <runsRoot>/<repo-slug>/issue-<N>/<ts>/
		rel, err := filepath.Rel(runsRoot, path)
		if err != nil {
			return nil
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 3 {
			return nil
		}
		repoSlug, issueDir, ts := parts[0], parts[1], parts[2]
		repo := strings.ReplaceAll(repoSlug, "__", "/")
		var issueNum int
		if _, err := fmt.Sscanf(issueDir, "issue-%d", &issueNum); err != nil {
			return nil
		}
		startedAt, err := time.Parse("2006-01-02T15-04-05Z", ts)
		if err != nil {
			return nil
		}
		endedAt := startedAt
		if st, err := os.Stat(eventsPath); err == nil {
			endedAt = st.ModTime().UTC()
		}
		m := RunMeta{
			Operator:    "(unknown)",
			Repo:        repo,
			IssueNumber: issueNum,
			StartedAt:   startedAt,
			EndedAt:     &endedAt,
			Status:      "failed",
			Error:       "reconciled: meta.json missing; runner exited before writing initial state",
		}
		if err := WriteRunMeta(path, m); err == nil {
			fixed++
		}
		return nil
	})
	return fixed, nil
}

// ConsecutiveFailures counts how many of the most recent runs for a given
// (repo, issue) ended with status "failed", stopping at the first non-failed
// run. This powers the circuit breaker: after N consecutive failures the
// runner auto-labels the issue `agent-failed` to stop retrying.
func ConsecutiveFailures(repo string, issueNum int) int {
	slug := strings.ReplaceAll(repo, "/", "__")
	issueDir := filepath.Join(DataDir(), "runs", slug, fmt.Sprintf("issue-%d", issueNum))
	if _, err := os.Stat(issueDir); os.IsNotExist(err) {
		return 0
	}

	// Each run is a subdirectory named by timestamp. Walk them, collect
	// meta.json, sort newest-first, then count leading failures.
	type runEntry struct {
		startedAt time.Time
		status    string
	}
	var runs []runEntry
	entries, err := os.ReadDir(issueDir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		metaPath := filepath.Join(issueDir, e.Name(), "meta.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var m RunMeta
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		if m.Status == "running" || m.Status == "finalizing" {
			continue
		}
		runs = append(runs, runEntry{startedAt: m.StartedAt, status: m.Status})
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].startedAt.After(runs[j].startedAt)
	})
	count := 0
	for _, r := range runs {
		if r.status != "failed" && r.status != "no-marker" && r.status != "skipped-empty" {
			break
		}
		count++
	}
	return count
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
		if m.Usage == nil && m.Status != "" && m.Status != "running" && m.Status != "finalizing" {
			runDir := filepath.Dir(path)
			if u, err := ExtractUsage(filepath.Join(runDir, "events.jsonl")); err == nil && u != nil {
				m.Usage = u
				_ = WriteRunMeta(runDir, m)
			}
		}
		relDir := strings.TrimPrefix(filepath.Dir(path), DataDir())
		entry := RunIndexEntry{
			RunMeta: m,
			Path:    "./data" + filepath.ToSlash(relDir) + "/",
		}
		// For running rows, surface whether the lockfile's PID is alive
		// at index time. The dashboard renders this as a green "live"
		// badge so users can distinguish "actively running, just quietly
		// retrying upstream" from "frozen, will be reconciled soon".
		if (m.Status == "running" || m.Status == "finalizing") && runnerStillAlive(m.Repo, m.IssueNumber, m.StartedAt) {
			alive := true
			entry.RunnerAlive = &alive
		}
		out = append(out, entry)
		return nil
	})
	return out
}

// ProjectView is the dashboard-facing view of one project.
type ProjectView struct {
	Name       string                `json:"name"`
	Repos      []string              `json:"repos"`
	Context    string                `json:"context_md,omitempty"`
	Testing    string                `json:"testing_md,omitempty"`
	Deployment string                `json:"deployment_md,omitempty"`
	Automation ProjectAutomationView `json:"automation"`
	CreatedAt  string                `json:"created_at"`
	UpdatedAt  string                `json:"updated_at"`
}

// ProjectAutomationView mirrors project.Automation for the dashboard.
// Always emitted (even when disabled) so the frontend can render the
// toggle in its current state without conditional access.
type ProjectAutomationView struct {
	Enabled         bool   `json:"enabled"`
	CooldownMinutes int    `json:"cooldown_minutes"`
	LastWokenAt     string `json:"last_woken_at,omitempty"`
	BoundMachine    string `json:"bound_machine,omitempty"`
	LastSkipReason  string `json:"last_skip_reason,omitempty"`
	LastSkipAt      string `json:"last_skip_at,omitempty"`
}

// WriteProjects writes data/projects.json from the on-disk project store.
func WriteProjects() error {
	projects, err := project.List()
	if err != nil {
		// No projects dir yet — write empty array.
		if os.IsNotExist(err) {
			return writeJSON(filepath.Join(DataDir(), "projects.json"), []ProjectView{})
		}
		return err
	}
	views := make([]ProjectView, 0, len(projects))
	for _, p := range projects {
		ctx, _ := project.ReadContext(p.Name)
		testing, _ := project.ReadTesting(p.Name)
		deployment, _ := project.ReadDeployment(p.Name)
		views = append(views, ProjectView{
			Name:       p.Name,
			Repos:      p.Repos,
			Context:    ctx,
			Testing:    testing,
			Deployment: deployment,
			Automation: ProjectAutomationView{
				Enabled:         p.Automation.Enabled,
				CooldownMinutes: p.Automation.CooldownMinutes,
				LastWokenAt:     p.Automation.LastWokenAt,
				BoundMachine:    p.Automation.BoundMachine,
				LastSkipReason:  p.Automation.LastSkipReason,
				LastSkipAt:      p.Automation.LastSkipAt,
			},
			CreatedAt: p.CreatedAt,
			UpdatedAt: p.UpdatedAt,
		})
	}
	return writeJSON(filepath.Join(DataDir(), "projects.json"), views)
}

// --- Pilot run snapshot ---

// PilotRunMeta describes one Pilot wake. Stored alongside operator
// runs so the dashboard can show Pilot activity on the project detail page.
//
// Duties is the structured, duty-shaped view of what Pilot did this
// wake — populated by parsing the YAML front-matter block at the top
// of Pilot's stdout. Absent on legacy / failed-to-parse runs, in which
// case the UI falls back to the free-form Summary.
type PilotRunMeta struct {
	Project   string        `json:"project"`
	StartedAt time.Time     `json:"started_at"`
	EndedAt   *time.Time    `json:"ended_at,omitempty"`
	Status    string        `json:"status"` // "running", "success", "failed"
	Result    string        `json:"result,omitempty"`
	Error     string        `json:"error,omitempty"`
	Summary   string        `json:"summary,omitempty"`
	Usage     *Usage        `json:"usage,omitempty"`
	Duties    *PilotDuties  `json:"duties,omitempty"`
	Verdict   string        `json:"verdict,omitempty"`
}

// PilotDuty is the shape every actionable duty (pr_triage, monitoring,
// doc_sync, backlog_hygiene) returns. Status is one of:
//
//	"ok"           — checked, nothing to do
//	"action_taken" — checked, did something (Actions populated)
//	"flagged"      — checked, found something a human should see (Note populated)
//	"error"        — could not check (Note carries the reason)
type PilotDuty struct {
	Status  string   `json:"status"`
	Actions []string `json:"actions,omitempty"`
	Note    string   `json:"note,omitempty"`
}

// PilotIssueDigest is the passive-review duty: a human-readable
// summary of recent issue activity. Counts are computed by the runner
// from issues.json / runs.json (Pilot is not asked to count), so they
// are always accurate; Summary is the prose Pilot writes.
type PilotIssueDigest struct {
	SinceHours int    `json:"since_hours"` // window the summary covers (typically 24)
	New        int    `json:"new"`
	Closed     int    `json:"closed"`
	Labeled    int    `json:"labeled"`
	Commented  int    `json:"commented"`
	Summary    string `json:"summary,omitempty"`
}

// PilotDuties is the full duty rollup for one wake. Each field maps
// to one of Pilot's standing responsibilities — see internal/chat
// pilot prompt for the contract Pilot agrees to honour.
type PilotDuties struct {
	PRTriage        PilotDuty        `json:"pr_triage"`
	Monitoring      PilotDuty        `json:"monitoring"`
	DocSync         PilotDuty        `json:"doc_sync"`
	IssueDigest     PilotIssueDigest `json:"issue_digest"`
	BacklogHygiene  PilotDuty        `json:"backlog_hygiene"`
}

// PilotRunDir returns the directory for a single Pilot run.
func PilotRunDir(projectName string, startedAt time.Time) string {
	return filepath.Join(
		DataDir(),
		"pilot-runs",
		projectName,
		startedAt.UTC().Format("2006-01-02T15-04-05Z"),
	)
}

// WritePilotRunMeta writes meta.json inside a Pilot run directory.
func WritePilotRunMeta(runDir string, m PilotRunMeta) error {
	if m.StartedAt.IsZero() {
		m.StartedAt = time.Now().UTC()
	}
	return writeJSON(filepath.Join(runDir, "meta.json"), m)
}

// PilotRunIndexEntry is a flattened row for the dashboard's Pilot runs list.
type PilotRunIndexEntry struct {
	PilotRunMeta
	Path string `json:"path"`
}

// WritePilotRunsIndex walks data/pilot-runs/* and writes data/pilot-runs.json.
func WritePilotRunsIndex(limit int) ([]PilotRunIndexEntry, error) {
	root := filepath.Join(DataDir(), "pilot-runs")
	entries := collectPilotRunEntries(root)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].StartedAt.After(entries[j].StartedAt)
	})
	indexed := entries
	if limit > 0 && len(indexed) > limit {
		indexed = indexed[:limit]
	}
	if err := writeJSON(filepath.Join(DataDir(), "pilot-runs.json"), indexed); err != nil {
		return entries, err
	}
	return entries, nil
}

// LastPilotRunSummaries returns the most recent n PilotRunMeta entries
// for a given project. Reads only meta.json files (no events.jsonl).
// Summary field is truncated to 500 chars to keep prompt size bounded.
func LastPilotRunSummaries(projectName string, n int) []PilotRunMeta {
	root := filepath.Join(DataDir(), "pilot-runs", projectName)
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	var dirs []string
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	if len(dirs) > n {
		dirs = dirs[:n]
	}
	var out []PilotRunMeta
	for _, d := range dirs {
		data, err := os.ReadFile(filepath.Join(root, d, "meta.json"))
		if err != nil {
			continue
		}
		var m PilotRunMeta
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		if len(m.Summary) > 500 {
			m.Summary = m.Summary[:500] + "…"
		}
		out = append(out, m)
	}
	return out
}

func collectPilotRunEntries(root string) []PilotRunIndexEntry {
	out := []PilotRunIndexEntry{}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return out
	}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "meta.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var m PilotRunMeta
		if err := json.Unmarshal(data, &m); err != nil {
			return nil
		}
		if m.Usage == nil && m.Status != "" && m.Status != "running" && m.Status != "finalizing" {
			runDir := filepath.Dir(path)
			if u, err := ExtractUsage(filepath.Join(runDir, "events.jsonl")); err == nil && u != nil {
				m.Usage = u
				_ = WritePilotRunMeta(runDir, m)
			}
		}
		relDir := strings.TrimPrefix(filepath.Dir(path), DataDir())
		out = append(out, PilotRunIndexEntry{
			PilotRunMeta: m,
			Path:      "./data" + filepath.ToSlash(relDir) + "/",
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
	// Write to a temp file in the same directory, then rename atomically.
	// os.Rename on the same filesystem is atomic on POSIX — the target
	// either has the old content or the new content, never a partial write.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op if rename succeeded
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	return os.Rename(tmpName, path)
}
