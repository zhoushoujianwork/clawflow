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

type fileState struct {
	data string
	mode os.FileMode
}

func snapshotDir(t *testing.T, root string) map[string]fileState {
	t.Helper()
	state := map[string]fileState{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		fs := fileState{mode: info.Mode()}
		if info.Mode().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			fs.data = string(raw)
		}
		state[path] = fs
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func assertSnapshotEqual(t *testing.T, before, after map[string]fileState) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("file set changed: before=%d after=%d", len(before), len(after))
	}
	for path, b := range before {
		if a, ok := after[path]; !ok || a != b {
			t.Fatalf("%s changed", path)
		}
	}
}

func clearOffloadEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"CLAWFLOW_OFFLOAD_COMMAND", "CLAWFLOW_OFFLOAD_PROFILE", "CLAWFLOW_OFFLOAD_CODEX_SANDBOX"} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func TestExecutionCheckLegacyConfigIsNeverMigrated(t *testing.T) {
	for _, withEnv := range []bool{false, true} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		clearOffloadEnv(t)
		cmdPath := fakeOffload(t, executionHelp(), "2")
		cfgDir := filepath.Join(home, ".clawflow", "config")
		if err := os.MkdirAll(cfgDir, 0o700); err != nil {
			t.Fatal(err)
		}
		legacy := "settings:\n  execution:\n    command: " + cmdPath + "\n    profile: legacy\nrepos: {}\n"
		legacyPath := filepath.Join(cfgDir, "repos.yaml")
		if err := os.WriteFile(legacyPath, []byte(legacy), 0o640); err != nil {
			t.Fatal(err)
		}
		if withEnv {
			t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "env-profile")
			t.Setenv("CLAWFLOW_OFFLOAD_COMMAND", cmdPath)
		}
		before := snapshotDir(t, home)
		out, errOut, err := runExecutionCheckCLI(t, "--json")
		after := snapshotDir(t, home)
		assertSnapshotEqual(t, before, after)
		if _, statErr := os.Stat(filepath.Join(cfgDir, "config.yaml")); !os.IsNotExist(statErr) {
			t.Fatal("canonical config was created")
		}
		if _, statErr := os.Stat(legacyPath + ".bak"); !os.IsNotExist(statErr) {
			t.Fatal("legacy config was renamed")
		}
		var r execution.Report
		if json.Unmarshal([]byte(out), &r) != nil || errOut != "" {
			t.Fatalf("out=%q stderr=%q", out, errOut)
		}
		if withEnv {
			if err != nil || r.Status != "ready" || r.Profile != "env-profile" {
				t.Fatalf("env profile without canonical config must work: err=%v r=%+v", err, r)
			}
			continue
		}
		msg := r.Checks[0].Message
		if err == nil || r.Status != "blocked" || !strings.Contains(msg, "legacy repos.yaml") || !strings.Contains(msg, "config.yaml") {
			t.Fatalf("legacy-only must be actionable: err=%v r=%+v", err, r)
		}
	}
}

func TestExecutionCheckInvalidCanonicalBlocksWithoutMutationOrLeak(t *testing.T) {
	for name, content := range map[string]string{
		"malformed": "settings: [unterminated\n  secret-parser-detail: sk-LEAK\n",
		"empty":     "",
		"scalar":    "just a string\n",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			clearOffloadEnv(t)
			// Env must not bypass an unreadable canonical file.
			t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", "env-profile")
			t.Setenv("CLAWFLOW_OFFLOAD_COMMAND", fakeOffload(t, executionHelp(), "2"))
			cfgDir := filepath.Join(home, ".clawflow", "config")
			if err := os.MkdirAll(cfgDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			before := snapshotDir(t, home)
			out, errOut, err := runExecutionCheckCLI(t, "--json")
			assertSnapshotEqual(t, before, snapshotDir(t, home))
			var r execution.Report
			if err == nil || json.Unmarshal([]byte(out), &r) != nil || r.Status != "blocked" || r.Checks[0].Status != "blocked" {
				t.Fatalf("err=%v out=%q", err, out)
			}
			if r.Command != "" || strings.Contains(out+errOut+err.Error(), "LEAK") || strings.Contains(out, "yaml:") {
				t.Fatalf("parser detail leaked: %q", out)
			}
		})
	}
}

func TestExecutionCheckMissingEverythingIsBlockedWithoutWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	clearOffloadEnv(t)
	before := snapshotDir(t, home)
	out, _, err := runExecutionCheckCLI(t, "--json")
	var r execution.Report
	if err == nil || json.Unmarshal([]byte(out), &r) != nil || r.Status != "blocked" {
		t.Fatalf("err=%v out=%q", err, out)
	}
	assertSnapshotEqual(t, before, snapshotDir(t, home))
}

func TestPilotProfileCheckAndConfigShow(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(map[bool]string{false: "fallback", true: "override"}[override], func(t *testing.T) {
			settings, want := "    profile: base\n", "base"
			if override {
				settings += "    role_profiles:\n      pilot: supervisor\n"
				want = "supervisor"
			}
			executionFixture(t, executionHelp(), "2", settings)
			out, _, err := runExecutionCheckCLI(t, "--role", "pilot")
			if err != nil || !strings.Contains(out, "Profile: "+want) {
				t.Fatalf("%v: %s", err, out)
			}
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			old := os.Stdout
			os.Stdout = w
			cmd := NewConfigCmd()
			cmd.SetArgs([]string{"show"})
			err = cmd.Execute()
			os.Stdout = old
			w.Close()
			var buf bytes.Buffer
			buf.ReadFrom(r)
			r.Close()
			if err != nil || !strings.Contains(buf.String(), "Pilot:        Offload profile="+want) {
				t.Fatalf("%v: %s", err, &buf)
			}
		})
	}
}
