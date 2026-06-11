package retry_test

import (
	"context"
	"errors"
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
