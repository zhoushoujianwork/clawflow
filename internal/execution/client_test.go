package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAGHHelper(t *testing.T) {
	if os.Getenv("CLAWFLOW_TEST_AGH") != "1" {
		return
	}
	var req Request
	if json.NewDecoder(os.Stdin).Decode(&req) != nil {
		os.Exit(2)
	}
	if path := os.Getenv("CLAWFLOW_TEST_RECORD"); path != "" {
		f, _ := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		wd, _ := os.Getwd()
		_ = json.NewEncoder(f).Encode(map[string]any{"request": req, "cwd": wd, "args": os.Args, "api_key": os.Getenv("ANTHROPIC_API_KEY")})
		_ = f.Close()
	}
	mode := os.Getenv("CLAWFLOW_TEST_MODE")
	if mode == "delayed-cleanup" {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM)
		if err := os.WriteFile(os.Getenv("CLAWFLOW_TEST_READY"), []byte("ready"), 0600); err != nil {
			os.Exit(1)
		}
		<-signals
		time.Sleep(3 * time.Second)
		if err := os.WriteFile(os.Getenv("CLAWFLOW_TEST_CLEANUP"), []byte("cleaned"), 0600); err != nil {
			os.Exit(1)
		}
		_ = json.NewEncoder(os.Stdout).Encode(Event{Version: 1, RequestID: req.RequestID, Seq: 1, Type: "result", Result: &Result{Status: "canceled", Text: "cleanup complete", Model: req.Model, SessionKey: req.SessionKey, NativeSessionID: "native-distinct", ErrorKind: "canceled", Error: "execution canceled", Usage: json.RawMessage("null")}})
		os.Exit(130)
	}
	if mode == "hang" {
		signal.Ignore(syscall.SIGTERM)
		c := exec.Command("sleep", "60")
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		_ = c.Start()
		select {}
	}
	enc := json.NewEncoder(os.Stdout)
	seq := 0
	emit := func(e Event) { seq++; e.Version = 1; e.RequestID = req.RequestID; e.Seq = seq; _ = enc.Encode(e) }
	emit(Event{Type: "observation", Observation: &Observation{Type: "text_delta", Delta: "answer\n<!-- clawflow:outcome=done -->"}})
	emit(Event{Type: "observation", Observation: &Observation{Type: "tool_start", Message: "Read"}})
	r := Result{Status: "completed", Text: "", Model: req.Model, SessionKey: req.SessionKey, NativeSessionID: "native-distinct", Usage: json.RawMessage("null")}
	code := 0
	switch mode {
	case "failed":
		r.Status = "failed"
		r.ErrorKind = "auth"
		code = 1
	case "budget":
		if req.MaxBudgetUSD > 0 {
			r.Status = "failed"
			r.ErrorKind = "unsupported_budget"
			code = 1
		}
	case "wrong-session":
		r.SessionKey = NewSessionKey()
	case "exit-mismatch":
		code = 1
	case "missing":
		os.Exit(0)
	}
	emit(Event{Type: "result", Result: &r})
	if mode == "duplicate" {
		emit(Event{Type: "result", Result: &r})
	}
	os.Exit(code)
}

func fixture(t *testing.T) (Config, Request, []string) {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(dir, "agent-host")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(exe, "'", "'\\''") + "' -test.run=^TestAGHHelper$ -- \"$@\"\n"
	if err = os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "execution.yaml")
	if err = os.WriteFile(cfgPath, []byte("test: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Command: command, ConfigPath: cfgPath, NativeCommand: exe, NativeProfile: dir}
	req := Request{Version: 1, RequestID: NewSessionKey(), SessionKey: NewSessionKey(), Workspace: dir, Prompt: "hello", PermissionMode: "bypassPermissions", TimeoutMS: 10000}
	return cfg, req, append(os.Environ(), "CLAWFLOW_TEST_AGH=1")
}

func TestRunContract(t *testing.T) {
	cfg, req, env := fixture(t)
	req.Prompt = strings.Repeat("long input\n", 4000)
	req.Model = "role-model"
	req.Instructions = "instructions"
	req.DisallowedTools = []string{"Edit", "Write", "NotebookEdit"}
	req.MaxBudgetUSD = 1.23
	record := filepath.Join(req.Workspace, "record.jsonl")
	env = append(env, "CLAWFLOW_TEST_RECORD="+record, "ANTHROPIC_API_KEY=test-secret")
	var evidence bytes.Buffer
	result, err := Run(context.Background(), cfg, req, env, &evidence, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "outcome=done") || result.NativeSessionID == req.SessionKey {
		t.Fatalf("result: %+v", result)
	}
	var got struct {
		Request Request  `json:"request"`
		Cwd     string   `json:"cwd"`
		Args    []string `json:"args"`
		APIKey  string   `json:"api_key"`
	}
	raw, _ := os.ReadFile(record)
	if err = json.Unmarshal(bytes.TrimSpace(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.Request.Prompt != req.Prompt || got.Request.MaxBudgetUSD != 1.23 || got.Request.PermissionMode != "bypassPermissions" || got.Request.Instructions != "instructions" || len(got.Request.DisallowedTools) != 3 || got.Cwd != req.Workspace || got.APIKey != "test-secret" {
		t.Fatalf("request transport changed: %+v", got)
	}
	if strings.Contains(strings.Join(got.Args, " "), "test-secret") || strings.Contains(evidence.String(), "test-secret") {
		t.Fatal("secret leaked")
	}
	if !strings.Contains(evidence.String(), `"type":"observation"`) || strings.Contains(evidence.String(), `"type":"assistant"`) {
		t.Fatal("evidence was fabricated")
	}
}

func TestRunFailuresNeverReplay(t *testing.T) {
	for _, mode := range []string{"failed", "wrong-session", "exit-mismatch", "missing", "duplicate", "budget"} {
		t.Run(mode, func(t *testing.T) {
			cfg, req, env := fixture(t)
			record := filepath.Join(req.Workspace, "record")
			env = append(env, "CLAWFLOW_TEST_MODE="+mode, "CLAWFLOW_TEST_RECORD="+record)
			if mode == "budget" {
				req.MaxBudgetUSD = 2
			}
			_, err := Run(context.Background(), cfg, req, env, nil, nil)
			if err == nil {
				t.Fatal("invalid success")
			}
			if mode == "budget" && !IsKind(err, "unsupported_budget") {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(record)
			if bytes.Count(raw, []byte("\n")) != 1 {
				t.Fatal("execution replayed")
			}
		})
	}
}

func TestProtocolValidation(t *testing.T) {
	_, req, _ := fixture(t)
	good := func() string {
		raw, _ := json.Marshal(Event{Version: 1, RequestID: req.RequestID, Seq: 1, Type: "result", Result: &Result{Status: "completed", SessionKey: req.SessionKey, Usage: json.RawMessage("null")}})
		return string(raw) + "\n"
	}
	cases := map[string]string{
		"valid": good(), "missing": "", "malformed": "not json\n", "duplicate": good() + good(),
		"version":        strings.Replace(good(), `"version":1`, `"version":2`, 1),
		"request":        strings.Replace(good(), req.RequestID, "wrong", 1),
		"sequence":       strings.Replace(good(), `"seq":1`, `"seq":2`, 1),
		"session":        strings.Replace(good(), req.SessionKey, "wrong", 1),
		"status":         strings.Replace(good(), `"completed"`, `"unknown"`, 1),
		"usage":          strings.Replace(good(), `"usage":null`, `"usage":{}`, 1),
		"missing-usage":  strings.Replace(good(), `,"usage":null`, "", 1),
		"duplicate-key":  strings.Replace(good(), `"version":1`, `"version":1,"version":1`, 1),
		"null-text":      strings.Replace(good(), `"text":""`, `"text":null`, 1),
		"oversized-line": strings.Repeat("x", MaxEventBytes+1),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseStream(strings.NewReader(data), req, nil, nil, nil)
			if (err == nil) != (name == "valid") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestPreflight(t *testing.T) {
	cfg, req, env := fixture(t)
	cfg.NativeProfile = "relative"
	if _, err := Run(context.Background(), cfg, req, env, nil, nil); err == nil {
		t.Fatal("relative profile accepted")
	}
	cfg, req, env = fixture(t)
	req.Prompt = strings.Repeat("x", MaxRequestBytes)
	if _, err := Run(context.Background(), cfg, req, env, nil, nil); err == nil {
		t.Fatal("oversized request accepted")
	}
	cfg, req, env = fixture(t)
	req.ToolsDisabled = true
	req.AllowedTools = []string{"Read"}
	if _, err := Run(context.Background(), cfg, req, env, nil, nil); err == nil {
		t.Fatal("conflicting tools accepted")
	}
	cfg, req, env = fixture(t)
	req.TimeoutMS = 86400001
	if _, err := Run(context.Background(), cfg, req, env, nil, nil); err == nil {
		t.Fatal("timeout accepted")
	}
}

func TestTimeoutAndCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprint(deadline), func(t *testing.T) {
			cfg, req, env := fixture(t)
			env = append(env, "CLAWFLOW_TEST_MODE=hang")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if deadline {
				req.TimeoutMS = 100
			} else {
				time.AfterFunc(100*time.Millisecond, cancel)
			}
			start := time.Now()
			_, err := Run(ctx, cfg, req, env, nil, nil)
			want := context.Canceled
			if deadline {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) {
				t.Fatalf("error=%v want %v", err, want)
			}
			if time.Since(start) > DefaultTerminateGrace+3*time.Second {
				t.Fatal("owned subprocess tree held stdout open")
			}
		})
	}
}

func TestRedactionAndPartialFailure(t *testing.T) {
	_, req, _ := fixture(t)
	line, _ := json.Marshal(Event{Version: 1, RequestID: req.RequestID, Seq: 1, Type: "observation", Observation: &Observation{Type: "text_delta", Delta: "secret-value partial"}})
	var ev bytes.Buffer
	r, err := parseStream(bytes.NewReader(append(line, '\n')), req, &ev, []string{"ANTHROPIC_API_KEY=secret-value"}, nil)
	if err == nil || r.Text != "[redacted] partial" || strings.Contains(ev.String(), "secret-value") {
		t.Fatalf("partial/redaction: %q %v", r.Text, err)
	}
}

func TestChatResume(t *testing.T) {
	cfg, req, _ := fixture(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_AGH_COMMAND", cfg.Command)
	t.Setenv("CLAWFLOW_AGH_CONFIG", cfg.ConfigPath)
	t.Setenv("CLAWFLOW_AGH_NATIVE", cfg.NativeCommand)
	t.Setenv("CLAWFLOW_AGH_PROFILE", cfg.NativeProfile)
	t.Setenv("CLAWFLOW_TEST_AGH", "1")
	record := filepath.Join(req.Workspace, "record")
	t.Setenv("CLAWFLOW_TEST_RECORD", record)
	var out bytes.Buffer
	if err := Chat(context.Background(), strings.NewReader("first\nsecond\n"), &out, Options{Workspace: req.Workspace, Model: "chat-model", SessionKey: req.SessionKey}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(record)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for i := 0; i < 2; i++ {
		var v struct {
			Request Request `json:"request"`
		}
		if err = dec.Decode(&v); err != nil {
			t.Fatal(err)
		}
		if v.Request.SessionKey != req.SessionKey || v.Request.Resume != (i > 0) || v.Request.Model != "chat-model" {
			t.Fatalf("turn %d: %+v", i, v.Request)
		}
	}
	if dec.Decode(new(any)) != io.EOF {
		t.Fatal("extra execution")
	}
}

func TestExecuteRoleAndAuthSelection(t *testing.T) {
	cfg, req, _ := fixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".clawflow", "config")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	credentials := "claude_providers:\n  - name: selected\n    enabled: true\n    api_key: selected-secret\n    operator_model: role-specific\n  - name: unused\n    enabled: true\n    api_key: unused-secret\n    operator_model: unused-model\n"
	if err := os.WriteFile(filepath.Join(dir, "credentials.yaml"), []byte(credentials), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAWFLOW_AGH_COMMAND", cfg.Command)
	t.Setenv("CLAWFLOW_AGH_CONFIG", cfg.ConfigPath)
	t.Setenv("CLAWFLOW_AGH_NATIVE", cfg.NativeCommand)
	t.Setenv("CLAWFLOW_AGH_PROFILE", cfg.NativeProfile)
	t.Setenv("CLAWFLOW_TEST_AGH", "1")
	t.Setenv("CLAWFLOW_CLAUDE_API_KEY", "")
	t.Setenv("CLAWFLOW_CLAUDE_OPERATOR_MODEL", "")
	record := filepath.Join(req.Workspace, "record")
	t.Setenv("CLAWFLOW_TEST_RECORD", record)
	_, err := Execute(context.Background(), "hello", Options{Workspace: req.Workspace, Role: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(record)
	var got struct {
		Request Request `json:"request"`
		APIKey  string  `json:"api_key"`
	}
	if err = json.Unmarshal(bytes.TrimSpace(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.Request.Model != "role-specific" || got.APIKey != "selected-secret" || bytes.Count(raw, []byte("\n")) != 1 {
		t.Fatal("provider policy changed or replayed")
	}
}

func TestRequestCannotContainSecret(t *testing.T) {
	cfg, req, env := fixture(t)
	req.Instructions = "credential secret-token-value"
	_, err := Run(context.Background(), cfg, req, append(env, "API_TOKEN=secret-token-value"), nil, nil)
	if err == nil {
		t.Fatal("secret request accepted")
	}
}

func TestChatIdleCancellation(t *testing.T) {
	cfg, req, _ := fixture(t)
	t.Setenv("CLAWFLOW_AGH_COMMAND", cfg.Command)
	t.Setenv("CLAWFLOW_AGH_CONFIG", cfg.ConfigPath)
	t.Setenv("CLAWFLOW_AGH_NATIVE", cfg.NativeCommand)
	t.Setenv("CLAWFLOW_AGH_PROFILE", cfg.NativeProfile)
	in, w := io.Pipe()
	defer in.Close()
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Chat(ctx, in, io.Discard, Options{Workspace: req.Workspace}); !errors.Is(err, context.Canceled) {
		t.Fatalf("idle cancellation: %v", err)
	}
}

func TestDelayedGracefulCancellation(t *testing.T) {
	if terminateGrace < 8*time.Second {
		t.Fatal("ACP cancellation grace is too short")
	}
	cfg, req, env := fixture(t)
	req.TimeoutMS = 20000
	ready := filepath.Join(req.Workspace, "ready")
	cleanup := filepath.Join(req.Workspace, "cleanup")
	env = append(env, "CLAWFLOW_TEST_MODE=delayed-cleanup", "CLAWFLOW_TEST_READY="+ready, "CLAWFLOW_TEST_CLEANUP="+cleanup)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() { r, err := Run(ctx, cfg, req, env, nil, nil); done <- outcome{r, err} }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fake AGH did not install its signal handler")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case got := <-done:
		if _, err := os.Stat(cleanup); err != nil {
			t.Fatal("AGH was killed before delayed native cleanup completed")
		}
		if !errors.Is(got.err, context.Canceled) || got.result.Status != "canceled" || got.result.Text != "cleanup complete" {
			t.Fatalf("canceled final result became success or was lost: %+v %v", got.result, got.err)
		}
	case <-time.After(DefaultTerminateGrace + 5*time.Second):
		t.Fatal("cancellation did not finish")
	}
}

func TestExplicitFinalTextWinsOverEarlierOutcome(t *testing.T) {
	_, req, _ := fixture(t)
	var stream bytes.Buffer
	enc := json.NewEncoder(&stream)
	_ = enc.Encode(Event{Version: 1, RequestID: req.RequestID, Seq: 1, Type: "observation", Observation: &Observation{Type: "text_delta", Delta: "Earlier answer\n<!-- clawflow:outcome=done -->"}})
	_ = enc.Encode(Event{Version: 1, RequestID: req.RequestID, Seq: 2, Type: "observation", Observation: &Observation{Type: "tool_start", Message: "Read"}})
	_ = enc.Encode(Event{Version: 1, RequestID: req.RequestID, Seq: 3, Type: "result", Result: &Result{Status: "completed", Text: "Final answer without an outcome marker", Model: req.Model, SessionKey: req.SessionKey, NativeSessionID: "native-distinct", Usage: json.RawMessage("null")}})
	got, err := parseStream(&stream, req, nil, nil, nil)
	if err != nil || got.Text != "Final answer without an outcome marker" {
		t.Fatalf("explicit final answer overwritten: %q %v", got.Text, err)
	}
}
