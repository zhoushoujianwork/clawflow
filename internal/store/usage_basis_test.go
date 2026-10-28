package store

import "testing"

func TestSubscriptionAggregation(t *testing.T) {
	b := newBreakdown()
	r := Run{Model: "gpt-test", Profile: "codex", Payload: `{"usage":{"cost_basis":"subscription"}}`}
	b.add(r, nil)
	if b.Totals.TotalCostUSD != nil || b.Totals.SubscriptionRuns != 1 || b.ByModel["gpt-test"].SubscriptionRuns != 1 || b.ByExecutor["codex/"].SubscriptionRuns != 1 {
		t.Fatalf("%+v", b)
	}
	cost := 1.0
	b.add(Run{Model: "claude", CostUSD: &cost}, nil)
	if *b.Totals.TotalCostUSD != 1 || b.Totals.SubscriptionRuns != 1 {
		t.Fatalf("%+v", b.Totals)
	}
}
