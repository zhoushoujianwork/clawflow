package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// UsageCoverage counts reported values, including measured zero, per finished attempt.
type UsageCoverage struct {
	Cost       int `json:"cost"`
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
	Tokens     int `json:"tokens"`
}

// Nullable totals distinguish unreported usage from measured zero.
type UsageAggregate struct {
	Coverage                  UsageCoverage `json:"coverage"`
	EstimatedCostRuns         int           `json:"estimated_cost_runs"`
	ReportedCostRuns          int           `json:"reported_cost_runs"`
	CostProvenanceUnknownRuns int           `json:"cost_provenance_unknown_runs"`
	TotalTokens               *int64        `json:"total_tokens"`
	SubscriptionRuns          int           `json:"subscription_runs,omitempty"`
	Runs                      int           `json:"runs"`
	TotalCostUSD              *float64      `json:"total_cost_usd"`
	InputTokens               *int64        `json:"input_tokens"`
	OutputTokens              *int64        `json:"output_tokens"`
	CacheReadInputTokens      *int64        `json:"cache_read_input_tokens"`
	CacheCreationInputTokens  *int64        `json:"cache_creation_input_tokens"`
	DurationMs                int64         `json:"duration_ms"`
}
type ModelAggregate struct {
	UsageAggregate
	CostUSD *float64 `json:"cost_usd"`
}
type UsageBreakdown struct {
	ByProject  map[string]UsageAggregate `json:"by_project"`
	ByRole     map[string]UsageAggregate `json:"by_role"`
	ByProfile  map[string]UsageAggregate `json:"by_profile"`
	Totals     UsageAggregate            `json:"totals"`
	ByRepo     map[string]UsageAggregate `json:"by_repo"`
	ByOperator map[string]UsageAggregate `json:"by_operator"`
	ByExecutor map[string]UsageAggregate `json:"by_executor"`
	ByMachine  map[string]UsageAggregate `json:"by_machine"`
	ByModel    map[string]ModelAggregate `json:"by_model"`
}
type DailyUsage struct {
	Date string `json:"date"`
	UsageAggregate
	UsageBreakdown
}
type UsagePeriod struct {
	PeriodStart string `json:"period_start"`
	PeriodEnd   string `json:"period_end"`
	UsageBreakdown
	DailyTrend []DailyUsage `json:"daily_trend"`
}
type UsageSummary struct {
	RangeStart   *time.Time     `json:"range_start"`
	RangeEnd     *time.Time     `json:"range_end"`
	History      []UsageHistory `json:"history"`
	HistoryLimit int            `json:"history_limit"`
	GeneratedAt  time.Time      `json:"generated_at"`
	UsageBreakdown
	Periods []UsagePeriod `json:"periods"`
}

// UsageSelection adds presentation-only filters without changing run history contracts.
// A model selects whole attempts containing that model, including their other models.
type UsageSelection struct{ Machine, Model string }
type UsageHistory struct {
	StartedAt time.Time `json:"started_at"`
	Repo      string    `json:"repo"`
	Project   string    `json:"project"`
	Operator  string    `json:"operator"`
	Status    string    `json:"status"`
	Href      string    `json:"href,omitempty"`
}

func newBreakdown() UsageBreakdown {
	return UsageBreakdown{ByProject: map[string]UsageAggregate{}, ByRole: map[string]UsageAggregate{}, ByProfile: map[string]UsageAggregate{}, ByRepo: map[string]UsageAggregate{}, ByOperator: map[string]UsageAggregate{}, ByExecutor: map[string]UsageAggregate{}, ByMachine: map[string]UsageAggregate{}, ByModel: map[string]ModelAggregate{}}
}
func addInt(a **int64, b *int64) {
	if b != nil {
		if *a == nil {
			*a = new(int64)
		}
		**a += *b
	}
}
func addFloat(a **float64, b *float64) {
	if b != nil {
		if *a == nil {
			*a = new(float64)
		}
		**a += *b
	}
}
func (a *UsageAggregate) add(r Run) {
	a.Runs++
	// The metadata payload already persists cost basis; no schema/backfill is needed.
	var meta struct {
		Usage struct {
			CostBasis      string `json:"cost_basis"`
			CostIsEstimate *bool  `json:"cost_is_estimate"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(r.Payload), &meta) == nil && meta.Usage.CostBasis == "subscription" {
		a.SubscriptionRuns++
	}
	if r.CostUSD != nil {
		a.Coverage.Cost++
		if meta.Usage.CostIsEstimate != nil && *meta.Usage.CostIsEstimate {
			a.EstimatedCostRuns++
		} else if meta.Usage.CostBasis != "" {
			a.ReportedCostRuns++
		} else {
			a.CostProvenanceUnknownRuns++
		}
	}
	for _, metric := range []struct {
		value *int64
		count *int
	}{
		{r.InputTokens, &a.Coverage.Input}, {r.OutputTokens, &a.Coverage.Output},
		{r.CacheReadInputTokens, &a.Coverage.CacheRead}, {r.CacheCreationInputTokens, &a.Coverage.CacheWrite},
	} {
		if metric.value != nil {
			(*metric.count)++
		}
	}
	// Store input is exclusive (the #474 conversion boundary). Only complete
	// per-attempt components contribute to the inclusive token total.
	if r.InputTokens != nil && r.OutputTokens != nil && r.CacheReadInputTokens != nil && r.CacheCreationInputTokens != nil {
		total := *r.InputTokens + *r.OutputTokens + *r.CacheReadInputTokens + *r.CacheCreationInputTokens
		addInt(&a.TotalTokens, &total)
		a.Coverage.Tokens++
	}
	a.DurationMs += r.DurationMs
	addFloat(&a.TotalCostUSD, r.CostUSD)
	addInt(&a.InputTokens, r.InputTokens)
	addInt(&a.OutputTokens, r.OutputTokens)
	addInt(&a.CacheReadInputTokens, r.CacheReadInputTokens)
	addInt(&a.CacheCreationInputTokens, r.CacheCreationInputTokens)
}
func usageRole(r Run) string {
	var m struct {
		Role               string `json:"role"`
		DevelopmentSession struct {
			Role string `json:"role"`
		} `json:"development_session"`
	}
	_ = json.Unmarshal([]byte(r.Payload), &m)
	if m.Role == "" {
		m.Role = m.DevelopmentSession.Role
	}
	return unknown(m.Role)
}

func (b *UsageBreakdown) add(r Run, models []RunModelUsage) {
	b.Totals.add(r)
	for _, group := range []struct {
		m map[string]UsageAggregate
		k string
	}{{b.ByProject, unknown(r.Project)}, {b.ByRole, usageRole(r)}, {b.ByProfile, unknown(r.Profile)}, {b.ByRepo, r.Repo}, {b.ByOperator, r.Operator}, {b.ByExecutor, r.Profile + "/" + r.Backend}, {b.ByMachine, r.Machine}} {
		if group.k == "" {
			continue
		}
		a := group.m[group.k]
		a.add(r)
		group.m[group.k] = a
	}
	if len(models) == 0 {
		models = []RunModelUsage{{Model: r.Model, CostUSD: r.CostUSD, InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CacheReadInputTokens: r.CacheReadInputTokens, CacheCreationInputTokens: r.CacheCreationInputTokens}}
	}
	// Attribute the full run duration to its sole model, or the model with the
	// most output tokens. Missing tokens count as zero; ties use the model name.
	durationModel := 0
	outputTokens := func(m RunModelUsage) int64 {
		if m.OutputTokens == nil {
			return 0
		}
		return *m.OutputTokens
	}
	for i, m := range models {
		best := models[durationModel]
		if outputTokens(m) > outputTokens(best) || (outputTokens(m) == outputTokens(best) && m.Model < best.Model) {
			durationModel = i
		}
	}
	for i, m := range models {
		var duration int64
		if i == durationModel {
			duration = r.DurationMs
		}
		a := b.ByModel[m.Model]
		a.UsageAggregate.add(Run{Payload: r.Payload, DurationMs: duration, CostUSD: m.CostUSD, InputTokens: m.InputTokens, OutputTokens: m.OutputTokens, CacheReadInputTokens: m.CacheReadInputTokens, CacheCreationInputTokens: m.CacheCreationInputTokens})
		a.CostUSD = a.TotalCostUSD
		b.ByModel[m.Model] = a
	}
}

// BillingPeriod uses UTC and an exclusive upper bound.
func BillingPeriod(t time.Time, day int) (time.Time, time.Time) {
	if day < 1 || day > 28 {
		day = 1
	}
	t = t.UTC()
	y, m, _ := t.Date()
	if t.Day() < day {
		m--
	}
	start := time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}

// Usage reads only database rows. In-flight rows are excluded before aggregation,
// while every finished row contributes to counts even when no usage was reported.
func (s *Store) Usage(ctx context.Context, f RunFilter, day int, selection ...UsageSelection) (UsageSummary, error) {
	sum := UsageSummary{GeneratedAt: time.Now().UTC(), UsageBreakdown: newBreakdown(), Periods: []UsagePeriod{}, RangeStart: f.From, RangeEnd: f.Until, History: []UsageHistory{}, HistoryLimit: 50}
	var selected UsageSelection
	if len(selection) > 0 {
		selected = selection[0]
	}
	// Legacy runs have no project attribution. Never infer it from current membership.
	missingProject := f.Project == "unknown"
	if missingProject {
		f.Project = ""
	}
	f.Finished = true
	f.Limit = 0
	periods := map[string]*UsagePeriod{}
	days := map[string]map[string]*DailyUsage{}
	for _, kind := range []string{"run", "pilot"} {
		rows, err := s.Runs(ctx, kind, f)
		if err != nil {
			return sum, err
		}
		if len(rows) == 0 {
			continue
		}
		// Join against the filtered run table to avoid unbounded SQL parameter lists.
		table := "runs"
		if kind == "pilot" {
			table = "pilot_runs"
		}
		query := s.db.WithContext(ctx).Table(table).Select("run_dir").Where("status NOT IN ?", []string{"running", "finalizing", "pending", "queued"})
		if f.Repo != "" {
			query = query.Where("repo = ?", f.Repo)
		}
		if f.Project != "" {
			query = query.Where("project = ?", f.Project)
		}
		if f.From != nil {
			query = query.Where("started_at >= ?", *f.From)
		}
		if f.Until != nil {
			query = query.Where("started_at < ?", *f.Until)
		}
		var modelRows []RunModelUsage
		if err := s.db.WithContext(ctx).Where("kind = ? AND run_dir IN (?)", kind, query).Find(&modelRows).Error; err != nil {
			return sum, err
		}
		models := map[string][]RunModelUsage{}
		for _, m := range modelRows {
			models[m.RunDir] = append(models[m.RunDir], m)
		}
		for _, r := range rows {
			if missingProject && unknown(r.Project) != "unknown" {
				continue
			}
			ms := models[r.RunDir]
			if selected.Machine != "" && r.Machine != selected.Machine {
				continue
			}
			if selected.Model != "" {
				matches := len(ms) == 0 && r.Model == selected.Model
				for _, m := range ms {
					if m.Model == selected.Model {
						matches = true
					}
				}
				if !matches {
					continue
				}
			}
			if f.From == nil && (sum.RangeStart == nil || r.StartedAt.Before(*sum.RangeStart)) {
				t := r.StartedAt.UTC().Truncate(24 * time.Hour)
				sum.RangeStart = &t
			}
			if f.Until == nil && (sum.RangeEnd == nil || !r.StartedAt.Before(*sum.RangeEnd)) {
				t := r.StartedAt.UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
				sum.RangeEnd = &t
			}
			h := UsageHistory{StartedAt: r.StartedAt, Repo: r.Repo, Project: r.Project, Operator: r.Operator, Status: r.Status}
			// Link only known existing run routes; never return local absolute paths or bodies.
			if kind == "run" {
				ts := filepath.Base(r.RunDir)
				issue := filepath.Base(filepath.Dir(r.RunDir))
				slug := filepath.Base(filepath.Dir(filepath.Dir(r.RunDir)))
				if issue == fmt.Sprintf("issue-%d", r.IssueNumber) && slug == strings.ReplaceAll(r.Repo, "/", "__") {
					h.Href = "/runs/" + url.PathEscape(slug) + "/" + url.PathEscape(issue) + "/" + url.PathEscape(ts)
				}
			} else {
				h.Href = "/projects/" + url.PathEscape(r.Project) + "/pilot-runs"
			}
			sum.History = append(sum.History, h)
			sum.UsageBreakdown.add(r, ms)
			start, end := BillingPeriod(r.StartedAt, day)
			key := start.Format("2006-01-02")
			p := periods[key]
			if p == nil {
				p = &UsagePeriod{PeriodStart: key, PeriodEnd: end.AddDate(0, 0, -1).Format("2006-01-02"), UsageBreakdown: newBreakdown(), DailyTrend: []DailyUsage{}}
				periods[key] = p
				days[key] = map[string]*DailyUsage{}
				for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
					date := d.Format("2006-01-02")
					days[key][date] = &DailyUsage{Date: date, UsageBreakdown: newBreakdown()}
				}
			}
			p.UsageBreakdown.add(r, ms)
			d := days[key][r.StartedAt.UTC().Format("2006-01-02")]
			d.UsageAggregate.add(r)
			d.UsageBreakdown.add(r, ms)
		}
	}
	sort.SliceStable(sum.History, func(i, j int) bool {
		a, b := sum.History[i], sum.History[j]
		if a.StartedAt.Equal(b.StartedAt) {
			return a.Href < b.Href
		}
		return a.StartedAt.After(b.StartedAt)
	})
	if len(sum.History) > sum.HistoryLimit {
		sum.History = sum.History[:sum.HistoryLimit]
	}
	for key, p := range periods {
		for _, d := range days[key] {
			p.DailyTrend = append(p.DailyTrend, *d)
		}
		sort.Slice(p.DailyTrend, func(i, j int) bool { return p.DailyTrend[i].Date < p.DailyTrend[j].Date })
		sum.Periods = append(sum.Periods, *p)
	}
	sort.Slice(sum.Periods, func(i, j int) bool { return sum.Periods[i].PeriodStart > sum.Periods[j].PeriodStart })
	return sum, nil
}
