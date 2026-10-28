package store

import "testing"

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
