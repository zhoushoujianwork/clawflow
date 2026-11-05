package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func transcriptScript(t *testing.T, document string, exit string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "doc.json"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "args")
	return writeScript(t, dir, "offload", "echo \"$@\" > '"+argsFile+"'\ncat '"+filepath.Join(dir, "doc.json")+"'\nexit "+exit+"\n")
}

func TestReadTranscriptIsReadOnlyBoundedAndIdentityChecked(t *testing.T) {
	doc := `{"schema_version":1,"interface":"offload.transcript","availability":"available","run":{"run_id":"run-1","caller_run_id":"caller-1","backend":"claude","model":"m","capture_id":"c1"},"state":"running","events":[{"seq":1}],"next_cursor":"t1.c1.10.1","has_more":true,"end_of_stream":false,"completeness":"in_progress"}`
	cmd := transcriptScript(t, doc, "0")
	page, err := ReadTranscript(context.Background(), cmd, []string{"PATH=" + os.Getenv("PATH")}, TranscriptQuery{CallerRunID: "caller-1", Cursor: "t1.c1.0.0", Limit: 50})
	if err != nil || page.Availability != "available" || len(page.Events) != 1 || !page.HasMore || page.NextCursor != "t1.c1.10.1" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	args, _ := os.ReadFile(filepath.Join(filepath.Dir(cmd), "args"))
	for _, want := range []string{"transcript read --json", "--caller-run-id caller-1", "--cursor t1.c1.0.0", "--limit 50"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("args %q missing %q", args, want)
		}
	}
	// Run/task launch flags must never reach the reader.
	for _, banned := range []string{"-p ", "-w", "-r ", "-capture"} {
		if strings.Contains(" "+string(args), " "+banned) {
			t.Fatalf("reader passed %q: %s", banned, args)
		}
	}

	// A document for a different invocation is rejected.
	if _, err := ReadTranscript(context.Background(), cmd, nil, TranscriptQuery{CallerRunID: "someone-else"}); !IsKind(err, "transcript_identity") {
		t.Fatalf("identity: %v", err)
	}
	// Unsafe identifiers never reach a process.
	for _, q := range []TranscriptQuery{{CallerRunID: "a b"}, {CallerRunID: "-x"}, {CallerRunID: "ok", RunID: "../x"}, {CallerRunID: "ok", Cursor: strings.Repeat("c", 300)}} {
		if _, err := ReadTranscript(context.Background(), cmd, nil, q); !IsKind(err, "transcript_request") {
			t.Fatalf("%+v: %v", q, err)
		}
	}
}

func TestReadTranscriptRejectsMalformedAndOversizedOutput(t *testing.T) {
	for name, tc := range map[string]struct{ doc, exit, kind string }{
		"not-json":    {"nope", "0", "transcript_protocol"},
		"wrong-iface": {`{"schema_version":1,"interface":"other","availability":"available","events":[]}`, "0", "transcript_protocol"},
		"unknown":     {`{"schema_version":1,"interface":"offload.transcript","availability":"surprise","events":[]}`, "0", "transcript_protocol"},
		"version":     {`{"schema_version":2,"interface":"offload.transcript","availability":"available","events":[]}`, "0", "transcript_protocol"},
		"dup-keys":    {`{"schema_version":1,"schema_version":1,"interface":"offload.transcript","availability":"available","events":[]}`, "0", "transcript_protocol"},
		"exit-2":      {`{}`, "2", "transcript_read"},
		"too-large":   {`{"schema_version":1,"interface":"offload.transcript","availability":"available","events":[],"pad":"` + strings.Repeat("x", transcriptReadOutput) + `"}`, "0", "transcript_output_limit"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadTranscript(context.Background(), transcriptScript(t, tc.doc, tc.exit), nil, TranscriptQuery{CallerRunID: "caller-1"}); !IsKind(err, tc.kind) {
				t.Fatalf("want %s, got %v", tc.kind, err)
			}
		})
	}
	// Every availability value Offload documents is accepted (exit 0 even when nothing is readable).
	for _, a := range []string{"not_yet_available", "no_captured_content", "capture_disabled", "expired", "unreadable", "unsupported_backend", "unsupported_version", "not_found", "ambiguous", "cursor_invalid", "identity_mismatch"} {
		doc := `{"schema_version":1,"interface":"offload.transcript","availability":"` + a + `","events":[]}`
		page, err := ReadTranscript(context.Background(), transcriptScript(t, doc, "0"), nil, TranscriptQuery{CallerRunID: "caller-1"})
		if err != nil || page.Availability != a {
			t.Fatalf("%s: %+v %v", a, page, err)
		}
	}
}
