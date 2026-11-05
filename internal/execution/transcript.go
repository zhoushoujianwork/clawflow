package execution

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/joblog"
	offload "github.com/zhoushoujianwork/offload/pkg/execution"
)

// TranscriptTarget identifies one managed Offload invocation whose transcript
// may be read. CallerRunID is the ClawFlow request id passed to Offload as
// -caller-run-id; it is the only identity a reader needs, so a reader cannot be
// pointed at another invocation.
type TranscriptTarget struct {
	CallerRunID string
	Command     string
	Env         []string
	// Capture reports whether -capture-transcript was passed. When false,
	// Unsupported explains why (for example an Offload without the flag).
	Capture     bool
	Unsupported string
}

// TranscriptSink receives managed invocations. Begin must not block: it returns
// a callback that Run invokes once the Offload process has ended.
type TranscriptSink interface {
	Begin(TranscriptTarget) (end func(Result, error))
}

type transcriptSinkKey struct{}

// WithTranscriptSink makes Run request Offload transcript capture for its
// invocations (when the installed Offload supports it) and report them to sink.
// Capture stays off for any Run whose context has no sink.
func WithTranscriptSink(ctx context.Context, sink TranscriptSink) context.Context {
	return context.WithValue(ctx, transcriptSinkKey{}, sink)
}

func transcriptSink(ctx context.Context) TranscriptSink {
	s, _ := ctx.Value(transcriptSinkKey{}).(TranscriptSink)
	return s
}

var captureFlag = regexp.MustCompile(`(?m)^ {1,8}--?capture-transcript\b`)

var captureCache sync.Map // command path|mtime|size -> bool

// captureSupported reads `offload -h`, the same bounded, read-only probe used
// for readiness. It never runs `offload transcript ...`, which older versions
// would treat as a task.
func captureSupported(ctx context.Context, command string) bool {
	key := command
	if st, err := os.Stat(command); err == nil {
		key += "|" + st.ModTime().UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(st.Size(), 10)
	}
	if v, ok := captureCache.Load(key); ok {
		return v.(bool)
	}
	out, problem := runHelpProbe(ctx, command)
	ok := problem == "" && captureFlag.Match(out)
	if problem == "" {
		captureCache.Store(key, ok)
	}
	return ok
}

// TranscriptQuery selects a bounded page of one invocation's transcript.
type TranscriptQuery struct {
	CallerRunID, RunID, Cursor string
	Limit, MaxBytes            int
}

// TranscriptPage is the validated subset of `offload transcript read --json`.
type TranscriptPage struct {
	Availability string
	Reason       string
	State        string
	Completeness string
	Run          struct{ RunID, CallerRunID, Backend, Model, CaptureID string }
	Events       []json.RawMessage
	NextCursor   string
	HasMore      bool
	EndOfStream  bool
	Summary      *joblog.Summary
}

// ReadTranscript reads one page through Offload's public pkg/execution reader.
// It starts no provider and takes no worker slot. command and env are accepted
// for the injected reader signature but unused: the reader opens the local
// capture files directly, with no subprocess.
func ReadTranscript(ctx context.Context, command string, env []string, q TranscriptQuery) (TranscriptPage, error) {
	var page TranscriptPage
	if !joblog.ValidID(q.CallerRunID) || (q.RunID != "" && !joblog.ValidID(q.RunID)) || len(q.Cursor) > 256 {
		return page, &Error{Kind: "transcript_request"}
	}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	if q.Limit < 1 || q.Limit > 1000 {
		q.Limit = joblog.MaxPageEvents
	}
	if q.MaxBytes < 1 || q.MaxBytes > 1<<20 {
		q.MaxBytes = 256 << 10
	}
	doc := offload.ReadTranscript(offload.TranscriptReadOptions{RunID: q.RunID, CallerRunID: q.CallerRunID, Cursor: q.Cursor, Limit: q.Limit, MaxBytes: q.MaxBytes})
	if doc.SchemaVersion != 1 || doc.Interface != "offload.transcript" || !joblog.ValidAvailability(string(doc.Availability)) || len(doc.Events) > q.Limit || len(doc.NextCursor) > 256 {
		return page, &Error{Kind: "transcript_protocol"}
	}
	page = TranscriptPage{Availability: string(doc.Availability), Reason: sanitizeText(doc.Reason, 300), State: sanitizeText(doc.State, 32), Completeness: sanitizeText(doc.Completeness, 32),
		Events: doc.Events, NextCursor: doc.NextCursor, HasMore: doc.HasMore, EndOfStream: doc.EndOfStream}
	if doc.Summary != nil {
		page.Summary = &joblog.Summary{Events: doc.Summary.Events, ContentEvents: doc.Summary.ContentEvents, Gaps: doc.Summary.Gaps, TruncatedEvents: doc.Summary.TruncatedEvents,
			DroppedEvents: doc.Summary.DroppedEvents, DroppedBytes: doc.Summary.DroppedBytes, CaptureTruncated: doc.Summary.CaptureTruncated, WriteFailed: doc.Summary.WriteFailed,
			SkippedBlocks: doc.Summary.SkippedBlocks, Unrecognized: doc.Summary.Unrecognized, OpenToolCalls: doc.Summary.OpenToolCalls}
	}
	if doc.Run != nil {
		page.Run.RunID, page.Run.CallerRunID, page.Run.Backend = doc.Run.RunID, doc.Run.CallerRunID, sanitizeText(doc.Run.Backend, 32)
		page.Run.Model, page.Run.CaptureID = sanitizeText(doc.Run.Model, 128), sanitizeText(doc.Run.CaptureID, 64)
		// A document for another invocation is never accepted.
		if doc.Run.CallerRunID != q.CallerRunID || (doc.Run.RunID != "" && !joblog.ValidID(doc.Run.RunID)) {
			return TranscriptPage{}, &Error{Kind: "transcript_identity"}
		}
	}
	return page, nil
}

// RedactText removes values of credential-named environment variables from s.
func RedactText(s string, env []string) string { return redact(s, env) }
