package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/joblog"
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

const (
	transcriptReadTimeout = 15 * time.Second
	// Offload caps one page at 1 MiB of events; JSON escaping and metadata can add to it.
	transcriptReadOutput = 4 << 20
)

// ReadTranscript runs the read-only Offload transcript interface. It starts no
// provider and takes no worker slot. Exit status 0 means a document was printed.
func ReadTranscript(ctx context.Context, command string, env []string, q TranscriptQuery) (TranscriptPage, error) {
	var page TranscriptPage
	if !joblog.ValidID(q.CallerRunID) || (q.RunID != "" && !joblog.ValidID(q.RunID)) || len(q.Cursor) > 256 {
		return page, &Error{Kind: "transcript_request"}
	}
	if q.Limit < 1 || q.Limit > 1000 {
		q.Limit = joblog.MaxPageEvents
	}
	if q.MaxBytes < 1 || q.MaxBytes > 1<<20 {
		q.MaxBytes = 256 << 10
	}
	args := []string{"transcript", "read", "--json", "--caller-run-id", q.CallerRunID, "--limit", strconv.Itoa(q.Limit), "--max-bytes", strconv.Itoa(q.MaxBytes)}
	if q.RunID != "" {
		args = append(args, "--run-id", q.RunID)
	}
	if q.Cursor != "" {
		args = append(args, "--cursor", q.Cursor)
	}
	ctx, cancel := context.WithTimeout(ctx, transcriptReadTimeout)
	defer cancel()
	out := newBoundedBuffer(transcriptReadOutput)
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = os.TempDir()
	cmd.Env = env
	cmd.Stdout = out
	cmd.WaitDelay = time.Second
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return page, &Error{Kind: "launch"}
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-out.over:
		terminateProcessGroupSync(cmd, time.Second)
		<-done
		return page, &Error{Kind: "transcript_output_limit"}
	}
	if waitErr != nil && !errors.Is(waitErr, exec.ErrWaitDelay) {
		if ctx.Err() != nil {
			return page, ctx.Err()
		}
		return page, &Error{Kind: "transcript_read"}
	}
	select {
	case <-out.over:
		return page, &Error{Kind: "transcript_output_limit"}
	default:
	}
	raw := bytes.TrimSpace(out.bytes())
	var doc struct {
		SchemaVersion int    `json:"schema_version"`
		Interface     string `json:"interface"`
		Availability  string `json:"availability"`
		Reason        string `json:"reason"`
		State         string `json:"state"`
		Run           *struct {
			RunID       string `json:"run_id"`
			CallerRunID string `json:"caller_run_id"`
			Backend     string `json:"backend"`
			Model       string `json:"model"`
			CaptureID   string `json:"capture_id"`
		} `json:"run"`
		Events       []json.RawMessage `json:"events"`
		NextCursor   string            `json:"next_cursor"`
		HasMore      bool              `json:"has_more"`
		EndOfStream  bool              `json:"end_of_stream"`
		Completeness string            `json:"completeness"`
		Summary      *joblog.Summary   `json:"summary"`
	}
	if !validJSON(raw) || json.Unmarshal(raw, &doc) != nil || doc.Interface != "offload.transcript" || doc.SchemaVersion != 1 || !joblog.ValidAvailability(doc.Availability) || len(doc.Events) > q.Limit || len(doc.NextCursor) > 256 {
		return page, &Error{Kind: "transcript_protocol"}
	}
	page = TranscriptPage{Availability: doc.Availability, Reason: sanitizeText(doc.Reason, 300), State: sanitizeText(doc.State, 32), Completeness: sanitizeText(doc.Completeness, 32),
		Events: doc.Events, NextCursor: doc.NextCursor, HasMore: doc.HasMore, EndOfStream: doc.EndOfStream, Summary: doc.Summary}
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
