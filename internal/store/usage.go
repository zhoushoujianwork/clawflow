package store

import (
	"context"
	"encoding/json"
	"sort"
	"time"
)

// Nullable totals distinguish unreported usage from measured zero.
type UsageAggregate struct {
	SubscriptionRuns         int      `json:"subscription_runs,omitempty"`
	Runs                     int      `json:"runs"`
	TotalCostUSD             *float64 `json:"total_cost_usd"`
	InputTokens              *int64   `json:"input_tokens"`
	OutputTokens             *int64   `json:"output_tokens"`
	CacheReadInputTokens     *int64   `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int64   `json:"cache_creation_input_tokens"`
	DurationMs               int64    `json:"duration_ms"`
}
type ModelAggregate struct {
	UsageAggregate
	CostUSD *float64 `json:"cost_usd"`
}
type UsageBreakdown struct {
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
	GeneratedAt time.Time `json:"generated_at"`
	UsageBreakdown
	Periods []UsagePeriod `json:"periods"`
}

func newBreakdown() UsageBreakdown {
	return UsageBreakdown{ByRepo: map[string]UsageAggregate{}, ByOperator: map[string]UsageAggregate{}, ByExecutor: map[string]UsageAggregate{}, ByMachine: map[string]UsageAggregate{}, ByModel: map[string]ModelAggregate{}}
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
			CostBasis string `json:"cost_basis"`
		} `json:"usage"`
	}
	if json.Unmarshal([]byte(r.Payload), &meta) == nil && meta.Usage.CostBasis == "subscription" {
		a.SubscriptionRuns++
	}
	a.DurationMs += r.DurationMs
	addFloat(&a.TotalCostUSD, r.CostUSD)
	addInt(&a.InputTokens, r.InputTokens)
	addInt(&a.OutputTokens, r.OutputTokens)
	addInt(&a.CacheReadInputTokens, r.CacheReadInputTokens)
	addInt(&a.CacheCreationInputTokens, r.CacheCreationInputTokens)
}
func (b *UsageBreakdown) add(r Run, models []RunModelUsage) {
	b.Totals.add(r)
	for _, group := range []struct {
		m map[string]UsageAggregate
		k string
	}{{b.ByRepo, r.Repo}, {b.ByOperator, r.Operator}, {b.ByExecutor, r.Profile + "/" + r.Backend}, {b.ByMachine, r.Machine}} {
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
	for _, m := range models {
		a := b.ByModel[m.Model]
		a.UsageAggregate.add(Run{Payload: r.Payload, CostUSD: m.CostUSD, InputTokens: m.InputTokens, OutputTokens: m.OutputTokens, CacheReadInputTokens: m.CacheReadInputTokens, CacheCreationInputTokens: m.CacheCreationInputTokens})
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
func (s *Store) Usage(ctx context.Context, f RunFilter, day int) (UsageSummary, error) {
	sum := UsageSummary{GeneratedAt: time.Now().UTC(), UsageBreakdown: newBreakdown(), Periods: []UsagePeriod{}}
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
			ms := models[r.RunDir]
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
