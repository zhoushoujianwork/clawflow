package retry_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/retry"
)

func TestDo_SuccessFirstAttempt(t *testing.T) {
	calls := 0
	err := retry.Do(context.Background(), retry.Config{MaxAttempts: 3, BaseDelay: time.Millisecond}, func() error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}
}

func TestDo_RetriesAndSucceeds(t *testing.T) {
	calls := 0
	sentinel := errors.New("transient")
	err := retry.Do(context.Background(), retry.Config{MaxAttempts: 3, BaseDelay: time.Millisecond}, func() error {
		calls++
		if calls < 3 {
			return sentinel
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestDo_ExhaustsAttempts(t *testing.T) {
	calls := 0
	sentinel := errors.New("persistent")
	err := retry.Do(context.Background(), retry.Config{MaxAttempts: 3, BaseDelay: time.Millisecond}, func() error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 calls, got %d", calls)
	}
}

func TestDo_ContextCancelDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	// Cancel after first failure so the backoff sleep is interrupted.
	err := retry.Do(ctx, retry.Config{MaxAttempts: 4, BaseDelay: 10 * time.Second}, func() error {
		calls++
		cancel()
		return errors.New("fail")
	})
	if err == nil {
		t.Fatal("expected error after ctx cancel")
	}
	// Should have called fn exactly once (cancel fires during first backoff).
	if calls != 1 {
		t.Fatalf("expected 1 call before ctx cancel, got %d", calls)
	}
}

func TestDo_ZeroMaxAttempts(t *testing.T) {
	calls := 0
	retry.Do(context.Background(), retry.Config{MaxAttempts: 0, BaseDelay: time.Millisecond}, func() error { //nolint:errcheck
		calls++
		return nil
	})
	if calls != 1 {
		t.Fatalf("zero MaxAttempts should act as 1, got %d calls", calls)
	}
}

func TestPermanentAndRetryAfterBudget(t *testing.T) {
	calls := 0
	err := retry.Do(context.Background(), retry.Config{MaxAttempts: 4}, func() error { calls++; return retry.Permanent(errors.New("validation")) })
	if err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	calls = 0
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = retry.Do(ctx, retry.Config{MaxAttempts: 4}, func() error { calls++; return &retry.HTTPError{Status: 429, After: time.Minute} })
	if err == nil || calls != 1 {
		t.Fatalf("Retry-After must not be shortened: calls=%d err=%v", calls, err)
	}
}

func TestResponseRetryAfter(t *testing.T) {
	for _, value := range []string{"7", time.Now().Add(7 * time.Second).UTC().Format(http.TimeFormat)} {
		err := retry.ResponseError(429, http.Header{"Retry-After": []string{value}}, nil)
		var response *retry.HTTPError
		if !errors.As(err, &response) || response.After < 5*time.Second || response.After > 8*time.Second {
			t.Fatalf("Retry-After %q: %+v", value, response)
		}
	}
	if retry.ResponseError(422, nil, nil) != nil || retry.ResponseError(403, nil, []byte("permission denied")) != nil {
		t.Fatal("validation or permission errors must not be transient")
	}
}

func TestPrimaryRateLimitResetAndSecondaryFallback(t *testing.T) {
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "0")
	h.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(2*time.Minute).Unix(), 10))
	h.Set("Retry-After", "1")
	var e *retry.HTTPError
	if !errors.As(retry.ResponseError(403, h, []byte("rate limit exceeded")), &e) || !e.RateLimited || e.After < 119*time.Second {
		t.Fatalf("primary reset ignored: %+v", e)
	}
	if !errors.As(retry.ResponseError(403, http.Header{}, []byte("secondary rate limit")), &e) || !e.RateLimited || e.After != time.Minute {
		t.Fatalf("secondary backoff missing: %+v", e)
	}
}

func TestCancellationPreservesRateLimitIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := retry.Do(ctx, retry.Config{MaxAttempts: 2}, func() error {
		cancel()
		return &retry.HTTPError{Status: 403, RateLimited: true, After: time.Millisecond}
	})
	var limited *retry.HTTPError
	if !errors.Is(err, context.Canceled) || !errors.As(err, &limited) || !limited.RateLimited {
		t.Fatalf("lost rate limit on cancellation: %v", err)
	}
}

func TestIsTransient(t *testing.T) {
	timeout := &url.Error{Op: "Get", URL: "https://api.github.com/repos/o/r/pulls", Err: context.DeadlineExceeded}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"nil":                 {nil, false},
		"deadline exceeded":   {context.DeadlineExceeded, true},
		"wrapped url timeout": {fmt.Errorf("list: %w", timeout), true},
		"joined with timeout": {errors.Join(errors.New("context"), timeout), true},
		"HTTP 502":            {&retry.HTTPError{Status: 502}, true},
		"EOF":                 {io.ErrUnexpectedEOF, true},
		"validation":          {errors.New("HTTP 422: Validation Failed"), false},
		"permanent":           {retry.Permanent(errors.New("bad input")), false},
		"canceled":            {context.Canceled, false},
	} {
		if got := retry.IsTransient(tc.err); got != tc.want {
			t.Errorf("%s: IsTransient = %v, want %v", name, got, tc.want)
		}
	}
	// RequestError must keep its previous behaviour: transient errors pass
	// through unchanged, everything else stops retrying.
	if err := retry.RequestError(timeout); err != timeout {
		t.Fatalf("RequestError changed a transient error: %v", err)
	}
	if err := retry.RequestError(errors.New("decode")); err == nil {
		t.Fatal("RequestError lost a permanent error")
	}
}
