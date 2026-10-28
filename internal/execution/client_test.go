package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/agentcapacity"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/offloadtest"
)

func TestOffloadHelper(t *testing.T) {
	if os.Getenv("CLAWFLOW_OFFLOAD_TEST_HELPER") != "1" {
		return
	}
	if os.Getenv("CLAWFLOW_EXPECT_SLOT") == "1" && agentcapacity.Used() != 1 {
		os.Exit(3)
	}
	args := offloadtest.Args()
	if args["-w"] != "" {
		offloadtest.Helper(t)
		return
	}
	// The shared historical fixture emits an empty read-only policy. Match the
	// delivered CLI's explicit default while capturing the actual argument set.
	prompt, _ := io.ReadAll(os.Stdin)
	instructions, err := os.ReadFile(args["-instructions-file"])
	if err != nil {
		os.Exit(2)
	}
	if capture := os.Getenv("OFFLOAD_TEST_CAPTURE"); capture != "" {
		f, err := os.OpenFile(capture, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
		if err != nil {
			os.Exit(2)
		}
		cwd, _ := os.Getwd()
		_ = json.NewEncoder(f).Encode(map[string]any{"args": args, "prompt": string(prompt), "instructions": string(instructions), "cwd": cwd})
		_ = f.Close()
	}
	args["-execution-policy"] = "default"
	text := os.Getenv("OFFLOAD_TEST_TEXT")
	if text == "" {
		text = "Final answer\n<!-- clawflow:outcome=done -->"
	}
	offloadtest.Emit(args, text, os.Getenv("OFFLOAD_TEST_ERROR"), os.Getenv("OFFLOAD_TEST_MODE"))
}
func fixture(t *testing.T) (Config, Request) {
	t.Helper()
	command := offloadtest.Setup(t)
	t.Setenv("HOME", t.TempDir())
	// Allow process startup under full-suite contention.
	return Config{Command: command, Profile: "test-profile", CodexSandbox: "workspace-write"}, Request{Version: 1, RequestID: NewSessionKey(), Workspace: t.TempDir(), Prompt: "task", Instructions: "private context", PermissionMode: "bypassPermissions", TimeoutMS: 15000}
}
func TestDirectOffloadContract(t *testing.T) {
	c, req := fixture(t)
	capture := filepath.Join(t.TempDir(), "calls")
	t.Setenv("OFFLOAD_TEST_CAPTURE", capture)
	var evidence bytes.Buffer
	result, err := Run(context.Background(), c, req, nil, &evidence, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.NativeSessionID != "native-actual" || !strings.Contains(result.Text, "outcome=done") {
		t.Fatalf("result=%+v", result)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var call struct {
		Args                      map[string]string
		Prompt, Instructions, Cwd string
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"-json": "true", "-no-salvage": "true", "-p": "test-profile", "-execution-policy": "managed-development", "-w": "true", "-codex-sandbox": "workspace-write", "-caller-run-id": req.RequestID} {
		if call.Args[k] != want {
			t.Errorf("%s=%q want %q", k, call.Args[k], want)
		}
	}
	if call.Args["-r"] != "" || call.Args["-m"] != "" || call.Prompt != req.Prompt || call.Instructions != req.Instructions {
		t.Fatalf("call=%+v", call)
	}
	if strings.Contains(evidence.String(), "Final answer") || !strings.Contains(evidence.String(), `"type":"final"`) {
		t.Fatalf("evidence=%s", evidence.String())
	}
	for _, key := range []string{"-events-file", "-instructions-file"} {
		if _, err := os.Stat(call.Args[key]); !os.IsNotExist(err) {
			t.Errorf("private input left behind: %v", err)
		}
	}
}
func TestOffloadProtocolFailuresNeverReplay(t *testing.T) {
	for _, mode := range []string{"mismatch", "wrong-profile", "changed-session", "sequence", "missing-final", "duplicate-final", "truncated", "exit-mismatch", "missing-field", "truncated-events", "oversized-events", "oversized-stdout", "duplicate-stdout"} {
		t.Run(mode, func(t *testing.T) {
			c, req := fixture(t)
			req.Resume = true
			req.SessionKey = "native-actual"
			t.Setenv("OFFLOAD_TEST_MODE", mode)
			capture := filepath.Join(t.TempDir(), "calls")
			t.Setenv("OFFLOAD_TEST_CAPTURE", capture)
			_, err := Run(context.Background(), c, req, nil, nil, nil)
			if err == nil {
				t.Fatal("accepted invalid contract")
			}
			calls, _ := os.ReadFile(capture)
			if bytes.Count(calls, []byte("\n")) != 1 {
				t.Fatalf("replayed: %s", calls)
			}
		})
	}
}
func TestOffloadBudgetAndOwnershipFailBeforeLaunch(t *testing.T) {
	c, req := fixture(t)
	capture := filepath.Join(t.TempDir(), "calls")
	t.Setenv("OFFLOAD_TEST_CAPTURE", capture)
	req.MaxBudgetUSD = 1
	_, err := Run(context.Background(), c, req, nil, nil, nil)
	if !IsKind(err, "unsupported_budget") {
		t.Fatal(err)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("budget launched child")
	}
	req.MaxBudgetUSD = 0
	release, err := acquireOwnership(req.Workspace, c.Profile, "", false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = Run(context.Background(), c, req, nil, nil, nil)
	if !IsKind(err, "session_busy") {
		t.Fatal(err)
	}
}
func TestOffloadTimeoutAndCancel(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "cancel"}[cancelled], func(t *testing.T) {
			c, req := fixture(t)
			req.TimeoutMS = 80
			t.Setenv("OFFLOAD_TEST_MODE", "sleep")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				time.AfterFunc(40*time.Millisecond, cancel)
			}
			start := time.Now()
			_, err := Run(ctx, c, req, nil, nil, nil)
			if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if time.Since(start) > 5*time.Second {
				t.Fatal("cancellation unbounded")
			}
			release, err := acquireOwnership(req.Workspace, c.Profile, "", false)
			if err != nil {
				t.Fatal(err)
			}
			release()
		})
	}
}
func TestOffloadChatResumesActualSessionOnce(t *testing.T) {
	_, req := fixture(t)
	capture := filepath.Join(t.TempDir(), "calls")
	t.Setenv("OFFLOAD_TEST_CAPTURE", capture)
	var out bytes.Buffer
	native := ""
	err := Chat(context.Background(), strings.NewReader("first\nsecond\n/exit\n"), &out, Options{Workspace: req.Workspace, OnSession: func(id string) { native = id; t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "changed-profile") }})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "Final answer") != 2 || native != "native-actual" {
		t.Fatalf("out=%s session=%s", out.String(), native)
	}
	raw, _ := os.ReadFile(capture)
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	if len(lines) != 2 {
		t.Fatal(string(raw))
	}
	for i, line := range lines {
		var call struct{ Args map[string]string }
		_ = json.Unmarshal(line, &call)
		if i == 0 && call.Args["-r"] != "" || i == 1 && call.Args["-r"] != "native-actual" {
			t.Fatal(string(line))
		}
		if call.Args["-p"] != "test-profile" {
			t.Fatal("chat changed profile during resume")
		}
		if call.Args["-w"] != "" || call.Args["-execution-policy"] != "" {
			t.Fatal("read-only broadened")
		}
	}
}
func TestOffloadRolesRestrictionsRedactionAndErrors(t *testing.T) {
	_, req := fixture(t)
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "")
	cfg := []byte("settings:\n  execution:\n    command: " + os.Getenv("CLAWFLOW_OFFLOAD_COMMAND") + "\n    profile: base\n    role_profiles:\n      chat: chat-profile\n")
	path := filepath.Join(os.Getenv("HOME"), ".clawflow", "config", "config.yaml")
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	_ = os.WriteFile(path, cfg, 0600)
	_ = os.Unsetenv("CLAWFLOW_OFFLOAD_PROFILE")
	capture := filepath.Join(t.TempDir(), "calls")
	t.Setenv("OFFLOAD_TEST_CAPTURE", capture)
	t.Setenv("TEST_API_KEY", "sensitive-value")
	t.Setenv("OFFLOAD_TEST_TEXT", "answer sensitive-value")
	t.Setenv("OFFLOAD_TEST_ERROR", "unsupported_tools")
	result, err := Execute(context.Background(), "task", Options{Workspace: req.Workspace, Role: "chat", ToolsDisabled: true, DisallowedTools: []string{"Edit", "Write"}})
	if !IsKind(err, "unsupported_tools") || strings.Contains(result.Text, "sensitive-value") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	raw, _ := os.ReadFile(capture)
	var call struct{ Args map[string]string }
	_ = json.Unmarshal(raw, &call)
	if call.Args["-p"] != "chat-profile" || call.Args["-tools-disabled"] != "true" || call.Args["-disallowed-tools"] != "Edit,Write" || call.Args["-m"] != "" {
		t.Fatal(string(raw))
	}
}
func TestOffloadConfigurationRequired(t *testing.T) {
	c, _ := fixture(t)
	c.Profile = ""
	if ValidateConfig(c) == nil {
		t.Fatal("implicit profile")
	}
	c.Profile = "test"
	c.CodexSandbox = "unrestricted"
	if ValidateConfig(c) == nil {
		t.Fatal("invalid sandbox")
	}
}
func TestDuplicateJSONKeysRejected(t *testing.T) {
	if validJSON([]byte(`{"x":1,"x":2}`)) {
		t.Fatal("duplicate accepted")
	}
}

func TestOffloadExplicitModelToolsAndInstructions(t *testing.T) {
	_, req := fixture(t)
	capture := filepath.Join(t.TempDir(), "calls")
	t.Setenv("OFFLOAD_TEST_CAPTURE", capture)
	if err := os.WriteFile(filepath.Join(req.Workspace, "AGENTS.md"), []byte("Project-specific rule"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Execute(context.Background(), "task", Options{Workspace: req.Workspace, Role: "operator", Model: "explicit-model", Instructions: strings.Repeat("instruction ", 1800), AllowedTools: []string{"Read", "Grep"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(capture)
	var call struct {
		Args         map[string]string
		Instructions string
	}
	_ = json.Unmarshal(raw, &call)
	if call.Args["-m"] != "explicit-model" || call.Args["-allowed-tools"] != "Read,Grep" || !strings.Contains(call.Instructions, "Project-specific rule") || len(call.Instructions) < 16000 {
		t.Fatal("explicit execution input lost")
	}
}

func TestPilotExecutionProfile(t *testing.T) {
	for _, tc := range []struct{ name, env, want string }{
		{"mapped", "", "supervisor"},
		{"fallback", "", "base"},
		{"environment", "explicit", "explicit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, req := fixture(t)
			t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "")
			os.Unsetenv("CLAWFLOW_OFFLOAD_PROFILE")
			if tc.env != "" {
				t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", tc.env)
			}
			cfg := &config.Config{Settings: config.Settings{Execution: config.ExecutionSettings{Profile: "base", RoleProfiles: map[string]string{}}}}
			if tc.name == "fallback" {
				cfg.Settings.Execution.RoleProfiles[config.RoleOperator] = "implementer"
			} else {
				cfg.Settings.Execution.RoleProfiles[config.RolePilot] = "supervisor"
			}
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			capture := filepath.Join(t.TempDir(), "calls")
			t.Setenv("OFFLOAD_TEST_CAPTURE", capture)
			_, err := Execute(context.Background(), "task", Options{Workspace: req.Workspace, Role: config.RolePilot})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			var call struct{ Args map[string]string }
			if err := json.Unmarshal(raw, &call); err != nil {
				t.Fatal(err)
			}
			if call.Args["-p"] != tc.want {
				t.Fatalf("args=%v, want %s", call.Args, tc.want)
			}
		})
	}
}

// All operator roles and the direct acceptance runner share Run's launch gate.
func TestOffloadSharedCapacity(t *testing.T) {
	for _, mode := range []string{"implement", "repair-ci", "acceptance"} {
		t.Run(mode, func(t *testing.T) {
			c, req := fixture(t)
			if mode == "acceptance" {
				req.PermissionMode = "default"
			}
			cfg := &config.Config{Settings: config.Settings{MaxConcurrentAgents: 1}}
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			release, err := agentcapacity.Acquire(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			capture := filepath.Join(t.TempDir(), "calls")
			t.Setenv("OFFLOAD_TEST_CAPTURE", capture)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if _, err := Run(ctx, c, req, nil, nil, nil); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("full capacity: %v", err)
			}
			if _, err := os.Stat(capture); !os.IsNotExist(err) {
				t.Fatal("Offload started without a slot")
			}
			release()
			if agentcapacity.Used() != 0 {
				t.Fatal("cancelled waiter leaked a slot")
			}
			t.Setenv("CLAWFLOW_EXPECT_SLOT", "1")
			if _, err := Run(context.Background(), c, req, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			if agentcapacity.Used() != 0 {
				t.Fatal("execution leaked a slot")
			}
		})
	}
}

func TestResultModelAndUsage(t *testing.T) {
	for _, mode := range []string{"result-model", "event-model", "final-model-empty"} {
		t.Run(mode, func(t *testing.T) {
			c, req := fixture(t)
			t.Setenv("OFFLOAD_TEST_MODE", mode)
			r, err := Run(context.Background(), c, req, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "result-model" {
				var usage TokenUsage
				if err := json.Unmarshal(r.Usage, &usage); err != nil {
					t.Fatal(err)
				}
				if r.Model != "gpt-test" || r.Usage == nil || usage.InputTokens == nil || *usage.InputTokens != 1000 || r.CostBasis != "subscription" || r.CostUSD != nil {
					t.Fatalf("%+v", r)
				}
			} else if r.Model != "profile-model" {
				t.Fatalf("%+v", r)
			}
		})
	}
}
