// Package retry provides a simple exponential-backoff retry helper for
// transient errors (network timeouts, 5xx responses, 429 rate limits).
package retry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	clog "github.com/zhoushoujianwork/clawflow/internal/log"
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

// DefaultConfig allows four attempts with 1/2/4 second backoff plus jitter.
// Callers bind requests to a 60-second context; Retry-After is never shortened.
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
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var err error
	for attempt := 0; attempt < cfg.MaxAttempts; attempt++ {
		// Honour cancellation before each attempt, including retries.
		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), err)
		}
		err = fn()
		if err == nil {
			return nil
		}
		var permanent *permanentError
		if errors.As(err, &permanent) {
			return permanent.error
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
		var response *HTTPError
		if errors.As(err, &response) && response.After > sleep {
			sleep = response.After
		}
		if deadline, ok := ctx.Deadline(); ok && time.Now().Add(sleep).After(deadline) {
			return err
		}
		lg, _ := clog.Open("run")
		lg.Warn("run/writeback_retry", "attempt", attempt+2, "delay", sleep)
		lg.Close()
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), err)
		case <-time.After(sleep):
		}
	}
	return err
}

// Permanent stops retrying a request that cannot recover without changing input.
type permanentError struct{ error }

func Permanent(err error) error { return &permanentError{err} }

// HTTPError carries retry timing without exposing response bodies or credentials.
type HTTPError struct {
	Status      int
	RateLimited bool
	After       time.Duration
}

func (e *HTTPError) Error() string { return fmt.Sprintf("transient HTTP %d", e.Status) }
func ResponseError(status int, header http.Header, body []byte) error {
	if status != 429 && !(status >= 500 && status != 501) && !(status == 403 && (header.Get("X-RateLimit-Remaining") == "0" || strings.Contains(strings.ToLower(string(body)), "rate limit exceeded") || header.Get("Retry-After") != "" || strings.Contains(strings.ToLower(string(body)), "secondary rate limit"))) {
		return nil
	}
	after := time.Duration(0)
	retryAfterSet := false
	if n, err := strconv.Atoi(header.Get("Retry-After")); err == nil && n >= 0 {
		retryAfterSet = true
		after = time.Duration(n) * time.Second
	} else if date, err := http.ParseTime(header.Get("Retry-After")); err == nil {
		retryAfterSet = true
		after = time.Until(date)
	}
	limited := status == 403 || status == 429
	if limited {
		if reset, err := strconv.ParseInt(header.Get("X-RateLimit-Reset"), 10, 64); err == nil && header.Get("X-RateLimit-Remaining") == "0" {
			if wait := time.Until(time.Unix(reset, 0)); wait > after {
				after = wait
			}
		}
		if after <= 0 && !retryAfterSet {
			after = time.Minute
		}
	}
	return &HTTPError{Status: status, After: after, RateLimited: limited}
}

// RequestError keeps transport failures retryable and stops on validation or
// decoding failures from reconciliation reads before a non-idempotent write.
func RequestError(err error) error {
	var response *HTTPError
	var network net.Error
	if errors.As(err, &response) || errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	return Permanent(err)
}
