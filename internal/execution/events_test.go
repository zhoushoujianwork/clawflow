package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/offloadtest"
)

func TestExecutionEventsHelper(t *testing.T) {
	if os.Getenv("CLAWFLOW_EVENTS_HELPER") != "1" {
		return
	}
	args := offloadtest.Args()
	_, _ = io.Copy(io.Discard, os.Stdin)
	f, err := os.OpenFile(args["-events-file"], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		os.Exit(2)
	}
	mode := os.Getenv("CLAWFLOW_EVENTS_MODE")
	event := func(seq int, typ string, status string, code int, kind string) {
		e := map[string]any{"schema_version": 1, "type": typ, "run_id": "stream-run", "caller_run_id": args["-caller-run-id"], "seq": seq, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "session": "native-actual", "backend": "claude", "model": "profile-model"}
		if typ == "progress" {
			e["native_type"] = "tool_call"
			e["tool_category"] = "Read"
			e["text"] = "private raw text"
			e["args"] = "private raw arguments"
		}
		if typ == "final" {
			e["status"] = status
			e["exit_code"] = code
			e["error_kind"] = kind
		}
		_ = json.NewEncoder(f).Encode(e)
	}
	event(1, "start", "", 0, "")
	event(2, "progress", "", 0, "")
	if mode == "malformed" {
		event(44, "progress", "", 0, "")
	}
	for {
		if _, err := os.Stat(os.Getenv("CLAWFLOW_EVENTS_GATE")); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if mode == "event-file-io" {
		_ = f.Close()
		_ = os.Remove(args["-events-file"])
		time.Sleep(time.Minute)
		os.Exit(2)
	}
	status, code, kind := "ok", 0, ""
	if mode == "launch" || mode == "events_io" {
		status = "error"
		code = 1
		kind = mode
	}
	if mode != "missing-final" {
		event(3, "final", status, code, kind)
	}
	_ = f.Close()
	if mode == "invalid-stdout" {
		fmt.Fprintln(os.Stdout, "invalid stdout")
		os.Exit(0)
	}
	policy := args["-execution-policy"]
	if policy == "" {
		policy = "default"
	}
	if mode == "unknown-policy" {
		policy = "unknown"
	}
	if mode == "empty-policy" {
		policy = ""
	}
	_ = json.NewEncoder(os.Stdout).Encode(Result{Status: status, ExitCode: code, Text: "final answer", Profile: args["-p"], Backend: "claude", Model: "profile-model", RunID: "stream-run", CallerRunID: args["-caller-run-id"], NativeSessionID: "native-actual", ExecutionPolicy: policy, ErrorKind: kind})
	os.Exit(code)
}

func streamingFixture(t *testing.T) (Config, Request, string) {
	t.Helper()
	c, req := fixture(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(t.TempDir(), "offload-stream")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(exe, "'", "'\\''") + "' -test.run=^TestExecutionEventsHelper$ -- \"$@\"\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c.Command = command
	gate := filepath.Join(t.TempDir(), "finish")
	t.Setenv("CLAWFLOW_EVENTS_HELPER", "1")
	t.Setenv("CLAWFLOW_EVENTS_GATE", gate)
	return c, req, gate
}

type liveEvidence struct {
	mu           sync.Mutex
	data         bytes.Buffer
	progress     chan struct{}
	failProgress bool
}

func (w *liveEvidence) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failProgress && bytes.Contains(p, []byte(`"type":"progress"`)) {
		return 0, errors.New("writer failed")
	}
	n, err := w.data.Write(p)
	if bytes.Contains(p, []byte(`"type":"progress"`)) {
		select {
		case w.progress <- struct{}{}:
		default:
		}
	}
	return n, err
}
func (w *liveEvidence) String() string { w.mu.Lock(); defer w.mu.Unlock(); return w.data.String() }

func TestOffloadEvidenceAndObservationBeforeCompletion(t *testing.T) {
	c, req, gate := streamingFixture(t)
	evidence := &liveEvidence{progress: make(chan struct{}, 1)}
	observed := make(chan Observation, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := Run(ctx, c, req, nil, evidence, func(o Observation) { observed <- o }); done <- err }()
	select {
	case <-evidence.progress:
	case err := <-done:
		t.Fatalf("ended before progress: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("no live evidence")
	}
	select {
	case o := <-observed:
		if o.Type != "tool_call" || o.Delta != "" || o.Tool != nil {
			t.Fatalf("observation=%+v", o)
		}
	case <-time.After(time.Second):
		t.Fatal("no live observer")
	}
	select {
	case err := <-done:
		t.Fatalf("ended while fixture gated: %v", err)
	default:
	}
	if strings.Contains(evidence.String(), `"type":"final"`) {
		t.Fatal("premature final")
	}
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	raw := evidence.String()
	if strings.Count(raw, "\n") != 3 || strings.Contains(raw, "private raw") {
		t.Fatalf("duplicate or raw evidence: %s", raw)
	}
}

func TestOffloadRetainsEvidenceOnFailure(t *testing.T) {
	for _, mode := range []string{"timeout", "canceled", "invalid-stdout", "missing-final", "malformed", "events-io", "event-file-io"} {
		t.Run(mode, func(t *testing.T) {
			c, req, gate := streamingFixture(t)
			t.Setenv("CLAWFLOW_EVENTS_MODE", mode)
			if mode == "timeout" {
				req.TimeoutMS = 1500
			}
			evidence := &liveEvidence{progress: make(chan struct{}, 1), failProgress: mode == "events-io"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := Run(ctx, c, req, nil, evidence, nil); done <- err }()
			// Synchronize on retained evidence, not subprocess startup speed.
			// Run's request timeout bounds failures to produce progress.
			if mode != "events-io" {
				select {
				case <-evidence.progress:
				case err := <-done:
					// The runner may finish before this goroutine is scheduled.
					// Accept progress already retained before that completion.
					select {
					case <-evidence.progress:
						done <- err
					default:
						t.Fatalf("ended before progress: %v", err)
					}
				}
				if mode == "canceled" {
					cancel()
				}
				if mode == "invalid-stdout" || mode == "missing-final" || mode == "event-file-io" {
					if err := os.WriteFile(gate, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := <-done
			switch mode {
			case "timeout":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			case "canceled":
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case "invalid-stdout":
				if !IsKind(err, "protocol_invalid_result") {
					t.Fatal(err)
				}
			case "missing-final":
				if !IsKind(err, "protocol_missing_final") {
					t.Fatal(err)
				}
			case "malformed":
				if !IsKind(err, "protocol_invalid_event") {
					t.Fatal(err)
				}
			case "events-io", "event-file-io":
				if !IsKind(err, "events_io") {
					t.Fatal(err)
				}
			}
			want := 2
			if mode == "invalid-stdout" {
				want = 3
			}
			if mode == "events-io" {
				want = 1
			}
			raw := evidence.String()
			if strings.Count(raw, "\n") != want || strings.Contains(raw, "private raw") {
				t.Fatalf("lost prefix or duplicate/raw evidence: %s", raw)
			}
		})
	}
}

func TestOffloadExactDefaultPolicyAndErrorKinds(t *testing.T) {
	for _, mode := range []string{"default", "unknown-policy", "empty-policy", "launch", "events_io"} {
		t.Run(mode, func(t *testing.T) {
			c, req, gate := streamingFixture(t)
			req.PermissionMode = "default"
			t.Setenv("CLAWFLOW_EVENTS_MODE", mode)
			if err := os.WriteFile(gate, nil, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := Run(context.Background(), c, req, nil, nil, nil)
			switch mode {
			case "default":
				if err != nil {
					t.Fatal(err)
				}
			case "launch", "events_io":
				if !IsKind(err, mode) {
					t.Fatal(err)
				}
			default:
				if !IsKind(err, "protocol_policy_mismatch") {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestOffloadInstructionsAdmission256KiB(t *testing.T) {
	c, req := fixture(t)
	capture := filepath.Join(t.TempDir(), "calls")
	t.Setenv("OFFLOAD_TEST_CAPTURE", capture)
	req.Instructions = strings.Repeat("i", MaxInstructionsBytes)
	if err := validateRequest(req); err != nil {
		t.Fatal(err)
	}
	req.Instructions += "x"
	if _, err := Run(context.Background(), c, req, nil, nil, nil); !IsKind(err, "request_limit") {
		t.Fatal(err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("oversized instructions launched child")
	}
	if err := os.WriteFile(filepath.Join(req.Workspace, "AGENTS.md"), []byte(strings.Repeat("a", MaxInstructionsBytes/2)), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Execute(context.Background(), "task", Options{Workspace: req.Workspace, Instructions: strings.Repeat("b", MaxInstructionsBytes/2)})
	if !IsKind(err, "request_limit") {
		t.Fatal(err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("combined oversized instructions launched child")
	}
}

func TestOffloadEventTailPartialRecordsAndBounds(t *testing.T) {
	for _, mode := range []string{"partial", "line-limit", "total-limit"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events")
			f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			var evidence bytes.Buffer
			req := Request{RequestID: "caller"}
			tail := newEventTail(path, req, &evidence, nil, nil)
			defer tail.close()
			event := Event{Version: 1, RunID: "run", CallerRunID: "caller", Seq: 1, Type: "start", Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}
			if err := json.NewEncoder(f).Encode(event); err != nil {
				t.Fatal(err)
			}
			if err := tail.poll(); err != nil {
				t.Fatal(err)
			}
			if strings.Count(evidence.String(), "\n") != 1 {
				t.Fatal("missing start")
			}
			switch mode {
			case "partial":
				event.Seq = 2
				event.Type = "progress"
				raw, _ := json.Marshal(event)
				_, _ = f.Write(raw[:len(raw)/2])
				if err := tail.poll(); err != nil {
					t.Fatal(err)
				}
				if strings.Count(evidence.String(), "\n") != 1 {
					t.Fatal("emitted incomplete record")
				}
				_, _ = f.Write(append(raw[len(raw)/2:], '\n'))
				if err := tail.poll(); err != nil {
					t.Fatal(err)
				}
				if err := tail.poll(); err != nil {
					t.Fatal(err)
				}
				if strings.Count(evidence.String(), "\n") != 2 {
					t.Fatal("lost or duplicated record")
				}
				event.Seq = 3
				event.RunID = "wrong-run"
				_ = json.NewEncoder(f).Encode(event)
				if err := tail.poll(); !IsKind(err, "protocol_invalid_event") {
					t.Fatal(err)
				}
				if strings.Count(evidence.String(), "\n") != 2 {
					t.Fatal("invalid run emitted")
				}
			case "line-limit":
				_, _ = io.WriteString(f, strings.Repeat("x", MaxEventBytes))
				if err := tail.poll(); !IsKind(err, "protocol_output_limit") {
					t.Fatal(err)
				}
				if strings.Count(evidence.String(), "\n") != 1 {
					t.Fatal("invalid line emitted")
				}
			case "total-limit":
				padding := strings.Repeat("p", 1<<20)
				for seq := 2; seq <= 34; seq++ {
					event.Seq = seq
					event.Type = "progress"
					raw, _ := json.Marshal(event)
					var padded map[string]any
					_ = json.Unmarshal(raw, &padded)
					padded["padding"] = padding
					if err := json.NewEncoder(f).Encode(padded); err != nil {
						t.Fatal(err)
					}
				}
				if err := tail.poll(); !IsKind(err, "protocol_output_limit") {
					t.Fatal(err)
				}
				if strings.Count(evidence.String(), "\n") < 2 || strings.Contains(evidence.String(), "padding") {
					t.Fatal("lost prefix or raw metadata leaked")
				}
			}
		})
	}
}
