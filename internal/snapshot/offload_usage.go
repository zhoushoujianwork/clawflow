package snapshot

import "github.com/zhoushoujianwork/clawflow/internal/execution"

// UsageFromResult translates only this run's usage; cumulative usage would
// double-count earlier attempts when a development session is resumed.
func UsageFromResult(r execution.Result) *Usage {
	u := &Usage{
		CostBasis: r.CostBasis, CostIsEstimate: r.CostIsEstimate,
		CostUnavailable:   r.CostUSD == nil || r.CostUnavailable != "",
		TokensUnavailable: r.Usage == nil || r.UsageUnavailable != "",
		UsageUnavailable:  r.UsageUnavailable, CostUnavailableReason: r.CostUnavailable,
	}
	if r.CostUSD != nil {
		u.TotalCostUSD = *r.CostUSD
	}
	if r.Usage == nil && u.UsageUnavailable == "" {
		u.UsageUnavailable = "not reported by the Offload runtime"
	}
	if t := r.Usage; t != nil && !u.TokensUnavailable {
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
