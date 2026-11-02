package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingSink struct {
	mu      sync.Mutex
	pages   []TranscriptPage
	failing int // fail this many deliveries with a retryable error
	reject  bool
}

func (s *recordingSink) Deliver(_ context.Context, p TranscriptPage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reject {
		return ErrTranscriptRejected
	}
	if s.failing > 0 {
		s.failing--
		return errors.New("disconnected")
	}
	s.pages = append(s.pages, p)
	return nil
}
func (s *recordingSink) snapshot() []TranscriptPage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TranscriptPage(nil), s.pages...)
}

func tevent(seq int, kind string) string {
	return fmt.Sprintf(`{"schema_version":1,"seq":%d,"ts":"2026-11-02T00:00:0%dZ","run_id":"r1","caller_run_id":"{{CALLER}}","type":%q,"text":"t%d"}`, seq, seq%10, kind, seq)
}

func pagesFile(t *testing.T, pages map[string]string) {
	t.Helper()
	obj := map[string]json.RawMessage{}
	for k, v := range pages {
		obj[k] = json.RawMessage(v)
	}
	raw, _ := json.Marshal(obj)
	path := filepath.Join(t.TempDir(), "pages.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OFFLOAD_TEST_TRANSCRIPT_PAGES", path)
}

func doc(availability, state string, events []string, next string, more, end bool) string {
	return fmt.Sprintf(`{"schema_version":1,"interface":"offload.transcript","availability":%q,"run":{"run_id":"r1","caller_run_id":"{{CALLER}}","capture_id":"c1"},"state":%q,"events":[%s],"next_cursor":%q,"has_more":%t,"end_of_stream":%t,"completeness":"complete"}`,
		availability, state, strings.Join(events, ","), next, more, end)
}

func fastTranscriptTimers(t *testing.T) {
	t.Helper()
	oldPoll, oldRetry, oldGrace := transcriptPollInterval, transcriptRetryDelay, transcriptFinalGrace
	transcriptPollInterval, transcriptRetryDelay, transcriptFinalGrace = 10*time.Millisecond, 10*time.Millisecond, 3*time.Second
	t.Cleanup(func() {
		transcriptPollInterval, transcriptRetryDelay, transcriptFinalGrace = oldPoll, oldRetry, oldGrace
	})
}

func TestRunRequestsCaptureOnlyWithSinkAndReplaysFromCursor(t *testing.T) {
	fastTranscriptTimers(t)
	c, req := fixture(t)
	req.CaptureTranscript = true
	capture := filepath.Join(t.TempDir(), "calls")
	t.Setenv("OFFLOAD_TEST_CAPTURE", capture)
	log := filepath.Join(t.TempDir(), "reads")
	t.Setenv("OFFLOAD_TEST_TRANSCRIPT_LOG", log)
	pagesFile(t, map[string]string{
		"":    doc("available", "running", []string{tevent(1, "capture_start"), tevent(2, "message")}, "c.2", true, false),
		"c.2": doc("available", "ended", []string{tevent(3, "tool_call"), tevent(4, "execution_end")}, "c.4", false, true),
	})
	sink := &recordingSink{failing: 1}
	ctx := WithTranscriptSink(context.Background(), sink)
	result, err := Run(ctx, c, req, nil, nil, nil)
	if err != nil || !strings.Contains(result.Text, "outcome=done") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	raw, _ := os.ReadFile(capture)
	if !strings.Contains(string(raw), `"-capture-transcript":"true"`) && !strings.Contains(string(raw), "-capture-transcript") {
		t.Fatalf("capture flag not passed: %s", raw)
	}
	pages := sink.snapshot()
	if len(pages) != 2 || len(pages[0].Events) != 2 || pages[0].CursorIn != "" || pages[1].CursorIn != "c.2" || !pages[1].EndOfStream {
		t.Fatalf("pages=%+v", pages)
	}
	// The failed first delivery re-read the same (empty) cursor, never skipping ahead.
	reads, _ := os.ReadFile(log)
	if !strings.HasPrefix(string(reads), req.RequestID+"|\n"+req.RequestID+"|\n") {
		t.Fatalf("reads=%q", reads)
	}
}

func TestRunWithoutSinkNeverCapturesOrReads(t *testing.T) {
	c, req := fixture(t)
	req.CaptureTranscript = true
	capture := filepath.Join(t.TempDir(), "calls")
	t.Setenv("OFFLOAD_TEST_CAPTURE", capture)
	log := filepath.Join(t.TempDir(), "reads")
	t.Setenv("OFFLOAD_TEST_TRANSCRIPT_LOG", log)
	if _, err := Run(context.Background(), c, req, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(capture)
	if strings.Contains(string(raw), "capture-transcript") {
		t.Fatalf("capture requested without consumer: %s", raw)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatalf("transcript read without consumer: %v", err)
	}
}

func TestRunWithOlderOffloadRunsUnchangedAndReportsUnsupported(t *testing.T) {
	c, req := fixture(t)
	req.CaptureTranscript = true
	capture := filepath.Join(t.TempDir(), "calls")
	t.Setenv("OFFLOAD_TEST_CAPTURE", capture)
	t.Setenv("OFFLOAD_TEST_TRANSCRIPT_UNSUPPORTED", "1")
	sink := &recordingSink{}
	result, err := Run(WithTranscriptSink(context.Background(), sink), c, req, nil, nil, nil)
	if err != nil || result.Status != "ok" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	raw, _ := os.ReadFile(capture)
	if strings.Contains(string(raw), "capture-transcript") {
		t.Fatalf("flag passed to an Offload that lacks it: %s", raw)
	}
	pages := sink.snapshot()
	if len(pages) != 1 || pages[0].Availability != "unsupported_version" || !pages[0].EndOfStream {
		t.Fatalf("pages=%+v", pages)
	}
}

func TestTranscriptFailureNeverFailsExecution(t *testing.T) {
	fastTranscriptTimers(t)
	transcriptFinalGrace = 200 * time.Millisecond
	c, req := fixture(t)
	req.CaptureTranscript = true
	t.Setenv("OFFLOAD_TEST_TRANSCRIPT_PAGES", filepath.Join(t.TempDir(), "missing.json")) // reads exit 2
	sink := &recordingSink{reject: true}
	result, err := Run(WithTranscriptSink(context.Background(), sink), c, req, nil, nil, nil)
	if err != nil || result.Status != "ok" || !strings.Contains(result.Text, "outcome=done") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestReadTranscriptRejectsForeignInvocation(t *testing.T) {
	c, req := fixture(t)
	foreign := strings.ReplaceAll(doc("available", "ended", []string{tevent(1, "message")}, "c.1", false, true), "{{CALLER}}", "someone-else")
	pagesFile(t, map[string]string{"": foreign})
	got, ok := readTranscript(context.Background(), c.Command, os.Environ(), req.RequestID, "")
	if !ok || got.Availability != "identity_mismatch" || len(got.Events) != 0 {
		t.Fatalf("got=%+v ok=%v", got, ok)
	}
}

func TestReadTranscriptRedactsEnvironmentSecrets(t *testing.T) {
	c, req := fixture(t)
	secret := "s3cr3t-value-12345"
	event := strings.Replace(tevent(1, "message"), `"text":"t1"`, `"text":"token `+secret+` leaked"`, 1)
	pagesFile(t, map[string]string{"": doc("available", "ended", []string{event}, "c.1", false, true)})
	env := append(os.Environ(), "SOME_API_TOKEN="+secret)
	got, ok := readTranscript(context.Background(), c.Command, env, req.RequestID, "")
	if !ok || len(got.Events) != 1 || strings.Contains(string(got.Events[0]), secret) {
		t.Fatalf("got=%+v ok=%v", got, ok)
	}
}
