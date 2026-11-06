package commands

import (
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

func TestUsageWithEventsFallback(t *testing.T) {
	cost := 0.4
	reported := &snapshot.Usage{TotalCostUSD: cost}
	estimate := &snapshot.Usage{TokensUnavailable: true}
	if got := usageWithEventsFallback(reported, estimate); got != reported {
		t.Fatalf("reported usage overwritten: %+v", got)
	}
	if got := usageWithEventsFallback(nil, estimate); got != estimate {
		t.Fatalf("fallback not used: %+v", got)
	}
	if got := usageWithEventsFallback(reported, nil); got != reported {
		t.Fatalf("reported usage blanked: %+v", got)
	}
	if got := usageWithEventsFallback(nil, nil); got != nil {
		t.Fatalf("unexpected usage: %+v", got)
	}
}
