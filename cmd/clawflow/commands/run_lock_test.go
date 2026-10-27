package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func stubRunLock(t *testing.T, failures int) *int {
	t.Helper()
	calls := 0
	prevAcquire, prevWait, prevPoll := acquireRunLock, targetedLockWait, runLockPoll
	acquireRunLock = func(string) error {
		calls++
		if calls <= failures {
			return errors.New("another clawflow run is active")
		}
		return nil
	}
	targetedLockWait, runLockPoll = 200*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { acquireRunLock, targetedLockWait, runLockPoll = prevAcquire, prevWait, prevPoll })
	return &calls
}

func TestUnscopedRunSkipsOnLockContention(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := stubRunLock(t, 1000)
	skip, err := acquireRunLockForPass(context.Background(), nil, false)
	if !skip || err != nil || *calls != 1 {
		t.Fatalf("skip=%v err=%v calls=%d", skip, err, *calls)
	}
}

func TestTargetedRunWaitsForLock(t *testing.T) {
	calls := stubRunLock(t, 3)
	skip, err := acquireRunLockForPass(context.Background(), nil, true)
	if skip || err != nil || *calls != 4 {
		t.Fatalf("skip=%v err=%v calls=%d", skip, err, *calls)
	}
}

func TestTargetedRunFailsAfterLockWaitTimeout(t *testing.T) {
	stubRunLock(t, 1000)
	skip, err := acquireRunLockForPass(context.Background(), nil, true)
	if skip || err == nil || !strings.Contains(err.Error(), "still active after waiting") {
		t.Fatalf("skip=%v err=%v", skip, err)
	}
}

func TestTargetedRunLockWaitHonorsCancellation(t *testing.T) {
	stubRunLock(t, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := acquireRunLockForPass(ctx, nil, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
