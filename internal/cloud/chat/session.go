package chat

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

// Session is one router-side chat conversation. It holds no subprocess
// — only the SSE event channel that the worker writes into and the
// browser reads from. The Handler owns the registry of Sessions and
// reaps idle ones.
type Session struct {
	ID        string
	UserID    string
	Repo      string
	MachineID string
	CreatedAt time.Time

	mu         sync.Mutex
	closed     bool
	lastActive time.Time

	// eventCh fans events from worker → SSE handler. Closed exactly
	// once (by closeLocked).
	eventCh chan cloud.ChatEvent
	// done is closed when this session is fully torn down. Callers
	// waiting on a teardown to settle block here.
	done chan struct{}
}

func newSession(userID, repo, machineID string, now time.Time) *Session {
	return &Session{
		ID:         newSessionID(),
		UserID:     userID,
		Repo:       repo,
		MachineID:  machineID,
		CreatedAt:  now,
		lastActive: now,
		// 256 slots absorbs realistic claude bursts without blocking
		// the worker's events POST.
		eventCh: make(chan cloud.ChatEvent, 256),
		done:    make(chan struct{}),
	}
}

func newSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("s%d", time.Now().UnixNano())
	}
	return "s" + hex.EncodeToString(b[:])
}

// Events returns the receive end of the SSE event channel.
func (s *Session) Events() <-chan cloud.ChatEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eventCh
}

// Idle reports how long the session has been without activity.
func (s *Session) Idle(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return now.Sub(s.lastActive)
}

// emit enqueues an event onto the SSE channel. Drops on backpressure
// rather than blocking the worker's HTTP request. Touches lastActive
// even when dropped — the worker IS active, just the consumer is slow.
func (s *Session) emit(e cloud.ChatEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.lastActive = e.Time
	defer func() { _ = recover() }() // race vs closeLocked
	select {
	case s.eventCh <- e:
	default:
		// Backpressure: drop rather than block.
	}
}

// closeLocked tears the session down. Caller must hold s.mu.
// Idempotent; safe to call multiple times.
func (s *Session) closeLocked() {
	if s.closed {
		return
	}
	s.closed = true
	defer func() { _ = recover() }()
	close(s.eventCh)
	close(s.done)
}

// Close tears the session down. Idempotent. Safe to call concurrently.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

// Closed reports whether the session has been torn down.
func (s *Session) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// terminate emits a final error frame to the consumer and closes the
// session. Used when an admin deletes the session out from under a
// live stream.
func (s *Session) terminate(reason string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.lastActive = now
	defer func() { _ = recover() }()
	select {
	case s.eventCh <- cloud.ChatEvent{Type: cloud.ChatEventError, Text: reason, Time: now}:
	default:
	}
	s.closeLocked()
}
