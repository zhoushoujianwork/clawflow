package store

import (
	"fmt"
	"testing"
	"time"
)

func TestModelDurationAttribution(t *testing.T) {
	low, high := int64(2), int64(9)
	for _, tc := range []struct {
		name   string
		models []RunModelUsage
		want   string
	}{
		{"fallback", nil, "fallback"},
		{"sole", []RunModelUsage{{Model: "one"}}, "one"},
		{"most output", []RunModelUsage{{Model: "a", OutputTokens: &low}, {Model: "b", OutputTokens: &high}}, "b"},
		{"missing tokens", []RunModelUsage{{Model: "a"}, {Model: "b", OutputTokens: &low}}, "b"},
		{"tie", []RunModelUsage{{Model: "b", OutputTokens: &high}, {Model: "a", OutputTokens: &high}}, "a"},
		{"all missing", []RunModelUsage{{Model: "b"}, {Model: "a"}}, "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBreakdown()
			b.add(Run{Model: "fallback", Repo: "o/r", DurationMs: 1234}, tc.models)
			var total int64
			for model, a := range b.ByModel {
				total += a.DurationMs
				if model == tc.want && a.DurationMs != 1234 {
					t.Fatalf("duration for %s: %d", model, a.DurationMs)
				}
			}
			if total != b.Totals.DurationMs || total != b.ByRepo["o/r"].DurationMs {
				t.Fatalf("duration counted more than once: %d", total)
			}
		})
	}
}

// These are persisted (exclusive-input) fixtures, not raw inclusive Offload counts.
// Conversion from the latter is tested separately in snapshot's #474 fixtures.
func TestUsageCoverageAndProvenance(t *testing.T) {
	b := newBreakdown()
	fixtures := []string{
		`{"repo":"o/r","project":"one","role":"implement","operator":"implement","status":"success","started_at":"2026-10-16T00:00:00Z","model":"claude","usage":{"total_cost_usd":2,"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":50,"cache_creation_input_tokens":20}}`,
		`{"repo":"o/r","project":"one","development_session":{"role":"acceptance","profile":"codex"},"operator":"acceptance-review","status":"success","started_at":"2026-10-16T01:00:00Z","model":"gpt","session_id":"resumed","usage":{"cost_basis":"api","cost_is_estimate":true,"total_cost_usd":1,"input_tokens":20,"output_tokens":5,"cache_read_input_tokens":70,"cache_creation_input_tokens":10},"cumulative_usage":{"input_tokens":999999}}`,
		`{"repo":"o/r","project":"one","operator":"repair-ci","status":"failed","started_at":"2026-10-16T02:00:00Z","model":"gpt","session_id":"resumed","usage":{"cost_basis":"subscription","cost_unavailable":true,"total_cost_usd":0,"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":80,"cache_creation_input_tokens":10}}`,
		`{"repo":"o/r","operator":"implement","status":"cancelled","started_at":"2026-10-16T03:00:00Z"}`,
		`{"repo":"o/r","operator":"implement","status":"failed","started_at":"2026-10-16T04:00:00Z","usage":{"input_tokens":7,"cost_unavailable":true}}`,
		`{"repo":"o/r","operator":"implement","status":"success","started_at":"2026-10-16T05:00:00Z","usage":{"cost_basis":"api","total_cost_usd":0,"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`,
	}
	for i, raw := range fixtures {
		r, models, err := DecodeRun(fmt.Sprintf("/fixture/%d", i), "run", []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		b.add(r, models)
	}
	a := b.Totals
	if a.Runs != 6 || a.TotalCostUSD == nil || *a.TotalCostUSD != 3 || a.TotalTokens == nil || *a.TotalTokens != 387 {
		t.Fatalf("totals %+v", a)
	}
	if a.Coverage != (UsageCoverage{Cost: 3, Input: 5, Output: 4, CacheRead: 4, CacheWrite: 4, Tokens: 4}) {
		t.Fatalf("coverage %+v", a.Coverage)
	}
	if a.EstimatedCostRuns != 1 || a.ReportedCostRuns != 1 || a.CostProvenanceUnknownRuns != 1 || a.SubscriptionRuns != 1 {
		t.Fatalf("provenance %+v", a)
	}
	if b.ByProject["one"].Runs != 3 || b.ByProject["unknown"].Runs != 3 || b.ByRole["acceptance"].Runs != 1 || b.ByProfile["codex"].Runs != 1 {
		t.Fatalf("attribution %+v", b)
	}
	if b.ByModel["unknown"].TotalTokens == nil || *b.ByModel["unknown"].TotalTokens != 0 {
		t.Fatal("measured zero must remain known")
	}
	unknown := newBreakdown()
	unknown.add(Run{}, nil)
	if unknown.Totals.TotalCostUSD != nil || unknown.Totals.TotalTokens != nil || unknown.Totals.Coverage.Cost != 0 {
		t.Fatalf("unknown became zero: %+v", unknown.Totals)
	}
}

func TestUsageBillingPeriodUTCBoundary(t *testing.T) {
	for _, tc := range []struct{ at, start, end string }{
		{"2026-10-14T23:59:59Z", "2026-09-15", "2026-10-15"},
		{"2026-10-15T00:00:00Z", "2026-10-15", "2026-11-15"},
		{"2026-10-15T01:00:00+02:00", "2026-09-15", "2026-10-15"},
	} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		start, end := BillingPeriod(at, 15)
		if start.Format("2006-01-02") != tc.start || end.Format("2006-01-02") != tc.end {
			t.Fatalf("%s: %s %s", tc.at, start, end)
		}
	}
}
