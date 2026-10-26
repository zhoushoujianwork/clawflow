//go:build !windows

package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// helpFor renders Go flag.PrintDefaults style help declaring the given flags.
func helpFor(flags []string, extra string) string {
	var b strings.Builder
	b.WriteString("Usage of offload:\n")
	for _, f := range flags {
		b.WriteString("  -" + f + " string\n    \tdescription of " + f + "\n")
	}
	b.WriteString(extra)
	return b.String()
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

// helpScript prints help to stderr (like Go's flag package) and exits with code.
func helpScript(t *testing.T, help string, code int) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "help.txt"), []byte(help), 0600); err != nil {
		t.Fatal(err)
	}
	return writeScript(t, dir, "offload", "cat '"+filepath.Join(dir, "help.txt")+"' >&2\nexit "+strconv.Itoa(code)+"\n")
}

func shortProbe(t *testing.T, timeout time.Duration) {
	t.Helper()
	ot, og, ow := probeTimeout, probeGrace, probeWaitDelay
	probeTimeout, probeGrace, probeWaitDelay = timeout, 100*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { probeTimeout, probeGrace, probeWaitDelay = ot, og, ow })
}

func check(r Report, id string) Check {
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	return Check{}
}

func TestProbeReadyOnExit2Help(t *testing.T) {
	cmd := helpScript(t, helpFor(RequiredFlags, ""), 2)
	r := ProbeCapabilities(context.Background(), Config{Command: cmd, Profile: "p1"})
	if r.Status != StatusReady || r.SchemaVersion != 1 || r.Command != cmd || r.Profile != "p1" {
		t.Fatalf("report=%+v", r)
	}
	if strings.Join(r.Unverified, ",") != "provider_execution,permission_enforcement" {
		t.Fatalf("unverified=%v", r.Unverified)
	}
	for _, c := range r.Checks {
		if c.Status != CheckPassed {
			t.Fatalf("check %+v", c)
		}
	}
	if _, err := json.Marshal(r); err != nil {
		t.Fatal(err)
	}
}

func TestProbeReadyOnExit0AndSingleLineDeclarations(t *testing.T) {
	var b strings.Builder
	b.WriteString("Usage: offload [flags]\n")
	for _, f := range RequiredFlags {
		b.WriteString("  --" + f + "\tdescription\n")
	}
	cmd := helpScript(t, b.String(), 0)
	if r := ProbeCapabilities(context.Background(), Config{Command: cmd, Profile: "p"}); r.Status != StatusReady {
		t.Fatalf("report=%+v", r)
	}
}

func TestProbeUsesConfiguredPathNotPATH(t *testing.T) {
	good := helpScript(t, helpFor(RequiredFlags, ""), 2)
	pathDir := t.TempDir()
	writeScript(t, pathDir, "offload", "echo wrong >&2\nexit 1\n")
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := ProbeCapabilities(context.Background(), Config{Command: good, Profile: "p"})
	if r.Status != StatusReady || r.Command != good {
		t.Fatalf("report=%+v", r)
	}
	// A bare command name resolves through PATH to an absolute path.
	r = ProbeCapabilities(context.Background(), Config{Command: "offload", Profile: "p"})
	if r.Command != filepath.Join(pathDir, "offload") || r.Status != StatusBlocked {
		t.Fatalf("report=%+v", r)
	}
}

func TestProbeRejectsUnsupportedFlagOutput(t *testing.T) {
	help := "flag provided but not defined: -h\n" + helpFor(RequiredFlags, "")
	r := ProbeCapabilities(context.Background(), Config{Command: helpScript(t, help, 2), Profile: "p"})
	if r.Status != StatusBlocked || check(r, CheckHelpProbe).Status != CheckBlocked {
		t.Fatalf("report=%+v", r)
	}
	if check(r, CheckFlagDeclarations).Status != CheckSkipped {
		t.Fatalf("flag check should be skipped: %+v", r.Checks)
	}
}

func TestProbeRejectsMissingDeclarationsAndProseMentions(t *testing.T) {
	var flags []string
	for _, f := range RequiredFlags {
		if f != "caller-run-id" && f != "m" {
			flags = append(flags, f)
		}
	}
	prose := "Use -caller-run-id and -m to pass identity, see --caller-run-id.\n  note: -m is described in docs\n"
	r := ProbeCapabilities(context.Background(), Config{Command: helpScript(t, helpFor(flags, prose), 2), Profile: "p"})
	msg := check(r, CheckFlagDeclarations).Message
	if r.Status != StatusBlocked || !strings.Contains(msg, "-caller-run-id") || !strings.Contains(msg, "-m") {
		t.Fatalf("report=%+v", r)
	}
}

func TestProbeRejectsFlagWithoutDescription(t *testing.T) {
	help := "Usage of offload:\n  -json\n"
	r := ProbeCapabilities(context.Background(), Config{Command: helpScript(t, help, 2), Profile: "p"})
	if r.Status != StatusBlocked {
		t.Fatalf("report=%+v", r)
	}
}

func TestProbeRejectsNonUsageAndBadExit(t *testing.T) {
	for name, tc := range map[string]struct {
		help string
		code int
	}{
		"exit1":     {helpFor(RequiredFlags, ""), 1},
		"exit3":     {helpFor(RequiredFlags, ""), 3},
		"no-usage":  {"hello\n", 2},
		"empty":     {"", 0},
		"exit127":   {helpFor(RequiredFlags, ""), 127},
		"error-top": {"Usage of offload:\nError: boom\n" + helpFor(RequiredFlags, ""), 0},
	} {
		t.Run(name, func(t *testing.T) {
			r := ProbeCapabilities(context.Background(), Config{Command: helpScript(t, tc.help, tc.code), Profile: "p"})
			if r.Status != StatusBlocked || check(r, CheckHelpProbe).Status != CheckBlocked {
				t.Fatalf("report=%+v", r)
			}
		})
	}
}

func TestProbeConfigAndCommandFailures(t *testing.T) {
	r := ProbeCapabilities(context.Background(), Config{Command: filepath.Join(t.TempDir(), "missing"), Profile: "p"})
	if r.Status != StatusBlocked || check(r, CheckConfig).Status != CheckBlocked || r.Command != "" {
		t.Fatalf("report=%+v", r)
	}
	cmd := helpScript(t, helpFor(RequiredFlags, ""), 2)
	r = ProbeCapabilities(context.Background(), Config{Command: cmd})
	if r.Status != StatusBlocked || check(r, CheckConfig).Status != CheckBlocked || check(r, CheckHelpProbe).Status != CheckSkipped {
		t.Fatalf("report=%+v", r)
	}
}

func TestProbeOutputCapKillsProducer(t *testing.T) {
	shortProbe(t, 5*time.Second)
	dir := t.TempDir()
	cmd := writeScript(t, dir, "offload", "echo Usage >&2\nwhile true; do echo 'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx' >&2; done\n")
	start := time.Now()
	r := ProbeCapabilities(context.Background(), Config{Command: cmd, Profile: "p"})
	if r.Status != StatusBlocked || !strings.Contains(check(r, CheckHelpProbe).Message, "128 KiB") {
		t.Fatalf("report=%+v", r)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatalf("output cap took %s", time.Since(start))
	}
}

func waitDead(pid int) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("descendant pid not written")
	return 0
}

func TestProbeTimeoutKillsDescendants(t *testing.T) {
	shortProbe(t, 1500*time.Millisecond)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	// Descendant inherits the output pipes and outlives a naive leader kill.
	cmd := writeScript(t, dir, "offload", "echo Usage >&2\nsleep 60 &\necho $! > '"+pidFile+"'\nwait\n")
	r := ProbeCapabilities(context.Background(), Config{Command: cmd, Profile: "p"})
	if r.Status != StatusBlocked || !strings.Contains(check(r, CheckHelpProbe).Message, "timed out") {
		t.Fatalf("report=%+v", r)
	}
	pid := readPID(t, pidFile)
	if pid > 0 && !waitDead(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("descendant survived timeout")
	}
}

func TestProbeCancelKillsDescendants(t *testing.T) {
	shortProbe(t, 5*time.Second)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	cmd := writeScript(t, dir, "offload", "echo Usage >&2\nsleep 60 &\necho $! > '"+pidFile+"'\nwait\n")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { readPID(t, pidFile); cancel() }()
	r := ProbeCapabilities(ctx, Config{Command: cmd, Profile: "p"})
	if r.Status != StatusBlocked || !strings.Contains(check(r, CheckHelpProbe).Message, "canceled") {
		t.Fatalf("report=%+v", r)
	}
	pid := readPID(t, pidFile)
	if pid > 0 && !waitDead(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("descendant survived cancel")
	}
}

func TestProbeCanceledBeforeStartDoesNotRun(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	cmd := writeScript(t, dir, "offload", "touch '"+marker+"'\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := ProbeCapabilities(ctx, Config{Command: cmd, Profile: "p"})
	if r.Status != StatusBlocked {
		t.Fatalf("report=%+v", r)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("command ran despite canceled context")
	}
}

func TestProbeKillsDescendantHoldingPipeAfterLeaderExit(t *testing.T) {
	shortProbe(t, 3*time.Second)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	help := filepath.Join(dir, "help.txt")
	if err := os.WriteFile(help, []byte(helpFor(RequiredFlags, "")), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := writeScript(t, dir, "offload", "cat '"+help+"' >&2\nsleep 60 &\necho $! > '"+pidFile+"'\nexit 2\n")
	r := ProbeCapabilities(context.Background(), Config{Command: cmd, Profile: "p"})
	if r.Status != StatusReady {
		t.Fatalf("report=%+v", r)
	}
	if pid := readPID(t, pidFile); pid > 0 && !waitDead(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("descendant survived")
	}
}

func TestProbeArgsStdinCwdAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "capture")
	help := filepath.Join(dir, "help.txt")
	if err := os.WriteFile(help, []byte(helpFor(RequiredFlags, "")), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := writeScript(t, dir, "offload", "{ echo \"args=$*\"; echo \"stdin=$(cat | wc -c | tr -d ' ')\"; echo \"secret=$CLAWFLOW_TEST_SECRET_TOKEN\"; echo \"entries=$(ls -A | wc -l | tr -d ' ')\"; pwd -P; } > '"+capture+"'\ncat '"+help+"' >&2\nexit 2\n")
	t.Setenv("CLAWFLOW_TEST_SECRET_TOKEN", "super-secret-value")
	r := ProbeCapabilities(context.Background(), Config{Command: cmd, Profile: "p"})
	if r.Status != StatusReady {
		t.Fatalf("report=%+v", r)
	}
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "args=-h\n") || !strings.Contains(got, "stdin=0\n") || !strings.Contains(got, "secret=\n") || !strings.Contains(got, "entries=0\n") {
		t.Fatalf("capture=%q", got)
	}
	resolvedDir, _ := filepath.EvalSymlinks(dir)
	if strings.Contains(got, resolvedDir+"\n") {
		t.Fatalf("probe ran in the script directory: %q", got)
	}
}

func TestProbeReportDoesNotEchoRawOutput(t *testing.T) {
	help := "Usage of offload:\nflag provided but not defined: sk-LEAKED-SECRET\n"
	r := ProbeCapabilities(context.Background(), Config{Command: helpScript(t, help, 2), Profile: "p"})
	raw, _ := json.Marshal(r)
	if strings.Contains(string(raw), "LEAKED") {
		t.Fatalf("raw output leaked: %s", raw)
	}
}

func TestSanitizeTextBoundsAndStripsControl(t *testing.T) {
	got := sanitizeText("a\x1b[31m\nb"+strings.Repeat("x", 500), 50)
	if len([]rune(got)) > 50 || strings.ContainsAny(got, "\x1b\n") {
		t.Fatalf("got %q", got)
	}
}
