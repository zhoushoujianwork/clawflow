//go:build !windows

package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/repairledger"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func writeWorkerPlist(t *testing.T, dir, name, path, extraEnv, roles string) {
	t.Helper()
	body := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>Label</key><string>com.zhoushoujianwork.clawflow.worker-` + name + `</string>
<key>ProgramArguments</key><array><string>/bin/clawflow</string><string>worker</string><string>--roles</string><string>` + roles + `</string></array>
<key>EnvironmentVariables</key><dict><key>PATH</key><string>` + path + `</string>` + extraEnv + `</dict>
</dict></plist>`
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "com.zhoushoujianwork.clawflow.worker-"+name+".plist"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func statusFixture(t *testing.T) (agents string, in statusInputs, binDir string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"CLAWFLOW_OFFLOAD_COMMAND", "CLAWFLOW_OFFLOAD_PROFILE", "CLAWFLOW_OFFLOAD_CODEX_SANDBOX"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	binDir = t.TempDir()
	fakeOffloadWithProfiles(t, binDir)
	cfgDir := filepath.Join(home, ".clawflow", "config")
	os.MkdirAll(cfgDir, 0o700)
	os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("settings:\n  execution:\n    profile: base\n    role_profiles:\n      acceptance: review\n"), 0o600)
	agents = filepath.Join(home, "Library", "LaunchAgents")
	now := time.Unix(10_000, 0)
	return agents, statusInputs{AgentsDir: agents, Settings: loadExecutionSettings, Cache: t.TempDir(), Now: func() time.Time { return now }}, binDir
}

// fakeOffloadWithProfiles installs an `offload` that answers -h and profiles
// and records any attempt to run a task.
func fakeOffloadWithProfiles(t *testing.T, dir string) {
	t.Helper()
	help := executionHelp() + "  offload profiles [--json]     list profiles\n"
	os.WriteFile(filepath.Join(dir, "help.txt"), []byte(help), 0o600)
	os.WriteFile(filepath.Join(dir, "profiles.json"), []byte(`{"schema_version":1,"config_exists":true,"profiles":[{"name":"base"},{"name":"review"}]}`), 0o600)
	script := "#!/bin/sh\ncase \"$1\" in\n-h) cat '" + filepath.Join(dir, "help.txt") + "' >&2; exit 2;;\nprofiles) if [ \"$2\" = \"--json\" ]; then cat '" + filepath.Join(dir, "profiles.json") + "'; fi; exit 0;;\n*) touch '" + filepath.Join(dir, "task-started") + "'; exit 9;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "offload"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestStatusViewerOnlyNeedsNoExecutor(t *testing.T) {
	_, in, _ := statusFixture(t)
	doc := collectStatus(context.Background(), in)
	if doc.State != execution.StateNotRequired || len(doc.Workers) != 1 || !strings.Contains(doc.Summary, "not required") {
		t.Fatalf("%+v", doc)
	}
}

func TestStatusUsesServiceEnvironmentNotShell(t *testing.T) {
	agents, in, bin := statusFixture(t)
	t.Setenv("PATH", bin+":/usr/bin:/bin") // interactive shell can see offload
	writeWorkerPlist(t, agents, "good", bin+":/usr/bin:/bin", "", "operator")
	writeWorkerPlist(t, agents, "bad", "/usr/bin:/bin", "", "operator,acceptance")
	doc := collectStatus(context.Background(), in)
	if doc.State != execution.StateBlocked || !strings.Contains(doc.Summary, "bad") || strings.Contains(doc.Summary, "good") {
		t.Fatalf("summary=%q", doc.Summary)
	}
	byName := map[string]execution.Readiness{}
	for _, w := range doc.Workers {
		byName[w.Worker] = w
	}
	if byName["good"].State != execution.StateReady || byName["bad"].Reason != execution.ReasonMissing || byName["bad"].Environment != "service" {
		t.Fatalf("%+v", byName)
	}
	if byName["bad"].Guidance == nil || byName["bad"].Guidance.Recheck != execution.RecheckCommand {
		t.Fatalf("no actionable guidance: %+v", byName["bad"])
	}
	// Explicit service override of the command wins over a bad PATH.
	writeWorkerPlist(t, agents, "bad", "/usr/bin:/bin", `<key>CLAWFLOW_OFFLOAD_COMMAND</key><string>`+filepath.Join(bin, "offload")+`</string>`, "operator")
	in.Recheck = true
	doc = collectStatus(context.Background(), in)
	if doc.State != execution.StateReady {
		t.Fatalf("override not honored: %+v", doc.Summary)
	}
}

func TestStatusRoleProfilesPerWorker(t *testing.T) {
	agents, in, bin := statusFixture(t)
	os.WriteFile(filepath.Join(bin, "profiles.json"), []byte(`{"schema_version":1,"config_exists":true,"profiles":[{"name":"base"}]}`), 0o600)
	writeWorkerPlist(t, agents, "dev", bin+":/usr/bin:/bin", "", "operator")
	writeWorkerPlist(t, agents, "rev", bin+":/usr/bin:/bin", "", "operator,acceptance")
	doc := collectStatus(context.Background(), in)
	got := map[string]string{}
	for _, w := range doc.Workers {
		got[w.Worker] = w.State + ":" + w.Reason
	}
	if got["dev"] != "ready:" || got["rev"] != "blocked:profile_not_found" {
		t.Fatalf("%v", got)
	}
}

func TestStatusCachedAndRecoversAfterInstall(t *testing.T) {
	agents, in, bin := statusFixture(t)
	empty := t.TempDir()
	writeWorkerPlist(t, agents, "w", empty+":/usr/bin:/bin", "", "operator")
	if doc := collectStatus(context.Background(), in); doc.State != execution.StateBlocked {
		t.Fatalf("%+v", doc)
	}
	// "Install" Offload by placing it on the service PATH.
	writeWorkerPlist(t, agents, "w", bin+":/usr/bin:/bin", "", "operator")
	if doc := collectStatus(context.Background(), in); doc.State != execution.StateBlocked {
		t.Fatal("poll inside TTL must reuse the cached result and spawn no probe")
	}
	if _, err := os.Stat(filepath.Join(bin, "task-started")); err == nil {
		t.Fatal("task started")
	}
	in.Recheck = true
	if doc := collectStatus(context.Background(), in); doc.State != execution.StateReady {
		t.Fatalf("recheck did not clear stale warning: %+v", doc)
	}
	in.Recheck = false
	if doc := collectStatus(context.Background(), in); doc.State != execution.StateReady {
		t.Fatal("recovered state not cached")
	}
}

func TestStatusUnreadablePlistIsUnreportedNotReady(t *testing.T) {
	agents, in, _ := statusFixture(t)
	os.MkdirAll(agents, 0o755)
	os.WriteFile(filepath.Join(agents, "com.zhoushoujianwork.clawflow.worker-odd.plist"), []byte("not xml"), 0o644)
	doc := collectStatus(context.Background(), in)
	if doc.State != execution.StateUnreported {
		t.Fatalf("%+v", doc)
	}
}

func TestStatusCLIOutputAndExit(t *testing.T) {
	agents, _, _ := statusFixture(t)
	writeWorkerPlist(t, agents, "w", t.TempDir()+":/usr/bin:/bin", "", "operator")
	root := NewRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"execution", "status", "--json", "--recheck"})
	err := root.ExecuteContext(context.Background())
	if err != errExecutionBlocked {
		t.Fatalf("err=%v", err)
	}
	var doc execution.StatusDoc
	if json.Unmarshal(out.Bytes(), &doc) != nil || doc.State != execution.StateBlocked || doc.SchemaVersion != 1 {
		t.Fatalf("%s", out.String())
	}
	text := &bytes.Buffer{}
	printStatus(text, doc)
	for _, want := range []string{"Configured command: offload", "Recheck: clawflow execution status --recheck", execution.OffloadInstallCmd, execution.OffloadInstallDocs} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("missing %q in\n%s", want, text.String())
		}
	}
}

func TestStatusLaunchdDefaultPathWhenUnset(t *testing.T) {
	agents, in, bin := statusFixture(t)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	body := `<plist version="1.0"><dict><key>Label</key><string>com.zhoushoujianwork.clawflow.worker-nopath</string>
<key>ProgramArguments</key><array><string>/bin/clawflow</string><string>worker</string></array></dict></plist>`
	os.MkdirAll(agents, 0o755)
	os.WriteFile(filepath.Join(agents, "com.zhoushoujianwork.clawflow.worker-nopath.plist"), []byte(body), 0o644)
	doc := collectStatus(context.Background(), in)
	if doc.State != execution.StateBlocked {
		t.Fatalf("a service with no PATH must not inherit the shell PATH: %+v", doc.Summary)
	}
}

// --- gating ---

func TestWorkerGateBlocksClaimsOncePerCondition(t *testing.T) {
	_, _, bin := statusFixture(t)
	var runs atomic.Int32
	state := execution.StateBlocked
	g := &workerGate{name: "w", cache: newReadinessCache(func(context.Context) execution.Readiness {
		runs.Add(1)
		return execution.Readiness{State: state, Reason: execution.ReasonMissing, Summary: "missing"}
	})}
	_ = bin
	for i := 0; i < 50; i++ {
		if g.ready(context.Background()) {
			t.Fatal("blocked gate allowed a claim")
		}
	}
	if runs.Load() != 1 {
		t.Fatalf("probe per poll: %d", runs.Load())
	}
	state = execution.StateReady
	g.cache.Get(context.Background(), true)
	if !g.ready(context.Background()) {
		t.Fatal("recovered gate still blocked")
	}
}

func TestLocalRunBlockedByOffloadConsumesNoRepairAttemptOrLabel(t *testing.T) {
	withRunLog(t)
	old := offloadReadyFn
	defer func() { offloadReadyFn = old }()
	offloadReadyFn = func(context.Context, string) execution.Readiness {
		return execution.Readiness{State: execution.StateBlocked, Reason: execution.ReasonMissing, Summary: "Offload executable not found"}
	}
	fake := &labelRecorder{autonomousRecheckFake: autonomousRecheckFake{}}
	sub := &operator.Subject{Number: 42, State: "open"}
	op := effectiveOperator(implementationForTest(t), sub)
	j := &runJob{op: op, sub: sub, repo: "o/r", client: fake}
	fired, limited, failed := runOneOperator(context.Background(), j, time.Second)
	if fired || limited || failed {
		t.Fatalf("blocked run must not count as failure: %v %v %v", fired, limited, failed)
	}
	if len(fake.added) != 0 || fake.comments != 0 {
		t.Fatalf("labels=%v comments=%d", fake.added, fake.comments)
	}
	if fake.structuralChecks != 0 {
		t.Fatal("work began despite blocked Offload")
	}
	if j.result.Status != "blocked" || j.result.NextAction == "" {
		t.Fatalf("%+v", j.result)
	}
}

func TestBlockedOffloadReleaseKindDoesNotConsumeRepairBudget(t *testing.T) {
	root := t.TempDir()
	pr := vcs.PR{Number: 7, HeadSHA: "abc"}
	now := time.Unix(1_000_000, 0)
	for i := 0; i < 5; i++ {
		attempt, err := repairledger.Reserve(root, "o/r", pr, "ci", now)
		if err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
		if err := releaseCIRepair(root, "o/r", pr, attempt, repairledger.ReleaseOffloadUnavailable, now); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Hour)
	}
	_, path := repairledger.Path(root, "o/r", 7)
	ledger, err := repairledger.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if consumed, infra := ledger.Counts(); consumed != 0 || infra != 0 {
		t.Fatalf("consumed=%d infra=%d", consumed, infra)
	}
}

type labelRecorder struct {
	autonomousRecheckFake
	added    []string
	comments int
}

func (f *labelRecorder) AddLabel(_ string, _ int, l ...string) error {
	f.added = append(f.added, l...)
	return nil
}
func (f *labelRecorder) PostIssueComment(string, int, string) error {
	f.comments++
	return nil
}

func TestServiceInstallWarnsButNeverInstalls(t *testing.T) {
	_, _, bin := statusFixture(t)
	var out bytes.Buffer
	warnOffloadForService(&out, "/usr/bin:/bin")
	text := out.String()
	if !strings.Contains(text, "not ready") || !strings.Contains(text, execution.OffloadInstallCmd) || !strings.Contains(text, execution.RecheckCommand) {
		t.Fatalf("%q", text)
	}
	out.Reset()
	warnOffloadForService(&out, bin+":/usr/bin:/bin")
	if out.Len() != 0 {
		t.Fatalf("healthy PATH warned: %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(bin, "task-started")); err == nil {
		t.Fatal("detection started a task")
	}
}
