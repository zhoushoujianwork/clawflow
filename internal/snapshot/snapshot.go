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
	AutoApprove  bool   `json:"auto_approve"`
	AutoMerge    bool   `json:"auto_merge"`
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
	// EndedAt is a pointer so a nil value omits the JSON key for a still-
	// running run. With a value type, Go's `omitempty` does NOT skip a zero
	// time.Time (`omitempty` only skips nil/0/""/empty), so the wire format
	// would carry "0001-01-01T00:00:00Z" and the dashboard's duration math
	// would render it as a giant negative offset.
	EndedAt     *time.Time `json:"ended_at,omitempty"`
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
			FullName:   name,
			Platform:   r.Platform,
			BaseURL:    r.BaseURL,
			BaseBranch: r.BaseBranch,
			LocalPath:  r.LocalPath,
			Enabled:    r.Enabled,
			AutoApprove:  r.AutoApprove,
			AutoMerge:    r.AutoMerge,
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


// IssueEntry represents a single issue from the repository, regardless of
// whether it has pending operators. Used by the dashboard to show all issues
// with their current state and labels.
type IssueEntry struct {
	Repo        string    `json:"repo"`
	IssueNumber int       `json:"issue_number"`
	IssueTitle  string    `json:"issue_title,omitempty"`
	Labels      []string  `json:"labels,omitempty"`
	State       string    `json:"state"` // "open" | "closed"
	CapturedAt  time.Time `json:"captured_at"`
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

// quietWindow is how long events.jsonl can sit untouched before a
// status="running" meta is considered orphaned. Live runs flush
// stream-json events every few hundred ms; if nothing has hit the
// file for a couple of minutes the runner process is gone (kill,
// SIGTERM, crash, lid close). Tuned to be longer than any normal
// inter-event gap (claude can pause ~30 s mid-tool-use) and shorter
// than the default per-operator timeout (60 min) so dashboard
// reflects reality long before the timeout-based path triggers.
const quietWindow = 2 * time.Minute

// ReconcileStaleRuns walks data/runs/* and patches up runs whose on-disk
// state is inconsistent with reality:
//
//   - meta.json with status="running" whose started_at is older than
//     `staleAfter`: rewrite to "failed". The runner exited (crash, kill,
//     SIGTERM) before finalizing the meta. Without this, the dashboard
//     would show a frozen "running" row forever.
//   - meta.json with status="running" whose events.jsonl hasn't been
//     written to in `quietWindow`: also rewrite to "failed" (interrupted
//     process). Catches an interrupt within 2 min instead of waiting
//     out the full per-operator timeout.
//   - run dir that contains events.jsonl but no meta.json: synthesize a
//     "failed" meta from the dir layout (repo / issue / timestamp) and
//     events.jsonl mtime. Possible when WriteRunMeta itself failed for
//     the initial running placeholder.
//
// Idempotent. Safe (and intended) to call at the top of every clawflow run
// AND at clawflow web startup. Returns the number of runs reconciled so
// the caller can log it.
//
// Does NOT touch any VCS state — the lock label on the corresponding issue
// is left alone. Removing it would silently re-enable the operator to
// trigger again, which is a recovery decision the user should make
// explicitly (by deleting the lock label themselves once they've
// investigated the stuck run).
func ReconcileStaleRuns(staleAfter time.Duration) (int, error) {
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
			if m.Status != "running" {
				return nil
			}
			// A run is orphaned if EITHER it's been "running" longer than
			// the per-operator timeout OR its events.jsonl has gone quiet.
			// The events-quiet check fires much faster (2 min vs 60 min)
			// and matches the actual failure mode users hit — they Ctrl-C
			// or kill the process and want the dashboard to reflect that
			// without waiting an hour.
			tooOld := !m.StartedAt.After(cutoff)
			eventsQuiet := false
			var lastTouch time.Time
			if st, err := os.Stat(eventsPath); err == nil {
				lastTouch = st.ModTime().UTC()
				eventsQuiet = time.Since(lastTouch) > quietWindow
			} else {
				// No events.jsonl at all: a healthy runner opens the
				// file and writes claude's `system-init` event within
				// seconds of starting, so anything past quietWindow
				// (2 min) with no file means the runner died before
				// launching claude — flag without waiting the full
				// staleAfter timeout (default 1h) to land here.
				eventsQuiet = time.Since(m.StartedAt) > quietWindow
			}
			if !tooOld && !eventsQuiet {
				return nil
			}
			m.Status = "failed"
			switch {
			case tooOld && eventsQuiet:
				m.Error = fmt.Sprintf(
					"reconciled: stuck in running for >%s and events.jsonl quiet for >%s; runner is gone",
					staleAfter, quietWindow,
				)
			case tooOld:
				m.Error = fmt.Sprintf(
					"reconciled: stuck in running for >%s; runner exited without finalizing this run",
					staleAfter,
				)
			default:
				m.Error = fmt.Sprintf(
					"reconciled: events.jsonl quiet for >%s while status=running; runner process is gone (interrupted/killed)",
					quietWindow,
				)
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
