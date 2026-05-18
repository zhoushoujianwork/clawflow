package chat

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Event is one server-sent message frame. Types: "output" (stdout
// line), "stderr" (stderr line), "end" (clean exit, channel closes
// after), "error" (failure, channel closes after).
type Event struct {
	Type string    `json:"type"`
	Text string    `json:"text"`
	Time time.Time `json:"time"`
}

// Session is one active claude subprocess attached to a cloned repo.
// The Handler owns the map of sessions and reaps them via GC.
type Session struct {
	ID        string
	UserID    string
	Repo      string
	WorkDir   string
	CreatedAt time.Time

	// procCancel cancels the subprocess context. Handler sets it on
	// spawn / re-spawn; Close() invokes it so the timeout context is
	// released even when we kill the process directly.
	procCancel context.CancelFunc

	mu          sync.Mutex
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	lastActive  time.Time
	closed      bool
	closeReason string

	// events is the fan-out to the SSE handler. Closed exactly once
	// (by the reaper or Close()), after the terminal end/error event.
	events chan Event
	// done is closed when the reaper has finished tearing down.
	done chan struct{}
}

func newSession(userID, repo, workDir string, now time.Time) *Session {
	return &Session{
		ID:         newSessionID(),
		UserID:     userID,
		Repo:       repo,
		WorkDir:    workDir,
		CreatedAt:  now,
		lastActive: now,
		// 256 slots absorbs realistic claude bursts without blocking
		// the stdout pump on a slow SSE consumer.
		events: make(chan Event, 256),
		done:   make(chan struct{}),
	}
}

func newSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("s%d", time.Now().UnixNano())
	}
	return "s" + hex.EncodeToString(b[:])
}

// start spawns the claude subprocess against s.WorkDir. Must be called
// exactly once per turn. The reaper goroutine emits "end"/"error" and
// closes events when the process exits.
//
// We re-spawn per turn rather than keeping a persistent claude -p
// process: claude -p is one-shot, and the alternative (--input-format
// stream-json with framed JSON messages) is more surface area than the
// cloud-chat MVP needs.
func (s *Session) start(ctx context.Context, claudeBin, apiKey, firstMsg string) error {
	cmd := exec.CommandContext(ctx, claudeBin,
		"-p",
		"--dangerously-skip-permissions",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	)
	cmd.Dir = s.WorkDir
	cmd.Env = buildSubprocessEnv(os.Environ(), apiKey)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return fmt.Errorf("claude start: %w", err)
	}

	s.mu.Lock()
	s.cmd = cmd
	s.stdin = stdin
	s.lastActive = time.Now()
	s.mu.Unlock()

	var pumpsDone sync.WaitGroup
	pumpsDone.Add(2)
	go s.pumpReader(stdout, "output", &pumpsDone)
	go s.pumpReader(stderr, "stderr", &pumpsDone)

	// Reaper. The terminal emit must happen BEFORE s.closed flips —
	// emitLocked drops events on a closed session.
	go func() {
		pumpsDone.Wait()
		waitErr := cmd.Wait()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed {
			close(s.done)
			return
		}
		if waitErr != nil && !isExitedCleanly(waitErr) {
			s.emitLocked(Event{Type: "error", Text: waitErr.Error(), Time: time.Now()})
			s.closeReason = waitErr.Error()
		} else {
			s.emitLocked(Event{Type: "end", Text: "", Time: time.Now()})
		}
		s.closed = true
		close(s.events)
		close(s.done)
	}()

	if firstMsg != "" {
		if _, err := io.WriteString(stdin, firstMsg); err != nil {
			s.emit(Event{Type: "error", Text: "write first message: " + err.Error(), Time: time.Now()})
		}
	}
	_ = stdin.Close()
	return nil
}

func (s *Session) pumpReader(r io.ReadCloser, typ string, wg *sync.WaitGroup) {
	defer wg.Done()
	defer r.Close()

	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			line = strings.TrimRight(line, "\r\n")
			s.emit(Event{Type: typ, Text: line, Time: time.Now()})
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) emit(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitLocked(e)
}

func (s *Session) emitLocked(e Event) {
	// Recover from "send on closed channel" — race between pump and
	// Close().
	defer func() { _ = recover() }()
	if s.closed {
		return
	}
	s.lastActive = e.Time
	select {
	case s.events <- e:
	default:
		// Backpressure: drop rather than block the subprocess pump.
	}
}

// send queues a follow-up message. Reuses the workdir but spawns a
// fresh subprocess (claude -p is one-shot). Returns an error if the
// previous turn is still running; the caller serialises turns.
//
// NOTE: each follow-up resets the events channel, so any SSE
// consumer reading the previous channel will see it close. The browser
// must reconnect to /stream after every "end" event — EventSource's
// built-in auto-reconnect handles this naturally.
func (s *Session) send(ctx context.Context, claudeBin, apiKey, msg string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("session closed")
	}
	if s.cmd != nil && s.cmd.ProcessState == nil {
		s.mu.Unlock()
		return errors.New("previous turn still running")
	}
	s.cmd = nil
	s.stdin = nil
	s.events = make(chan Event, 256)
	s.done = make(chan struct{})
	s.closed = false
	s.mu.Unlock()

	return s.start(ctx, claudeBin, apiKey, msg)
}

// Close kills the subprocess (if running) and tears down the session.
// Safe to call multiple times.
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.done
		return
	}
	cmd := s.cmd
	stdin := s.stdin
	cancel := s.procCancel
	s.closeReason = "closed by user"
	// Emit BEFORE flipping closed (emitLocked drops on closed).
	s.emitLocked(Event{Type: "end", Text: "closed", Time: time.Now()})
	s.closed = true
	close(s.events)
	s.mu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}
	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	if cmd == nil {
		close(s.done)
		return
	}
	<-s.done
}

// Events returns the receive end of the SSE event channel.
func (s *Session) Events() <-chan Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events
}

// Idle reports how long the session has been without activity.
func (s *Session) Idle(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return now.Sub(s.lastActive)
}

func (s *Session) touch(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastActive = now
}

// buildSubprocessEnv returns a copy of parent with all ANTHROPIC_*
// vars stripped (avoid stale base-url/model leaking from the cloud
// server) and ANTHROPIC_API_KEY=<key> appended when key != "".
func buildSubprocessEnv(parent []string, apiKey string) []string {
	out := make([]string, 0, len(parent)+1)
	for _, kv := range parent {
		if strings.HasPrefix(kv, "ANTHROPIC_") {
			continue
		}
		out = append(out, kv)
	}
	if apiKey != "" {
		out = append(out, "ANTHROPIC_API_KEY="+apiKey)
	}
	return out
}

// isExitedCleanly distinguishes "context cancelled / killed by us"
// from a genuine claude failure. We sent the kill from Close(), so
// SIGKILL isn't user-facing.
func isExitedCleanly(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	if strings.Contains(err.Error(), "signal: killed") {
		return true
	}
	return false
}
