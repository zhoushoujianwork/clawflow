package commands

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/joblog"
)

const (
	logPollInterval = 1500 * time.Millisecond
	logFinalDrain   = 45 * time.Second
	logMaxBackoff   = 15 * time.Second
)

// logTransport is the part of workerAPI the log syncer needs.
type logTransport interface {
	postOnce(ctx context.Context, path string, in, out any) error
}

// workerLogSink mirrors the Offload transcript of this worker's own managed
// invocations to the controller over the existing authenticated worker path.
// Each page is bound to the task lease (task id, fencing token) and the
// authenticated worker; the controller rejects anything else. It reads only
// the invocation named by the request id this process generated, through
// Offload's read-only transcript interface. A failure here never affects the
// execution result.
type workerLogSink struct {
	api    logTransport
	taskID string
	fence  int64
	parent context.Context
	read   func(context.Context, string, []string, execution.TranscriptQuery) (execution.TranscriptPage, error)
	every  time.Duration
}

func newWorkerLogSink(parent context.Context, api logTransport, t *coordinator.Task) *workerLogSink {
	return &workerLogSink{api: api, taskID: t.ID, fence: t.FencingToken, parent: parent, read: execution.ReadTranscript, every: logPollInterval}
}

func (s *workerLogSink) Begin(target execution.TranscriptTarget) func(execution.Result, error) {
	run := &logSync{sink: s, target: target, finished: make(chan struct{}), done: make(chan struct{})}
	go run.loop()
	return func(execution.Result, error) {
		close(run.finished)
		select {
		case <-run.done:
		case <-time.After(logFinalDrain + 5*time.Second):
		}
	}
}

type logSync struct {
	sink     *workerLogSink
	target   execution.TranscriptTarget
	finished chan struct{} // closed when the Offload process has ended
	done     chan struct{}
	cursor   string
	acked    int64 // highest Offload sequence the controller has stored
	limited  bool
	stopped  bool // the controller refused this lease; stop uploading
	runID    string
	backend  string
	model    string
}

func (l *logSync) isFinished() bool {
	select {
	case <-l.finished:
		return true
	default:
		return false
	}
}

func (l *logSync) loop() {
	defer close(l.done)
	// Cancellation of the task (timeout, lost lease) must not drop the final
	// drain, so the syncer never inherits the task's cancellation.
	base := context.WithoutCancel(l.sink.parent)
	if !l.target.Capture {
		l.send(base, l.status(joblog.UnsupportedOffload, "installed Offload has no transcript capture", ""), true)
		return
	}
	// Live phase: follow the transcript until the Offload process has ended.
	for !l.isFinished() && !l.stopped {
		if l.step(base, false) {
			return
		}
		select {
		case <-l.finished:
		case <-time.After(l.sink.every):
		}
	}
	if l.stopped {
		return
	}
	// Drain phase: read what remains, bounded in time.
	ctx, cancel := context.WithTimeout(base, logFinalDrain)
	defer cancel()
	for ctx.Err() == nil && !l.stopped {
		if l.step(ctx, true) {
			return
		}
		select {
		case <-ctx.Done():
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// step reads and forwards pages until caught up. It reports whether the
// stream is complete.
func (l *logSync) step(ctx context.Context, finished bool) bool {
	for ctx.Err() == nil && !l.stopped {
		page, err := l.sink.read(ctx, l.target.Command, l.target.Env, execution.TranscriptQuery{CallerRunID: l.target.CallerRunID, RunID: l.runID, Cursor: l.cursor})
		if ctx.Err() != nil {
			return false
		}
		if err != nil {
			if finished {
				l.send(ctx, l.status(joblog.Unreadable, "transcript could not be read", ""), true)
				return true
			}
			return false
		}
		complete, more := l.handle(ctx, page, finished)
		if complete {
			return true
		}
		if !more {
			return false
		}
	}
	return false
}

func (l *logSync) status(availability, reason, state string) coordinator.LogPage {
	return coordinator.LogPage{FencingToken: l.sink.fence, Invocation: l.target.CallerRunID, OffloadRunID: l.runID, Backend: l.backend, Model: l.model,
		Availability: availability, Reason: reason, State: state, FromSeq: l.acked, ToSeq: l.acked, Events: []joblog.Event{}}
}

// handle processes one document. complete means the stream is finished;
// more means another page is immediately available.
func (l *logSync) handle(ctx context.Context, page execution.TranscriptPage, finished bool) (complete, more bool) {
	if page.Run.RunID != "" {
		l.runID, l.backend, l.model = page.Run.RunID, page.Run.Backend, page.Run.Model
	}
	switch page.Availability {
	case joblog.Available:
	case joblog.CursorInvalid:
		// Offload no longer accepts the cursor: re-read from the start. The
		// controller ignores sequences it already stored.
		l.cursor = ""
		return false, false
	case joblog.NotYetAvailable, joblog.NotFound, joblog.NoCapturedContent:
		if !finished {
			return false, false
		}
		l.send(ctx, l.status(page.Availability, page.Reason, page.State), true)
		return true, false
	default:
		// capture_disabled, expired, unreadable, unsupported_*, identity_mismatch, ambiguous
		l.send(ctx, l.status(page.Availability, page.Reason, page.State), true)
		return true, false
	}
	redact := func(v string) string { return execution.RedactText(v, l.target.Env) }
	out := l.status(joblog.Available, "", page.State)
	out.Completeness, out.Summary = page.Completeness, page.Summary
	for _, raw := range page.Events {
		e, seq, ok := joblog.ParseOffload(raw, l.target.CallerRunID, redact)
		if seq > out.ToSeq {
			out.ToSeq = seq
		}
		if ok && seq > l.acked && !l.limited {
			out.Events = append(out.Events, e)
		}
	}
	ended := page.EndOfStream && !page.HasMore
	if len(page.Events) == 0 && !ended {
		return false, false
	}
	ack := l.send(ctx, out, ended)
	switch {
	case ack == nil:
		return false, false
	case !ack.Accepted:
		// The controller is behind this page: restart from its position.
		l.cursor, l.acked = "", ack.LastSeq
		return false, true
	}
	l.acked, l.limited = ack.LastSeq, ack.Limited
	if page.NextCursor != "" {
		l.cursor = page.NextCursor
	}
	return ended, page.HasMore
}

// send uploads one page with retry, so a dropped connection resumes from the
// same cursor. It returns nil if the lease is lost or the context ends.
func (l *logSync) send(ctx context.Context, page coordinator.LogPage, end bool) *coordinator.LogAck {
	page.End = end
	delay := time.Second
	for {
		var ack coordinator.LogAck
		err := l.sink.api.postOnce(ctx, "tasks/"+url.PathEscape(l.sink.taskID)+"/logs", page, &ack)
		if err == nil {
			return &ack
		}
		var status workerHTTPError
		if ctx.Err() != nil || workerFatal(err) || (errors.As(err, &status) && status < 500 && status != 408 && status != 429) {
			if ctx.Err() == nil {
				slog.Warn("worker/log_sync_stopped", "task", l.sink.taskID, "err", err.Error())
			}
			l.stopped = true
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
		if delay *= 2; delay > logMaxBackoff {
			delay = logMaxBackoff
		}
	}
}
