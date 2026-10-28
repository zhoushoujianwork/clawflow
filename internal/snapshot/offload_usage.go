package snapshot

import (
	"encoding/json"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

// UsageFromResult translates only this run's usage; cumulative usage would
// double-count earlier attempts when a development session is resumed.
func UsageFromResult(r execution.Result) *Usage {
	// Keep Result.Usage as raw JSON for existing consumers, decoding the
	// inclusive Offload contract only at this conversion boundary.
	var tokens *execution.TokenUsage
	var decodeErr error
	if len(r.Usage) > 0 {
		decodeErr = json.Unmarshal(r.Usage, &tokens)
	}
	u := &Usage{
		CostBasis: r.CostBasis, CostIsEstimate: r.CostIsEstimate,
		CostUnavailable:   r.CostUSD == nil || r.CostUnavailable != "",
		TokensUnavailable: tokens == nil || r.UsageUnavailable != "",
		UsageUnavailable:  r.UsageUnavailable, CostUnavailableReason: r.CostUnavailable,
	}
	if decodeErr != nil {
		u.TokensUnavailable = true
		if u.UsageUnavailable == "" {
			u.UsageUnavailable = "invalid token counts reported by the Offload runtime"
		}
	}
	if r.CostUSD != nil {
		u.TotalCostUSD = *r.CostUSD
	}
	if tokens == nil && u.UsageUnavailable == "" {
		u.UsageUnavailable = "not reported by the Offload runtime"
	}
	if t := tokens; t != nil && !u.TokensUnavailable {
		if t.InputTokens == nil || t.CachedInputTokens == nil || t.CacheWriteInputTokens == nil || t.OutputTokens == nil {
			u.TokensUnavailable = true
			u.UsageUnavailable = "incomplete token counts reported by the Offload runtime"
			return u
		}
		// Offload includes cache reads/writes in input; Claude counts them separately.
		if *t.InputTokens < 0 || *t.CachedInputTokens < 0 || *t.CacheWriteInputTokens < 0 || *t.OutputTokens < 0 ||
			*t.CachedInputTokens > *t.InputTokens || *t.CacheWriteInputTokens > *t.InputTokens-*t.CachedInputTokens {
			u.TokensUnavailable = true
			u.UsageUnavailable = "invalid token counts reported by the Offload runtime"
		} else {
			u.InputTokens = *t.InputTokens - *t.CachedInputTokens - *t.CacheWriteInputTokens
			u.CacheReadInputTokens = *t.CachedInputTokens
			u.CacheCreationInputTokens = *t.CacheWriteInputTokens
			u.OutputTokens = *t.OutputTokens
		}
	}
	return u
}
