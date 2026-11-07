package commands

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/retry"
)

// A succeeded task whose write-back keeps failing transiently is retried
// forever with this backoff instead of being failed. The first retry is due on
// the next reconcile tick; the delay doubles up to the cap so an hours-long
// outage costs a handful of requests per task, not one per tick.
const (
	transientRetryBase = 30 * time.Second
	transientRetryMax  = 10 * time.Minute
)

// resultBackoff remembers, per task and lease epoch, how many consecutive
// transient write-back failures occurred and when the next try is due. It is
// deliberately in memory: after a controller restart every pending result is
// retried at once, and the durable state (result, intents, lease) is unchanged.
// The zero value is ready to use.
type resultBackoff struct {
	mu      sync.Mutex
	entries map[string]backoffEntry
}

type backoffEntry struct {
	fence    int64
	failures int
	next     time.Time
}

// wait returns how long until the task may be retried; zero means now.
func (b *resultBackoff) wait(id string, fence int64, now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.entries[id]
	if !ok || e.fence != fence || !e.next.After(now) {
		return 0
	}
	return e.next.Sub(now)
}

// failed records one more transient failure and returns the delay before the
// next attempt.
func (b *resultBackoff) failed(id string, fence int64, now time.Time) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.entries == nil {
		b.entries = map[string]backoffEntry{}
	}
	e := b.entries[id]
	if e.fence != fence {
		e = backoffEntry{fence: fence}
	}
	e.failures++
	delay := transientRetryBase
	for i := 1; i < e.failures && delay < transientRetryMax; i++ {
		delay *= 2
	}
	if delay > transientRetryMax {
		delay = transientRetryMax
	}
	e.next = now.Add(delay)
	b.entries[id] = e
	return delay
}

// failures returns the consecutive transient failures recorded for the task.
func (b *resultBackoff) failures(id string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.entries[id].failures
}

func (b *resultBackoff) clear(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.entries, id)
}

// resultSucceeded reports whether a stored report describes a task that
// finished its work: it ends as done and its status is success or skipped,
// the same statuses recoverResults treats as not failed. Only such a task is
// protected from being failed by a transient write-back error. A failed or
// interrupted report keeps the three-attempt bound.
func resultSucceeded(raw json.RawMessage) bool {
	var r coordinatorReport
	if json.Unmarshal(raw, &r) != nil {
		return false
	}
	if r.TerminalState == "failed" {
		return false
	}
	return r.Status == "success" || r.Status == "skipped"
}

// deferrable reports whether a write-back error may be retried without
// charging the task: the task itself succeeded and the error is transient
// (timeout, network error, HTTP 5xx). Everything else is counted and bounded.
func deferrable(err error, raw json.RawMessage) bool {
	return retry.IsTransient(err) && resultSucceeded(raw)
}

func (s *coordinatorResults) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}
