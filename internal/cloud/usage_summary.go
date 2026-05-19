package cloud

import (
	"sort"
	"time"
)

// UsageSummary is the response body of GET /api/cloud/usage/summary.
// Its shape is intentionally identical to snapshot.UsageSummary so the
// existing local-mode Usage page (web/src/routes/_app.usage.tsx) can
// consume it unchanged.
type UsageSummary struct {
	GeneratedAt time.Time                 `json:"generated_at"`
	Totals      UsageAggregate            `json:"totals"`
	ByOperator  map[string]UsageAggregate `json:"by_operator"`
	ByRepo      map[string]UsageAggregate `json:"by_repo"`
	ByModel     map[string]ModelAggregate `json:"by_model"`
	Periods     []PeriodSummary           `json:"periods,omitempty"`
}

// PeriodSummary mirrors the local-mode periods entry. The cloud
// endpoint currently emits a single "current" period covering the
// rolling 30 days ending now; future iterations can split per
// calendar month if users start having long-running histories.
type PeriodSummary struct {
	PeriodStart string                    `json:"period_start"`
	PeriodEnd   string                    `json:"period_end"`
	Totals      UsageAggregate            `json:"totals"`
	ByOperator  map[string]UsageAggregate `json:"by_operator"`
	ByRepo      map[string]UsageAggregate `json:"by_repo"`
	ByModel     map[string]ModelAggregate `json:"by_model"`
	DailyTrend  []DailyPoint              `json:"daily_trend"`
}

// DailyPoint is one day's aggregated usage; the bar-chart at the top
// of the Usage page renders one of these per day in the active period.
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

// UsageAggregate is the summable row used by the dashboard's
// per-operator, per-repo, and grand-total slices. Mirrors
// snapshot.UsageAggregate.
type UsageAggregate struct {
	Runs                     int     `json:"runs"`
	TotalCostUSD             float64 `json:"total_cost_usd"`
	InputTokens              int64   `json:"input_tokens"`
	OutputTokens             int64   `json:"output_tokens"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens"`
	DurationMs               int64   `json:"duration_ms"`
}

// ModelAggregate is the per-model slice; mirrors snapshot.ModelAggregate.
// No DurationMs because a single run's duration spans every model it
// called.
type ModelAggregate struct {
	CostUSD                  float64 `json:"cost_usd"`
	InputTokens              int64   `json:"input_tokens"`
	OutputTokens             int64   `json:"output_tokens"`
	CacheReadInputTokens     int64   `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64   `json:"cache_creation_input_tokens"`
}

// BuildUsageSummary aggregates a flat list of UsageRecord rows (both run
// and chat surfaces) into the UsageSummary shape the Usage page expects.
// `now` is the upper bound of the rolling 30-day "current" period;
// callers pass time.Now().UTC() in production and a fixed timestamp in
// tests.
func BuildUsageSummary(records []*UsageRecord, now time.Time) UsageSummary {
	sum := UsageSummary{
		GeneratedAt: now,
		ByOperator:  map[string]UsageAggregate{},
		ByRepo:      map[string]UsageAggregate{},
		ByModel:     map[string]ModelAggregate{},
	}

	periodEnd := now.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour).Add(-time.Nanosecond)
	periodStart := periodEnd.AddDate(0, 0, -29).Truncate(24 * time.Hour)
	period := &PeriodSummary{
		PeriodStart: periodStart.Format("2006-01-02"),
		PeriodEnd:   periodEnd.Format("2006-01-02"),
		ByOperator:  map[string]UsageAggregate{},
		ByRepo:      map[string]UsageAggregate{},
		ByModel:     map[string]ModelAggregate{},
	}

	// Pre-fill daily trend buckets so the chart shows zero-cost days
	// rather than gaps. 30 days, oldest first.
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

	for _, rec := range records {
		if rec == nil {
			continue
		}
		// "Operator" for chat sessions is the synthetic "chat" bucket
		// so the page's by-operator panel groups them separately from
		// agent runs. Repo carries through from the record either way.
		operator := rec.Operator
		if operator == "" {
			operator = "chat"
		}

		// All-time totals.
		addToAggregate(&sum.Totals, rec)
		op := sum.ByOperator[operator]
		addToAggregate(&op, rec)
		sum.ByOperator[operator] = op
		if rec.Repo != "" {
			r := sum.ByRepo[rec.Repo]
			addToAggregate(&r, rec)
			sum.ByRepo[rec.Repo] = r
		}
		for name, m := range rec.ModelUsage {
			cur := sum.ByModel[name]
			cur.CostUSD += m.CostUSD
			cur.InputTokens += m.InputTokens
			cur.OutputTokens += m.OutputTokens
			cur.CacheReadInputTokens += m.CacheReadInputTokens
			cur.CacheCreationInputTokens += m.CacheCreationInputTokens
			sum.ByModel[name] = cur
		}

		// Skip rows outside the current 30-day window for the
		// period-scoped aggregation, but they DO contribute to the
		// all-time totals computed above.
		if rec.EndedAt.Before(periodStart) || rec.EndedAt.After(periodEnd) {
			continue
		}
		addToAggregate(&period.Totals, rec)
		pop := period.ByOperator[operator]
		addToAggregate(&pop, rec)
		period.ByOperator[operator] = pop
		if rec.Repo != "" {
			pr := period.ByRepo[rec.Repo]
			addToAggregate(&pr, rec)
			period.ByRepo[rec.Repo] = pr
		}
		for name, m := range rec.ModelUsage {
			cur := period.ByModel[name]
			cur.CostUSD += m.CostUSD
			cur.InputTokens += m.InputTokens
			cur.OutputTokens += m.OutputTokens
			cur.CacheReadInputTokens += m.CacheReadInputTokens
			cur.CacheCreationInputTokens += m.CacheCreationInputTokens
			period.ByModel[name] = cur
		}

		day := rec.EndedAt.UTC().Format("2006-01-02")
		if dp, ok := dayMap[day]; ok {
			dp.Runs++
			dp.TotalCostUSD += rec.TotalCostUSD
			dp.InputTokens += rec.InputTokens
			dp.OutputTokens += rec.OutputTokens
			dop := dp.ByOperator[operator]
			addToAggregate(&dop, rec)
			dp.ByOperator[operator] = dop
			if rec.Repo != "" {
				dr := dp.ByRepo[rec.Repo]
				addToAggregate(&dr, rec)
				dp.ByRepo[rec.Repo] = dr
			}
			for name, m := range rec.ModelUsage {
				cur := dp.ByModel[name]
				cur.CostUSD += m.CostUSD
				cur.InputTokens += m.InputTokens
				cur.OutputTokens += m.OutputTokens
				cur.CacheReadInputTokens += m.CacheReadInputTokens
				cur.CacheCreationInputTokens += m.CacheCreationInputTokens
				dp.ByModel[name] = cur
			}
		}
	}

	// Materialize daily trend in date order.
	days := make([]string, 0, len(dayMap))
	for d := range dayMap {
		days = append(days, d)
	}
	sort.Strings(days)
	trend := make([]DailyPoint, 0, len(days))
	for _, d := range days {
		trend = append(trend, *dayMap[d])
	}
	period.DailyTrend = trend

	if period.Totals.Runs > 0 || len(trend) > 0 {
		sum.Periods = []PeriodSummary{*period}
	}
	return sum
}

// addToAggregate folds one UsageRecord into a running UsageAggregate.
func addToAggregate(agg *UsageAggregate, rec *UsageRecord) {
	agg.Runs++
	agg.TotalCostUSD += rec.TotalCostUSD
	agg.InputTokens += rec.InputTokens
	agg.OutputTokens += rec.OutputTokens
	agg.CacheReadInputTokens += rec.CacheReadInputTokens
	agg.CacheCreationInputTokens += rec.CacheCreationInputTokens
	agg.DurationMs += rec.DurationMs
}
