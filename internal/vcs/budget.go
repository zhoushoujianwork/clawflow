package vcs

import (
	"errors"
	"time"
)

// ErrPollingDeferred preserves snapshots while delivery uses reserved quota.
var ErrPollingDeferred = errors.New("VCS polling deferred until rate-limit reset")

// APIBudget is the most recently observed provider quota, shared by credentials.
type APIBudget struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Reset     time.Time `json:"reset"`
	Backoff   time.Time `json:"backoff"`
}

// APIUsage contains public counters from the most recent scheduler pass.
type APIUsage struct {
	CapturedAt time.Time            `json:"captured_at"`
	Calls      map[string]int       `json:"calls_by_stage"`
	Budgets    map[string]APIBudget `json:"budgets"`
}
