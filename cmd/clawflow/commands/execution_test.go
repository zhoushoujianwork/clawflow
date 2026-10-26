//go:build !windows

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

func executionHelp() string {
	var b strings.Builder
	b.WriteString("Usage of offload:\n")
	for _, f := range execution.RequiredFlags {
		b.WriteString("  -" + f + " string\n    \tdescription\n")
	}
	return b.String()
}

// fakeOffload writes a script that prints help to stderr and exits with code.
func fakeOffload(t *testing.T, help, code string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "help.txt"), []byte(help), 0o600); err != nil {
		t.Fatal(err)
	}
	cmdPath := filepath.Join(dir, "offload")
	script := "#!/bin/sh\ncat '" + filepath.Join(dir, "help.txt") + "' >&2\nexit " + code + "\n"
	if err := os.WriteFile(cmdPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return cmdPath
}

// executionFixture writes a fake offload command and a config under a temp HOME.
func executionFixture(t *testing.T, help, code, settings string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, key := range []string{"CLAWFLOW_OFFLOAD_COMMAND", "CLAWFLOW_OFFLOAD_PROFILE", "CLAWFLOW_OFFLOAD_CODEX_SANDBOX"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
	cmdPath := fakeOffload(t, help, code)
	cfgDir := filepath.Join(home, ".clawflow", "config")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "settings:\n  execution:\n    command: " + cmdPath + "\n" + settings
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return cmdPath
}

func runExecutionCheckCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(append([]string{"execution", "check"}, args...))
	err = root.ExecuteContext(context.Background())
	return out.String(), errBuf.String(), err
}

func TestExecutionCheckReadyJSON(t *testing.T) {
	cmdPath := executionFixture(t, executionHelp(), "2", "    profile: base\n")
	out, errOut, err := runExecutionCheckCLI(t, "--json")
	if err != nil || errOut != "" {
		t.Fatalf("err=%v stderr=%q", err, errOut)
	}
	var r execution.Report
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if r.Status != "ready" || r.Profile != "base" || r.Command != cmdPath || r.SchemaVersion != 1 {
		t.Fatalf("report=%+v", r)
	}
}

func TestExecutionCheckBlockedJSONOnceAndQuiet(t *testing.T) {
	t.Setenv("CLAWFLOW_TEST_SECRET_TOKEN", "super-secret-value")
	executionFixture(t, "Usage of offload:\nflag provided but not defined: -h super-secret-value\n", "2", "    profile: base\n")
	out, errOut, err := runExecutionCheckCLI(t, "--json")
	if err == nil {
		t.Fatal("blocked report must return an error")
	}
	if errOut != "" || strings.Contains(out, "Usage:") {
		t.Fatalf("duplicate output: stderr=%q stdout=%q", errOut, out)
	}
	if strings.Count(strings.TrimSpace(out), "\n") != 0 {
		t.Fatalf("expected exactly one JSON document: %q", out)
	}
	var r execution.Report
	if json.Unmarshal([]byte(out), &r) != nil || r.Status != "blocked" {
		t.Fatalf("out=%q", out)
	}
	if strings.Contains(out, "super-secret-value") || strings.Contains(err.Error(), "super-secret-value") {
		t.Fatal("secret echoed")
	}
}

func TestExecutionCheckMissingProfileBlocked(t *testing.T) {
	executionFixture(t, executionHelp(), "2", "")
	out, _, err := runExecutionCheckCLI(t, "--json")
	var r execution.Report
	if err == nil || json.Unmarshal([]byte(out), &r) != nil || r.Status != "blocked" || r.Checks[0].Status != "blocked" {
		t.Fatalf("err=%v out=%q", err, out)
	}
}

func TestExecutionCheckRoleAndEnvProfile(t *testing.T) {
	executionFixture(t, executionHelp(), "2", "    profile: base\n    role_profiles:\n      implement: role-profile\n")
	out, _, err := runExecutionCheckCLI(t, "--json")
	var r execution.Report
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || r.Profile != "base" {
		t.Fatalf("default profile: err=%v out=%q", err, out)
	}
	out, _, err = runExecutionCheckCLI(t, "--json", "--role", "implement")
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || r.Profile != "role-profile" {
		t.Fatalf("role profile: err=%v out=%q", err, out)
	}
	out, _, err = runExecutionCheckCLI(t, "--json", "--role", "unknown")
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || r.Profile != "base" {
		t.Fatalf("unknown role: err=%v out=%q", err, out)
	}
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "env-profile")
	out, _, err = runExecutionCheckCLI(t, "--json", "--role", "implement")
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || r.Profile != "env-profile" {
		t.Fatalf("env profile must win: err=%v out=%q", err, out)
	}
}

func TestExecutionCheckEnvCommandOverridesConfig(t *testing.T) {
	executionFixture(t, "Usage of offload:\nnot a flag list\n", "2", "    profile: base\n")
	good := fakeOffload(t, executionHelp(), "2")
	t.Setenv("CLAWFLOW_OFFLOAD_COMMAND", good)
	out, _, err := runExecutionCheckCLI(t, "--json")
	var r execution.Report
	if err != nil || json.Unmarshal([]byte(out), &r) != nil || r.Command != good || r.Status != "ready" {
		t.Fatalf("err=%v out=%q", err, out)
	}
}

func TestExecutionCheckTextReport(t *testing.T) {
	executionFixture(t, executionHelp(), "2", "    profile: base\n")
	out, errOut, err := runExecutionCheckCLI(t)
	if err != nil || errOut != "" {
		t.Fatalf("err=%v stderr=%q", err, errOut)
	}
	for _, want := range []string{"ready", "local help probe only", "provider_execution", "permission_enforcement"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
}
