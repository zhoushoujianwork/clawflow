package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type recordingSink struct {
	mu      sync.Mutex
	targets []TranscriptTarget
	ended   []error
}

func (s *recordingSink) Begin(t TranscriptTarget) func(Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets = append(s.targets, t)
	return func(_ Result, err error) {
		s.mu.Lock()
		s.ended = append(s.ended, err)
		s.mu.Unlock()
	}
}

// capturingOffload wraps the helper so both -h and a run can be observed.
func capturingOffload(t *testing.T, supportsCapture bool) (Config, Request, string) {
	t.Helper()
	c, req := fixture(t)
	help := helpFor(RequiredFlags, "")
	if supportsCapture {
		help += "  -capture-transcript offload transcript read\n    \topt in: record this run's messages\n"
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "help.txt"), []byte(help), 0o600); err != nil {
		t.Fatal(err)
	}
	inner := c.Command
	script := "if [ \"$1\" = \"-h\" ]; then cat '" + filepath.Join(dir, "help.txt") + "' >&2; exit 2; fi\nexec '" + inner + "' \"$@\"\n"
	c.Command = writeScript(t, dir, "offload", script)
	return c, req, filepath.Join(t.TempDir(), "calls")
}

func capturedArgs(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var call struct{ Args map[string]string }
	if err := json.Unmarshal(raw, &call); err != nil {
		t.Fatal(err)
	}
	return call.Args
}

func TestCaptureRequestedOnlyForManagedSinkAndKeepsIdentity(t *testing.T) {
	c, req, calls := capturingOffload(t, true)
	t.Setenv("OFFLOAD_TEST_CAPTURE", calls)
	sink := &recordingSink{}
	req.Resume, req.SessionKey = true, "native-actual"
	res, err := Run(WithTranscriptSink(context.Background(), sink), c, req, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	args := capturedArgs(t, calls)
	if _, ok := args["-capture-transcript"]; !ok {
		t.Fatalf("capture not requested: %v", args)
	}
	// Profile, caller/run identity, session resume and policy are unchanged.
	if args["-p"] != "test-profile" || args["-caller-run-id"] != req.RequestID || args["-r"] != "native-actual" || args["-execution-policy"] != "managed-development" {
		t.Fatalf("identity changed: %v", args)
	}
	if res.CallerRunID != req.RequestID || res.Text == "" || res.RunID == "" {
		t.Fatalf("result protocol changed: %+v", res)
	}
	if len(sink.targets) != 1 || !sink.targets[0].Capture || sink.targets[0].CallerRunID != req.RequestID || len(sink.ended) != 1 || sink.ended[0] != nil {
		t.Fatalf("sink: %+v", sink)
	}

	// Without a sink no capture is requested and no new argument appears.
	_ = os.Remove(calls)
	req.RequestID = NewSessionKey()
	if _, err := Run(context.Background(), c, req, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := capturedArgs(t, calls)["-capture-transcript"]; ok {
		t.Fatal("capture requested without a sink")
	}
}

func TestOlderOffloadRunsWithoutCaptureAndSaysUnsupported(t *testing.T) {
	c, req, calls := capturingOffload(t, false)
	t.Setenv("OFFLOAD_TEST_CAPTURE", calls)
	sink := &recordingSink{}
	if _, err := Run(WithTranscriptSink(context.Background(), sink), c, req, nil, nil, nil); err != nil {
		t.Fatalf("an Offload without the flag must still run: %v", err)
	}
	if _, ok := capturedArgs(t, calls)["-capture-transcript"]; ok {
		t.Fatal("unsupported flag was passed")
	}
	if len(sink.targets) != 1 || sink.targets[0].Capture || sink.targets[0].Unsupported == "" {
		t.Fatalf("sink: %+v", sink.targets)
	}
}

func TestSinkSeesFailedRuns(t *testing.T) {
	c, req, calls := capturingOffload(t, true)
	t.Setenv("OFFLOAD_TEST_CAPTURE", calls)
	t.Setenv("OFFLOAD_TEST_ERROR", "execution")
	sink := &recordingSink{}
	if _, err := Run(WithTranscriptSink(context.Background(), sink), c, req, nil, nil, nil); err == nil {
		t.Fatal("expected failure")
	}
	if len(sink.ended) != 1 || sink.ended[0] == nil {
		t.Fatalf("sink must see the failure: %+v", sink.ended)
	}
}

// transcriptStore points the Offload reader at a private temporary store and
// writes one capture file for runID with n message events.
func transcriptStore(t *testing.T, runID, caller string, n int, ended bool) {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	dir := filepath.Join(state, "offload", "transcripts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	add := func(seq int, typ, extra string) {
		b.WriteString(`{"schema_version":1,"seq":` + strconv.Itoa(seq) + `,"ts":"2026-11-05T00:00:00Z","run_id":"` + runID + `","caller_run_id":"` + caller + `","backend":"claude","model":"m","attempt":1,"type":"` + typ + `","capture_id":"0123456789abcdef"` + extra + "}\n")
	}
	add(1, "capture_start", "")
	for i := 0; i < n; i++ {
		add(i+2, "message", `,"role":"assistant","text":"hi"`)
	}
	if ended {
		add(n+2, "execution_end", `,"capture":{"events":3,"content_events":1}`)
	}
	if err := os.WriteFile(filepath.Join(dir, runID+".ndjson"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadTranscriptPagesAndChecksIdentity(t *testing.T) {
	const rid = "0123456789abcdef0123456789abcdef"
	transcriptStore(t, rid, "caller-1", 3, true)
	page, err := ReadTranscript(context.Background(), "", nil, TranscriptQuery{CallerRunID: "caller-1", Limit: 2})
	if err != nil || page.Availability != "available" || len(page.Events) != 2 || !page.HasMore || page.NextCursor == "" || page.Run.RunID != rid {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	next, err := ReadTranscript(context.Background(), "", nil, TranscriptQuery{CallerRunID: "caller-1", RunID: rid, Cursor: page.NextCursor, Limit: 50})
	if err != nil || len(next.Events) != 3 || next.HasMore || !next.EndOfStream || next.Summary == nil || next.Summary.ContentEvents != 1 {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	// A cursor from another capture is reported as invalid, not an error.
	bad, err := ReadTranscript(context.Background(), "", nil, TranscriptQuery{CallerRunID: "caller-1", Cursor: "t1.fedcba9876543210.0.1"})
	if err != nil || bad.Availability != "cursor_invalid" || len(bad.Events) != 0 {
		t.Fatalf("bad=%+v err=%v", bad, err)
	}
	// A run that does not exist is reported as not_found.
	miss, err := ReadTranscript(context.Background(), "", nil, TranscriptQuery{CallerRunID: "nobody"})
	if err != nil || miss.Availability != "not_found" {
		t.Fatalf("miss=%+v err=%v", miss, err)
	}
	// A run id from another invocation is not accepted.
	other, err := ReadTranscript(context.Background(), "", nil, TranscriptQuery{CallerRunID: "someone-else", RunID: rid})
	if err != nil || other.Availability != "identity_mismatch" || len(other.Events) != 0 {
		t.Fatalf("other=%+v err=%v", other, err)
	}
	// Unsafe identifiers and a canceled context never reach the reader.
	for _, q := range []TranscriptQuery{{CallerRunID: "a b"}, {CallerRunID: "-x"}, {CallerRunID: "ok", RunID: "../x"}, {CallerRunID: "ok", Cursor: strings.Repeat("c", 300)}} {
		if _, err := ReadTranscript(context.Background(), "", nil, q); !IsKind(err, "transcript_request") {
			t.Fatalf("%+v: %v", q, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadTranscript(ctx, "", nil, TranscriptQuery{CallerRunID: "caller-1"}); err == nil {
		t.Fatal("canceled context must fail")
	}
}
