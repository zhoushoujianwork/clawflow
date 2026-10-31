// Package offloadtest provides an isolated fake CLI, never a live provider.
package offloadtest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func Args() map[string]string {
	args := map[string]string{}
	start := 0
	for i, a := range os.Args {
		if a == "--" {
			start = i + 1
			break
		}
	}
	for i := start; i < len(os.Args); i++ {
		a := os.Args[i]
		if a == "-json" || a == "-no-salvage" || a == "-w" || a == "-tools-disabled" {
			args[a] = "true"
		} else if i+1 < len(os.Args) {
			i++
			args[a] = os.Args[i]
		}
	}
	return args
}

func Setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(dir, "offload")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(exe, "'", "'\\''") + "' -test.run=^TestOffloadHelper$ -- \"$@\"\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAWFLOW_OFFLOAD_COMMAND", command)
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "test-profile")
	t.Setenv("CLAWFLOW_OFFLOAD_TEST_HELPER", "1")
	return command
}

func Emit(args map[string]string, text, kind, mode string) {
	status, code := "ok", 0
	if kind != "" {
		status = "error"
		code = 1
	}
	if kind == "timeout" {
		status = "timeout"
		code = 124
	}
	session := "native-actual"
	if args["-r"] != "" {
		session = args["-r"]
	}
	if mode == "changed-session" {
		session = "changed"
	}
	caller := args["-caller-run-id"]
	if mode == "mismatch" {
		caller = "wrong"
	}
	result := map[string]any{"run_id": "run-actual", "caller_run_id": caller, "session": session, "profile": args["-p"], "backend": "claude", "model": "profile-model", "status": status, "exit_code": code, "result": text, "error_kind": kind, "error": "", "usage": nil, "execution_policy": args["-execution-policy"]}
	if mode == "missing-field" {
		delete(result, "exit_code")
	}
	if mode == "wrong-profile" {
		result["profile"] = "wrong"
	}
	if args["-m"] != "" {
		result["model"] = args["-m"]
	}
	f, err := os.OpenFile(args["-events-file"], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	event := func(seq int, typ string) {
		e := map[string]any{"schema_version": 1, "type": typ, "run_id": "run-actual", "caller_run_id": caller, "seq": seq, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "session": session, "backend": "claude", "model": result["model"]}
		if typ == "progress" && os.Getenv("OFFLOAD_TEST_TOOL_CATEGORY") != "" {
			e["tool_category"] = os.Getenv("OFFLOAD_TEST_TOOL_CATEGORY")
		}
		if typ == "final" {
			if mode == "final-model-empty" || mode == "result-model-final-empty" {
				e["model"] = ""
			}
			e["status"] = status
			e["exit_code"] = code
			e["error_kind"] = kind
		}
		if mode == "sequence" {
			e["seq"] = 7
		}
		_ = json.NewEncoder(f).Encode(e)
	}
	if mode == "result-model" {
		result["model"] = ""
	}
	event(1, "start")
	event(2, "progress")
	if mode != "missing-final" {
		event(3, "final")
	}
	if mode == "duplicate-final" {
		event(4, "final")
	}
	_ = f.Close()
	if mode == "oversized-events" {
		_ = os.Truncate(args["-events-file"], (32<<20)+1)
	}
	if mode == "truncated-events" {
		st, _ := os.Stat(args["-events-file"])
		_ = os.Truncate(args["-events-file"], st.Size()-1)
	}
	if mode == "oversized-stdout" {
		for i := 0; i < 34; i++ {
			fmt.Print(strings.Repeat("x", 1<<20))
		}
		os.Exit(0)
	}
	if mode == "truncated" {
		fmt.Print(`{"status":`)
		os.Exit(code)
	}
	if mode == "result-model" || mode == "result-model-different" || mode == "result-model-final-empty" {
		result["model"] = "gpt-test"
		result["cost_basis"] = "subscription"
		result["cost_usd"] = nil
		result["usage"] = map[string]int64{"input_tokens": 1000, "cached_input_tokens": 800, "cache_write_input_tokens": 0, "output_tokens": 50}
	}
	if mode == "event-model" {
		result["model"] = ""
	}
	_ = json.NewEncoder(os.Stdout).Encode(result)
	if mode == "duplicate-stdout" {
		_ = json.NewEncoder(os.Stdout).Encode(result)
	}
	if mode == "exit-mismatch" {
		os.Exit(7)
	}
	os.Exit(code)
}

func Helper(t *testing.T) {
	t.Helper()
	if os.Getenv("CLAWFLOW_OFFLOAD_TEST_HELPER") != "1" {
		return
	}
	args := Args()
	prompt, _ := io.ReadAll(os.Stdin)
	if args["-instructions-file"] == "" {
		os.Exit(2)
	}
	instructions, err := os.ReadFile(args["-instructions-file"])
	if err != nil {
		os.Exit(2)
	}
	st, _ := os.Stat(args["-instructions-file"])
	if st.Mode().Perm() != 0600 {
		os.Exit(2)
	}
	if _, err := os.Stat(args["-events-file"]); !os.IsNotExist(err) {
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
	mode := os.Getenv("OFFLOAD_TEST_MODE")
	if mode == "descendant" {
		child := exec.Command("sh", "-c", "trap '' TERM; sleep 60")
		child.Stdout = os.Stdout
		child.Stderr = os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(os.Getenv("OFFLOAD_TEST_CHILD_PID"), []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
			os.Exit(2)
		}
	}
	if mode == "sleep" {
		time.Sleep(time.Minute)
	}
	text := os.Getenv("OFFLOAD_TEST_TEXT")
	if text == "" {
		text = "Final answer\n<!-- clawflow:outcome=done -->"
	}
	Emit(args, text, os.Getenv("OFFLOAD_TEST_ERROR"), mode)
}
