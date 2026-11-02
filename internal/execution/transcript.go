package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"sync"
	"time"
)

// Transcript content is a side channel. It is read only through Offload's
// supported `transcript read --json` interface, for the single invocation this
// process started (selected by its caller run ID). It never feeds the result,
// events or outcome protocol, and a failure here never fails the execution.

var (
	// ErrTranscriptRejected tells the pump that the receiver will never accept
	// this stream (stale lease, identity conflict, oversized page). It stops.
	ErrTranscriptRejected = errors.New("transcript rejected by receiver")
)

const (
	transcriptReadEvents = 200
	transcriptReadBytes  = 400 << 10
	// Offload's documented maximum document is 1 MiB of events plus envelope.
	transcriptDocumentLimit = 2 << 20
)

// Tunable for tests.
var (
	transcriptPollInterval = time.Second
	transcriptReadTimeout  = 10 * time.Second
	transcriptFinalGrace   = 5 * time.Second
	transcriptRetryDelay   = 2 * time.Second
)

// TranscriptPage is one bounded read of an invocation's transcript.
type TranscriptPage struct {
	CallerRunID  string
	RunID        string
	CaptureID    string
	Availability string
	State        string
	Reason       string
	Completeness string
	Summary      json.RawMessage
	Events       []json.RawMessage
	CursorIn     string
	CursorOut    string
	HasMore      bool
	EndOfStream  bool
}

// TranscriptSink receives pages in order. A nil error acknowledges the page.
// Other errors are retried with the same cursor; ErrTranscriptRejected stops.
type TranscriptSink interface {
	Deliver(context.Context, TranscriptPage) error
}

type transcriptSinkKey struct{}

// WithTranscriptSink makes managed executions below ctx stream their transcript.
// Without a sink, capture is never requested from Offload.
func WithTranscriptSink(ctx context.Context, sink TranscriptSink) context.Context {
	return context.WithValue(ctx, transcriptSinkKey{}, sink)
}

func transcriptSinkFrom(ctx context.Context) TranscriptSink {
	sink, _ := ctx.Value(transcriptSinkKey{}).(TranscriptSink)
	return sink
}

// HasTranscriptSink reports whether capture will be consumed.
func HasTranscriptSink(ctx context.Context) bool { return transcriptSinkFrom(ctx) != nil }

type transcriptDocument struct {
	SchemaVersion int    `json:"schema_version"`
	Interface     string `json:"interface"`
	Availability  string `json:"availability"`
	Reason        string `json:"reason"`
	Run           struct {
		RunID       string `json:"run_id"`
		CallerRunID string `json:"caller_run_id"`
		CaptureID   string `json:"capture_id"`
	} `json:"run"`
	State        string            `json:"state"`
	Events       []json.RawMessage `json:"events"`
	NextCursor   string            `json:"next_cursor"`
	HasMore      bool              `json:"has_more"`
	EndOfStream  bool              `json:"end_of_stream"`
	Completeness string            `json:"completeness"`
	Summary      json.RawMessage   `json:"summary"`
}

// terminalAvailability values never become readable later in this invocation.
func terminalAvailability(a string) bool {
	switch a {
	case "capture_disabled", "expired", "unreadable", "unsupported_backend", "unsupported_version", "ambiguous", "cursor_invalid", "identity_mismatch", "no_captured_content":
		return true
	}
	return false
}

type transcriptPump struct {
	cancel   context.CancelFunc
	ended    chan struct{}
	done     chan struct{}
	endOnce  sync.Once
	finished sync.Once
}

func startTranscriptPump(ctx context.Context, command string, env []string, callerRunID string, sink TranscriptSink) *transcriptPump {
	pctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	p := &transcriptPump{cancel: cancel, ended: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		p.run(pctx, command, env, callerRunID, sink)
	}()
	return p
}

// finish marks the writer as exited, then allows a bounded time for the ended
// capture to be read and delivered. It is idempotent.
func (p *transcriptPump) finish(grace time.Duration) {
	p.endOnce.Do(func() { close(p.ended) })
	p.finished.Do(func() {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-p.done:
		case <-timer.C:
			p.cancel()
			<-p.done
		}
		p.cancel()
	})
}

func (p *transcriptPump) wait(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (p *transcriptPump) run(ctx context.Context, command string, env []string, callerRunID string, sink TranscriptSink) {
	cursor, lastKey := "", ""
	for ctx.Err() == nil {
		doc, ok := readTranscript(ctx, command, env, callerRunID, cursor)
		if !ok {
			if !p.wait(ctx, transcriptRetryDelay) {
				return
			}
			continue
		}
		page := TranscriptPage{CallerRunID: callerRunID, RunID: doc.Run.RunID, CaptureID: doc.Run.CaptureID, Availability: doc.Availability, State: doc.State, Reason: doc.Reason,
			Completeness: doc.Completeness, Summary: doc.Summary, Events: doc.Events, CursorIn: cursor, CursorOut: doc.NextCursor, HasMore: doc.HasMore, EndOfStream: doc.EndOfStream}
		if doc.Availability == "available" && doc.State == "abandoned" {
			page.EndOfStream = true
		}
		key := page.Availability + "|" + page.State + "|" + page.Completeness
		if len(page.Events) > 0 || key != lastKey || page.EndOfStream {
			if err := sink.Deliver(ctx, page); err != nil {
				if errors.Is(err, ErrTranscriptRejected) || ctx.Err() != nil {
					return
				}
				// Same cursor again: nothing is skipped across a disconnect.
				if !p.wait(ctx, transcriptRetryDelay) {
					return
				}
				continue
			}
			lastKey = key
		}
		if doc.Availability == "available" && doc.NextCursor != "" {
			cursor = doc.NextCursor
		}
		if page.EndOfStream || terminalAvailability(doc.Availability) {
			return
		}
		if doc.HasMore {
			continue
		}
		interval := transcriptPollInterval
		select {
		case <-p.ended:
			interval = transcriptPollInterval / 10
		default:
		}
		if !p.wait(ctx, interval) {
			return
		}
	}
}

// readTranscript runs one bounded, read-only Offload transcript read. It never
// starts a provider session. ok=false means no usable document (retry later).
func readTranscript(ctx context.Context, command string, env []string, callerRunID, cursor string) (transcriptDocument, bool) {
	var doc transcriptDocument
	rctx, cancel := context.WithTimeout(ctx, transcriptReadTimeout)
	defer cancel()
	args := []string{"transcript", "read", "--json", "--caller-run-id", callerRunID, "--limit", strconv.Itoa(transcriptReadEvents), "--max-bytes", strconv.Itoa(transcriptReadBytes)}
	if cursor != "" {
		args = append(args, "--cursor", cursor)
	}
	cmd := exec.CommandContext(rctx, command, args...)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, left: transcriptDocumentLimit}
	if err := cmd.Run(); err != nil {
		return doc, false
	}
	raw := bytes.TrimSpace(out.Bytes())
	if !validJSON(raw) || json.Unmarshal(raw, &doc) != nil || doc.SchemaVersion != 1 || doc.Interface != "offload.transcript" {
		return doc, false
	}
	if len(doc.Events) > 0 || doc.Run.CallerRunID != "" {
		if doc.Run.CallerRunID != callerRunID {
			// A document for another invocation is never forwarded.
			return transcriptDocument{SchemaVersion: 1, Availability: "identity_mismatch", Reason: "transcript belongs to a different invocation"}, true
		}
	}
	kept := doc.Events[:0]
	for _, e := range doc.Events {
		var head struct {
			Seq         int64  `json:"seq"`
			RunID       string `json:"run_id"`
			CallerRunID string `json:"caller_run_id"`
		}
		if json.Unmarshal(e, &head) != nil || head.Seq < 1 || head.CallerRunID != callerRunID || (doc.Run.RunID != "" && head.RunID != doc.Run.RunID) {
			return transcriptDocument{SchemaVersion: 1, Availability: "identity_mismatch", Reason: "transcript event does not belong to this invocation"}, true
		}
		kept = append(kept, redactTranscriptEvent(e, env))
	}
	doc.Events = kept
	return doc, true
}

// redactTranscriptEvent additionally removes this process's own secret
// environment values; Offload's redaction is best effort.
func redactTranscriptEvent(raw json.RawMessage, env []string) json.RawMessage {
	text := string(raw)
	cleaned := redact(text, env)
	if cleaned == text {
		return raw
	}
	if !validJSON([]byte(cleaned)) {
		return raw
	}
	return json.RawMessage(cleaned)
}

type limitedWriter struct {
	w    io.Writer
	left int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > l.left {
		return 0, errors.New("transcript document exceeds limit")
	}
	l.left -= len(p)
	return l.w.Write(p)
}

// transcriptSupported asks Offload's read-only capabilities interface whether
// capture is available. It starts no provider session.
func transcriptSupported(ctx context.Context, command string, env []string) (bool, string) {
	rctx, cancel := context.WithTimeout(ctx, transcriptReadTimeout)
	defer cancel()
	cmd := exec.CommandContext(rctx, command, "transcript", "capabilities", "--json")
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &out, left: 256 << 10}
	if err := cmd.Run(); err != nil {
		return false, "this Offload does not provide the transcript interface"
	}
	var doc struct {
		SchemaVersion int    `json:"schema_version"`
		Interface     string `json:"interface"`
	}
	raw := bytes.TrimSpace(out.Bytes())
	if !validJSON(raw) || json.Unmarshal(raw, &doc) != nil || doc.SchemaVersion != 1 || doc.Interface != "offload.transcript" {
		return false, "this Offload reports an unsupported transcript schema"
	}
	return true, ""
}
