package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

// pollWaitSeconds is the wait clamp the worker requests on each
// long-poll. Cloud caps this at 30s; we send the same value so a single
// successful poll covers a 30s window.
const pollWaitSeconds = 30

// pollHTTPTimeout is the HTTP read timeout for the long-poll request.
// It must be greater than pollWaitSeconds so the connection survives a
// quiet cloud (an empty assignment) without being torn down by the
// transport.
const pollHTTPTimeout = 45 * time.Second

// eventHTTPTimeout is the per-batch HTTP timeout when POSTing
// ChatEvents back to the cloud.
const eventHTTPTimeout = 20 * time.Second

// drainTimeout is the maximum time Loop.Run waits for in-flight
// sessions to wind down after ctx is cancelled.
const drainTimeout = 30 * time.Second

// backoffMin / backoffMax bracket the exponential backoff applied
// after a poll-level transport error.
const (
	backoffMin = 2 * time.Second
	backoffMax = 60 * time.Second
)

// Loop runs the long-poll → spawn → stream cycle. Construct via
// NewLoop; call Run with a cancellable context.
type Loop struct {
	cfg Config

	// pollClient is dedicated to /chat/poll requests; its read timeout
	// is sized for long-polling and unsuitable for sub-second event
	// posts. eventClient handles the latter.
	pollClient  *http.Client
	eventClient *http.Client

	once sync.Once
}

// Run blocks until ctx is cancelled. It long-polls
// /api/worker/chat/poll for assignments and dispatches each into a
// goroutine running runSession. Concurrent assignments are accepted —
// the poll never waits for a session to finish.
//
// Transient transport errors are retried with exponential backoff;
// ctx cancellation is propagated cleanly. After ctx is cancelled, Run
// blocks up to drainTimeout for outstanding sessions before returning.
func (l *Loop) Run(ctx context.Context, machineID, workerID string) error {
	if l.cfg.Client == nil {
		return errors.New("chat.Loop: Client is nil")
	}
	if machineID == "" || workerID == "" {
		return errors.New("chat.Loop: machineID and workerID are required")
	}
	l.initClients()

	var wg sync.WaitGroup
	backoff := backoffMin

	for {
		if ctx.Err() != nil {
			break
		}
		assignment, err := l.poll(ctx, machineID, workerID)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			fmt.Fprintf(stderr(), "clawflow chat poll: %v (retry in %s)\n", err, backoff)
			if !sleep(ctx, backoff) {
				break
			}
			backoff *= 2
			if backoff > backoffMax {
				backoff = backoffMax
			}
			continue
		}
		backoff = backoffMin
		if assignment == nil {
			// 204 / empty — immediately re-poll.
			continue
		}
		wg.Add(1)
		go func(a *cloud.ChatAssignment) {
			defer wg.Done()
			if err := runSession(ctx, l, a); err != nil {
				fmt.Fprintf(stderr(), "clawflow chat session %s: %v\n", a.SessionID, err)
			}
		}(assignment)
	}

	// Drain in-flight sessions with a bounded wait.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(drainTimeout):
		fmt.Fprintf(stderr(), "clawflow chat: drain timeout, leaving sessions in-flight\n")
	}
	return nil
}

func (l *Loop) initClients() {
	l.once.Do(func() {
		l.pollClient = &http.Client{Timeout: pollHTTPTimeout}
		l.eventClient = &http.Client{Timeout: eventHTTPTimeout}
	})
}

// poll issues one POST /api/worker/chat/poll and returns the
// assignment (nil when cloud responded 204 / empty Assignment).
func (l *Loop) poll(ctx context.Context, machineID, workerID string) (*cloud.ChatAssignment, error) {
	body, err := json.Marshal(cloud.ChatPollRequest{
		MachineID:   machineID,
		WorkerID:    workerID,
		WaitSeconds: pollWaitSeconds,
	})
	if err != nil {
		return nil, err
	}
	endpoint, err := l.endpoint("/api/worker/chat/poll")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := l.cfg.Client.Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := l.pollClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("chat poll HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(buf))
	}
	var pr cloud.ChatPollResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		// Cloud responded with no body — treat as "no assignment".
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, fmt.Errorf("decode poll response: %w", err)
	}
	return pr.Assignment, nil
}

// postEvents pushes a batched WorkerEventsRequest. Best-effort: a
// transport failure is returned to the caller, which may decide to
// retry or abandon the session.
func (l *Loop) postEvents(ctx context.Context, sessionID string, events []cloud.ChatEvent) error {
	if len(events) == 0 {
		return nil
	}
	body, err := json.Marshal(cloud.WorkerEventsRequest{Events: events})
	if err != nil {
		return err
	}
	endpoint, err := l.endpoint("/api/worker/chat/sessions/" + url.PathEscape(sessionID) + "/events")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := l.cfg.Client.Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := l.eventClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("chat events HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(buf))
	}
	return nil
}

// postChatUsage uploads the terminal token / cost breakdown for one
// chat session to cloud's /api/worker/chat/sessions/{id}/usage
// endpoint. Best-effort: a transport failure is returned but does not
// fail the session (the browser already saw the answer).
func (l *Loop) postChatUsage(ctx context.Context, sessionID string, usage *cloud.Usage) error {
	body, err := json.Marshal(cloud.ChatUsageRequest{Usage: usage})
	if err != nil {
		return err
	}
	endpoint, err := l.endpoint("/api/worker/chat/sessions/" + url.PathEscape(sessionID) + "/usage")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := l.cfg.Client.Token(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := l.eventClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("chat usage HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(buf))
	}
	return nil
}

func (l *Loop) endpoint(path string) (string, error) {
	base := l.cfg.Client.BaseURL()
	if base == "" {
		return "", errors.New("chat.Loop: cloud base URL is empty")
	}
	return base + path, nil
}

// sleep waits for d but returns false if ctx is cancelled first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
