package commands

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/joblog"
)

// fakeController models the log endpoint's sequence semantics without a database.
type fakeController struct {
	mu       sync.Mutex
	last     int64
	stored   []joblog.Event
	pages    []coordinator.LogPage
	failNext int // transport failures to inject before accepting
	status   int // when non-zero, answer with this HTTP error
	paths    []string
}

func (f *fakeController) postOnce(_ context.Context, path string, in, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, path)
	if f.status != 0 {
		return workerHTTPError(f.status)
	}
	if f.failNext > 0 {
		f.failNext--
		return workerHTTPError(503)
	}
	raw, _ := json.Marshal(in)
	var page coordinator.LogPage
	_ = json.Unmarshal(raw, &page)
	f.pages = append(f.pages, page)
	ack := coordinator.LogAck{LastSeq: f.last}
	if page.FromSeq <= f.last {
		ack.Accepted = true
		for _, e := range page.Events {
			if e.Seq > f.last {
				f.stored = append(f.stored, e)
				ack.Stored++
			} else {
				ack.Duplicates++
			}
		}
		if page.ToSeq > f.last {
			f.last = page.ToSeq
		}
		ack.LastSeq = f.last
	}
	b, _ := json.Marshal(ack)
	return json.Unmarshal(b, out)
}

func (f *fakeController) seqs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int64
	for _, e := range f.stored {
		out = append(out, e.Seq)
	}
	return out
}

func tevent(seq int, typ, extra string) json.RawMessage {
	body := `{"schema_version":1,"seq":` + strconv.Itoa(seq) + `,"ts":"2026-11-02T00:00:00Z","caller_run_id":"req-1","attempt":1,"type":"` + typ + `"`
	if extra != "" {
		body += "," + extra
	}
	return json.RawMessage(body + "}")
}

func newTestSync(c *fakeController, read func(context.Context, string, []string, execution.TranscriptQuery) (execution.TranscriptPage, error), capture bool) *logSync {
	sink := &workerLogSink{api: c, taskID: "task-1", fence: 7, parent: context.Background(), read: read, every: 5 * time.Millisecond}
	return &logSync{sink: sink, target: execution.TranscriptTarget{CallerRunID: "req-1", Command: "offload", Capture: capture}, finished: make(chan struct{}), done: make(chan struct{})}
}

func tpage(avail string, hasMore, end bool, cursor string, events ...json.RawMessage) execution.TranscriptPage {
	p := execution.TranscriptPage{Availability: avail, Events: events, NextCursor: cursor, HasMore: hasMore, EndOfStream: end, State: "running"}
	p.Run.RunID, p.Run.CallerRunID, p.Run.Backend = "run-1", "req-1", "claude"
	return p
}

func TestLogSyncForwardsLiveAppendsAndFinishes(t *testing.T) {
	c := &fakeController{}
	var mu sync.Mutex
	step := 0
	l := newTestSync(c, func(_ context.Context, _ string, _ []string, q execution.TranscriptQuery) (execution.TranscriptPage, error) {
		mu.Lock()
		defer mu.Unlock()
		if q.CallerRunID != "req-1" {
			t.Errorf("read a different invocation: %q", q.CallerRunID)
		}
		step++
		switch step {
		case 1:
			return tpage("not_yet_available", false, false, ""), nil
		case 2:
			return tpage("available", false, false, "c2", tevent(1, "capture_start", ""), tevent(2, "message", `"role":"assistant","text":"hello"`)), nil
		case 3:
			if q.Cursor != "c2" {
				t.Errorf("did not continue from cursor: %q", q.Cursor)
			}
			return tpage("available", false, false, "c3", tevent(3, "tool_call", `"tool_call_id":"t1","tool_name":"Bash","arguments":"ls"`)), nil
		default:
			return tpage("available", false, true, "c4", tevent(4, "tool_result", `"tool_call_id":"t1","output":"a.go","exit_code":0`), tevent(5, "execution_end", `"status":"ok"`)), nil
		}
	}, true)
	go l.loop()
	time.Sleep(60 * time.Millisecond)
	close(l.finished)
	select {
	case <-l.done:
	case <-time.After(5 * time.Second):
		t.Fatal("syncer did not finish")
	}
	if got := c.seqs(); len(got) != 5 || got[0] != 1 || got[4] != 5 {
		t.Fatalf("stored %v", got)
	}
	last := c.pages[len(c.pages)-1]
	if !last.End || last.FencingToken != 7 || last.Invocation != "req-1" || !strings.HasSuffix(c.paths[0], "/logs") {
		t.Fatalf("last=%+v paths=%v", last, c.paths)
	}
	// Tool result keeps the id that ties it to its call.
	var result joblog.Event
	for _, e := range c.stored {
		if e.Type == "tool_result" {
			result = e
		}
	}
	if result.ToolCallID != "t1" || result.Output != "a.go" {
		t.Fatalf("tool result lost correlation: %+v", result)
	}
}

func TestLogSyncResumesAfterDisconnectAndDuplicates(t *testing.T) {
	c := &fakeController{failNext: 2}
	reads := 0
	l := newTestSync(c, func(_ context.Context, _ string, _ []string, q execution.TranscriptQuery) (execution.TranscriptPage, error) {
		reads++
		if reads == 1 {
			return tpage("available", false, true, "c1", tevent(1, "message", `"role":"assistant","text":"a"`), tevent(2, "message", `"role":"assistant","text":"b"`)), nil
		}
		return execution.TranscriptPage{}, errors.New("unexpected extra read")
	}, true)
	close(l.finished)
	l.sink.every = time.Millisecond
	// Two transport failures are retried with the same page, never skipped.
	start := time.Now()
	go l.loop()
	<-l.done
	if time.Since(start) < time.Second {
		t.Fatal("expected backoff between retries")
	}
	if got := c.seqs(); len(got) != 2 {
		t.Fatalf("stored %v", got)
	}
	// Replaying a page the controller already stored adds nothing.
	ack := coordinator.LogAck{}
	if err := c.postOnce(context.Background(), "tasks/task-1/logs", c.pages[0], &ack); err != nil || ack.Stored != 0 || ack.Duplicates != 2 {
		t.Fatalf("replay ack=%+v err=%v", ack, err)
	}
}

func TestLogSyncRestartsFromControllerPositionWhenAheadOrCursorInvalid(t *testing.T) {
	c := &fakeController{last: 1}
	calls := 0
	l := newTestSync(c, func(_ context.Context, _ string, _ []string, q execution.TranscriptQuery) (execution.TranscriptPage, error) {
		calls++
		switch calls {
		case 1:
			return tpage("cursor_invalid", false, false, ""), nil
		case 2:
			if q.Cursor != "" {
				t.Errorf("cursor not reset: %q", q.Cursor)
			}
			return tpage("available", false, true, "c", tevent(1, "message", `"text":"old"`), tevent(2, "message", `"text":"new"`)), nil
		}
		return execution.TranscriptPage{}, errors.New("extra")
	}, true)
	close(l.finished)
	go l.loop()
	<-l.done
	if got := c.seqs(); len(got) != 1 || got[0] != 2 {
		t.Fatalf("only the unseen tail may be stored, got %v", got)
	}
}

func TestLogSyncReportsEveryNoContentState(t *testing.T) {
	for _, avail := range []string{"capture_disabled", "unsupported_backend", "unsupported_version", "expired", "unreadable", "identity_mismatch", "ambiguous", "no_captured_content", "not_found", "not_yet_available"} {
		t.Run(avail, func(t *testing.T) {
			c := &fakeController{}
			l := newTestSync(c, func(context.Context, string, []string, execution.TranscriptQuery) (execution.TranscriptPage, error) {
				return tpage(avail, false, false, ""), nil
			}, true)
			close(l.finished)
			go l.loop()
			select {
			case <-l.done:
			case <-time.After(5 * time.Second):
				t.Fatal("did not finish")
			}
			last := c.pages[len(c.pages)-1]
			if last.Availability != avail || !last.End || len(last.Events) != 0 {
				t.Fatalf("%+v", last)
			}
		})
	}
}

func TestLogSyncUnsupportedOffloadAndReadFailure(t *testing.T) {
	c := &fakeController{}
	l := newTestSync(c, func(context.Context, string, []string, execution.TranscriptQuery) (execution.TranscriptPage, error) {
		t.Error("an Offload without capture must never be read")
		return execution.TranscriptPage{}, nil
	}, false)
	go l.loop()
	<-l.done
	if len(c.pages) != 1 || c.pages[0].Availability != joblog.UnsupportedOffload || !c.pages[0].End {
		t.Fatalf("%+v", c.pages)
	}

	c = &fakeController{}
	l = newTestSync(c, func(context.Context, string, []string, execution.TranscriptQuery) (execution.TranscriptPage, error) {
		return execution.TranscriptPage{}, &execution.Error{Kind: "transcript_read"}
	}, true)
	close(l.finished)
	go l.loop()
	<-l.done
	if len(c.pages) != 1 || c.pages[0].Availability != joblog.Unreadable {
		t.Fatalf("%+v", c.pages)
	}
}

func TestLogSyncStopsWhenLeaseIsRefusedAndNeverBlocksExecution(t *testing.T) {
	c := &fakeController{status: 409}
	reads := 0
	l := newTestSync(c, func(context.Context, string, []string, execution.TranscriptQuery) (execution.TranscriptPage, error) {
		reads++
		return tpage("available", false, false, "c", tevent(reads, "message", `"text":"x"`)), nil
	}, true)
	go l.loop()
	select {
	case <-l.done:
	case <-time.After(5 * time.Second):
		t.Fatal("stale lease must stop uploads")
	}
	if len(c.paths) != 1 {
		t.Fatalf("retried a refused upload: %v", c.paths)
	}
	// The sink's end callback returns promptly even when the syncer already stopped.
	sink := &workerLogSink{api: c, taskID: "t", parent: context.Background(), read: func(context.Context, string, []string, execution.TranscriptQuery) (execution.TranscriptPage, error) {
		return execution.TranscriptPage{}, errors.New("down")
	}, every: time.Millisecond}
	done := make(chan struct{})
	go func() {
		sink.Begin(execution.TranscriptTarget{CallerRunID: "req-1", Capture: true})(execution.Result{}, nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(logFinalDrain + 10*time.Second):
		t.Fatal("end callback blocked")
	}
}

func TestLogSyncRedactsSecretsAndBoundsFields(t *testing.T) {
	c := &fakeController{}
	l := newTestSync(c, func(context.Context, string, []string, execution.TranscriptQuery) (execution.TranscriptPage, error) {
		return tpage("available", false, true, "c", tevent(1, "tool_result", `"output":"token=SECRET-VALUE-123 `+strings.Repeat("y", 100000)+`"`), tevent(2, "thinking", `"text":"hidden"`)), nil
	}, true)
	l.target.Env = []string{"MY_SERVICE_TOKEN=SECRET-VALUE-123"}
	close(l.finished)
	go l.loop()
	<-l.done
	if len(c.stored) != 1 {
		t.Fatalf("unknown event types must be skipped, stored %d", len(c.stored))
	}
	e := c.stored[0]
	if strings.Contains(e.Output, "SECRET-VALUE-123") || len(e.Output) > joblog.MaxOutputBytes || e.Truncated["output"] == 0 {
		t.Fatalf("redaction/bounds failed: len=%d trunc=%v", len(e.Output), e.Truncated)
	}
	// The skipped record is still acknowledged by sequence so reads move past it.
	if c.last != 2 {
		t.Fatalf("last=%d", c.last)
	}
}

func TestLogSyncIgnoresEventsFromAnotherInvocation(t *testing.T) {
	c := &fakeController{}
	foreign := json.RawMessage(`{"schema_version":1,"seq":1,"caller_run_id":"someone-else","type":"message","text":"leak"}`)
	l := newTestSync(c, func(context.Context, string, []string, execution.TranscriptQuery) (execution.TranscriptPage, error) {
		return tpage("available", false, true, "c", foreign), nil
	}, true)
	close(l.finished)
	go l.loop()
	<-l.done
	if len(c.stored) != 0 {
		t.Fatalf("foreign event forwarded: %+v", c.stored)
	}
}
