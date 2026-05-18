package chat

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

// batcher coalesces ChatEvents and ships them to cloud either every
// flushInterval or every flushBytes (whichever comes first). Use add
// to enqueue an event; the batcher takes care of POSTing.
//
// Concurrent calls to add are safe. flushNow blocks until the
// in-memory queue is drained (best-effort: a transport error logs and
// drops the batch).
type batcher struct {
	loop      *Loop
	sessionID string

	mu      sync.Mutex
	pending []cloud.ChatEvent
	size    int

	flushCh chan struct{}
	doneCh  chan struct{}
	stopCh  chan struct{}
}

func newBatcher(l *Loop, sessionID string) *batcher {
	b := &batcher{
		loop:      l,
		sessionID: sessionID,
		flushCh:   make(chan struct{}, 1),
		doneCh:    make(chan struct{}),
		stopCh:    make(chan struct{}),
	}
	go b.run()
	return b
}

func (b *batcher) add(ev cloud.ChatEvent) {
	b.mu.Lock()
	b.pending = append(b.pending, ev)
	b.size += len(ev.Text)
	overflow := b.size >= flushBytes
	b.mu.Unlock()
	if overflow {
		select {
		case b.flushCh <- struct{}{}:
		default:
		}
	}
}

// run is the ticker / signal loop. It is the only goroutine that
// reads pending and posts.
func (b *batcher) run() {
	defer close(b.doneCh)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-b.stopCh:
			b.drain()
			return
		case <-ticker.C:
			b.drain()
		case <-b.flushCh:
			b.drain()
		}
	}
}

// drain takes the current pending slice and POSTs it.
func (b *batcher) drain() {
	b.mu.Lock()
	if len(b.pending) == 0 {
		b.mu.Unlock()
		return
	}
	batch := b.pending
	b.pending = nil
	b.size = 0
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), eventHTTPTimeout)
	defer cancel()
	if err := b.loop.postEvents(ctx, b.sessionID, batch); err != nil {
		fmt.Fprintf(stderr(), "clawflow chat: post events: %v\n", err)
	}
}

// flushNow asks the batcher to flush and waits for it. Callers should
// invoke this once after the subprocess exits to make sure no events
// are stranded.
func (b *batcher) flushNow() {
	select {
	case b.flushCh <- struct{}{}:
	default:
	}
	// Brief wait for the run loop to pick up the signal. We don't
	// hard-sync because run is single-threaded; one tick is enough.
	// Worst case, stop() catches what's left.
	time.Sleep(10 * time.Millisecond)
	// Force a synchronous drain on the calling goroutine in case run
	// is already busy elsewhere.
	b.drain()
}

// stop signals the run goroutine to drain remaining events and exit.
// Safe to call multiple times.
func (b *batcher) stop() {
	select {
	case <-b.stopCh:
		// already stopped
	default:
		close(b.stopCh)
	}
	<-b.doneCh
}

// boundedBuffer is a tiny ring buffer keeping the last cap bytes of
// data written to it. Used for the stderr tail attached to
// ChatEventError.
type boundedBuffer struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cap <= 0 {
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.cap {
		b.buf = b.buf[len(b.buf)-b.cap:]
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
