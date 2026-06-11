// Package retry provides a simple exponential-backoff retry helper for
// transient errors (network timeouts, 5xx responses, 429 rate limits).
package retry

import (
	"context"
	"math/rand/v2"
	"time"
)

// Config controls retry behaviour.
type Config struct {
	// MaxAttempts is the total number of attempts (including the first one).
	// Must be >= 1. Zero is treated as 1 (no retries).
	MaxAttempts int

	// BaseDelay is the wait before the second attempt.
	BaseDelay time.Duration

	// MaxDelay caps the per-attempt sleep regardless of the exponential factor.
	MaxDelay time.Duration
}

// DefaultConfig is the project-wide default: 4 attempts with 1-2-4-8 s
// backoff + jitter, capped at 15 s per sleep. Total worst-case budget is
// roughly 30 s — well within the 5-minute writeBackTimeout.
var DefaultConfig = Config{
	MaxAttempts: 4,
	BaseDelay:   1 * time.Second,
	MaxDelay:    15 * time.Second,
}

// Do calls fn up to cfg.MaxAttempts times. It retries whenever fn returns a
// non-nil error. It respects ctx cancellation both before each attempt and
// during backoff sleeps so it does not outlive the caller's deadline. The
// last error (or nil) is returned.
func Do(ctx context.Context, cfg Config, fn func() error) error {
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}
	var err error
	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		// Honour cancellation before each attempt, including retries.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = fn()
		if err == nil {
			return nil
		}
		if attempt == cfg.MaxAttempts-1 {
			break
		}
		// Compute exponential delay with ±25 % jitter.
		delay := cfg.BaseDelay * (1 << attempt)
		if delay > cfg.MaxDelay {
			delay = cfg.MaxDelay
		}
		sleep := delay
		if jitterRange := int64(delay) / 4; jitterRange > 0 {
			sleep += time.Duration(rand.Int64N(jitterRange))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
	}
	return err
}
