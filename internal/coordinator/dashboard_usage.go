package coordinator

import (
	"context"
	"sort"
	"time"
)

// DashboardUsage is a bounded, credential-free usage projection for the native app.
type DashboardUsage struct {
	GeneratedAt time.Time                      `json:"generated_at"`
	Total       DashboardUsageScope            `json:"total"`
	ByRepo      map[string]DashboardUsageScope `json:"by_repo"`
}

type DashboardUsageScope struct {
	Today         DashboardUsageAggregate `json:"today"`
	ThirtyDays    DashboardUsageAggregate `json:"thirty_days"`
	Daily         []DashboardUsageDay     `json:"daily"`
	MostUsedModel string                  `json:"most_used_model,omitempty"`
}

type DashboardUsageAggregate struct {
	CostUSD                  *float64 `json:"cost_usd,omitempty"`
	TotalTokens              *int64   `json:"total_tokens,omitempty"`
	InputTokens              *int64   `json:"input_tokens,omitempty"`
	OutputTokens             *int64   `json:"output_tokens,omitempty"`
	CacheReadInputTokens     *int64   `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens *int64   `json:"cache_creation_input_tokens,omitempty"`
	CostIsEstimate           bool     `json:"cost_is_estimate"`
}

type DashboardUsageDay struct {
	Date        string   `json:"date"`
	CostUSD     *float64 `json:"cost_usd,omitempty"`
	TotalTokens *int64   `json:"total_tokens,omitempty"`
}

type dashboardUsageRow struct {
	Repo                     string
	CostUSD                  *float64
	InputTokens              *int64
	OutputTokens             *int64
	CacheReadInputTokens     *int64
	CacheCreationInputTokens *int64
	TotalTokens              *int64
	Model                    string
	EstimatedCostRows        int
}

type dashboardUsageAccumulator struct {
	scope       DashboardUsageScope
	days        map[string]*DashboardUsageDay
	modelTokens map[string]int64
}

func newDashboardUsageAccumulator(start time.Time) *dashboardUsageAccumulator {
	a := &dashboardUsageAccumulator{days: map[string]*DashboardUsageDay{}, modelTokens: map[string]int64{}}
	for i := 0; i < 30; i++ {
		date := start.AddDate(0, 0, i).Format("2006-01-02")
		a.days[date] = &DashboardUsageDay{Date: date}
	}
	return a
}

// DashboardUsage reads only the last 30 local calendar days. Go computes each
// local bucket's UTC boundaries, while SQL only applies index-friendly ranges.
func (s *Store) DashboardUsage(ctx context.Context, now time.Time, loc *time.Location) (DashboardUsage, error) {
	if loc == nil {
		loc = time.Local
	}
	now = now.In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	start := today.AddDate(0, 0, -29)
	total := newDashboardUsageAccumulator(start)
	repos := map[string]*dashboardUsageAccumulator{}
	for i := 0; i < 30; i++ {
		dayStart := start.AddDate(0, 0, i)
		dayEnd := dayStart.AddDate(0, 0, 1)
		if dayEnd.After(now) {
			dayEnd = now.Add(time.Nanosecond)
		}
		rows := []dashboardUsageRow{}
		err := s.db.WithContext(ctx).Table("runs").Select(`repo, model,
 SUM(cost_usd) AS cost_usd,
 SUM(input_tokens) AS input_tokens,
 SUM(output_tokens) AS output_tokens,
 SUM(cache_read_input_tokens) AS cache_read_input_tokens,
 SUM(cache_creation_input_tokens) AS cache_creation_input_tokens,
 SUM(CASE WHEN input_tokens IS NOT NULL AND output_tokens IS NOT NULL
               AND cache_read_input_tokens IS NOT NULL AND cache_creation_input_tokens IS NOT NULL
          THEN input_tokens + output_tokens + cache_read_input_tokens + cache_creation_input_tokens END) AS total_tokens,
 SUM(CASE WHEN cost_usd IS NOT NULL
               AND (COALESCE(json_extract(NULLIF(payload, ''), '$.usage.cost_is_estimate'), 0) <> 0
                    OR COALESCE(json_extract(NULLIF(payload, ''), '$.usage.cost_basis'), '') = '')
          THEN 1 ELSE 0 END) AS estimated_cost_rows`).
			Where("started_at >= ? AND started_at < ?", dayStart.UTC(), dayEnd.UTC()).
			Group("repo, model").Find(&rows).Error
		if err != nil {
			return DashboardUsage{}, err
		}
		day := dayStart.Format("2006-01-02")
		for _, row := range rows {
			addDashboardUsage(total, row, day, i == 29)
			if row.Repo == "" {
				continue
			}
			a := repos[row.Repo]
			if a == nil {
				a = newDashboardUsageAccumulator(start)
				repos[row.Repo] = a
			}
			addDashboardUsage(a, row, day, i == 29)
		}
	}
	out := DashboardUsage{GeneratedAt: now.UTC(), Total: total.finish(), ByRepo: map[string]DashboardUsageScope{}}
	for repo, a := range repos {
		out.ByRepo[repo] = a.finish()
	}
	return out, nil
}

func addDashboardUsage(a *dashboardUsageAccumulator, row dashboardUsageRow, day string, today bool) {
	estimate := row.EstimatedCostRows > 0
	addDashboardAggregate(&a.scope.ThirtyDays, row, estimate)
	if today {
		addDashboardAggregate(&a.scope.Today, row, estimate)
	}
	d := a.days[day]
	if d == nil {
		return
	}
	if row.CostUSD != nil {
		addFloat64(&d.CostUSD, *row.CostUSD)
	}
	if row.TotalTokens != nil {
		addInt64(&d.TotalTokens, *row.TotalTokens)
		if row.Model != "" {
			a.modelTokens[row.Model] += *row.TotalTokens
		}
	}
}

func addDashboardAggregate(a *DashboardUsageAggregate, row dashboardUsageRow, estimate bool) {
	if row.CostUSD != nil {
		addFloat64(&a.CostUSD, *row.CostUSD)
		if estimate {
			a.CostIsEstimate = true
		}
	}
	for _, metric := range []struct {
		dst **int64
		src *int64
	}{{&a.InputTokens, row.InputTokens}, {&a.OutputTokens, row.OutputTokens}, {&a.CacheReadInputTokens, row.CacheReadInputTokens}, {&a.CacheCreationInputTokens, row.CacheCreationInputTokens}} {
		if metric.src != nil {
			addInt64(metric.dst, *metric.src)
		}
	}
	if row.TotalTokens != nil {
		addInt64(&a.TotalTokens, *row.TotalTokens)
	}
}

func addFloat64(dst **float64, value float64) {
	if *dst == nil {
		*dst = new(float64)
	}
	**dst += value
}

func addInt64(dst **int64, value int64) {
	if *dst == nil {
		*dst = new(int64)
	}
	**dst += value
}

func (a *dashboardUsageAccumulator) finish() DashboardUsageScope {
	keys := make([]string, 0, len(a.days))
	for key := range a.days {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		a.scope.Daily = append(a.scope.Daily, *a.days[key])
	}
	var bestTokens int64 = -1
	for model, tokens := range a.modelTokens {
		if tokens > bestTokens || (tokens == bestTokens && model < a.scope.MostUsedModel) {
			a.scope.MostUsedModel, bestTokens = model, tokens
		}
	}
	return a.scope
}
