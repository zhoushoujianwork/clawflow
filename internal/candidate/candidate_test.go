package candidate

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/zhoushoujianwork/clawflow/internal/devmode"
)

const commitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const commitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func testStable(t *testing.T) Stable {
	t.Helper()
	root := filepath.Join(t.TempDir(), "stable")
	for _, d := range []string{"config", "data"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "config", "config.yaml"), []byte("repos: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Stable{Root: root, Ports: []int{8090, 8093, 5432}, LabelPrefix: "com.zhoushoujianwork.clawflow."}
}

func plan(t *testing.T, commit string, port int) Plan {
	t.Helper()
	p, err := NewPlan(commit, filepath.Join(t.TempDir(), "cands"), port, Pin{SOPRequired: true, SOPDigest: SOPDigest("sop"), Capabilities: []string{"fixture-runtime", "build"}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlanIsReproducibleAndNamespaced(t *testing.T) {
	a, _ := NewPlan(commitA, "/tmp/c", 0, Pin{})
	b, _ := NewPlan(commitA, "/tmp/c", 0, Pin{})
	c, _ := NewPlan(commitB, "/tmp/c", 0, Pin{})
	if a.ID != b.ID || a.Port != b.Port || a.Schema != b.Schema {
		t.Fatal("same commit must give the same identity")
	}
	if a.ID == c.ID || a.Schema == c.Schema || a.BundleID == c.BundleID {
		t.Fatal("different commits must not share an identity")
	}
	if a.BundleID == StableBundleID || !strings.HasPrefix(a.BundleID, BundlePrefix) {
		t.Fatalf("bundle id %q", a.BundleID)
	}
	for kind, l := range a.Labels {
		if !strings.HasPrefix(l, devmode.LabelPrefix(a.ID)) {
			t.Fatalf("%s label %q", kind, l)
		}
	}
	for _, dir := range []string{a.Home, a.State, a.ReviewDir, a.Config, a.Locks, a.Worktrees, a.Cache, a.Logs, a.Bin, a.AppPath} {
		if !devmode.Within(a.Root, dir) {
			t.Fatalf("%s outside root", dir)
		}
	}
	if _, err := NewPlan("main", "/tmp/c", 0, Pin{}); err == nil {
		t.Fatal("a branch name is not a pinned commit")
	}
	if _, err := NewPlan(strings.ToUpper(commitA), "/tmp/c", 0, Pin{}); err == nil {
		t.Fatal("an uppercase object name is not canonical")
	}
}

func TestValidateRefusesCollisionsBeforeMutation(t *testing.T) {
	stable := testStable(t)
	free := func(int) bool { return true }
	p := plan(t, commitA, 0)
	if err := p.Validate(stable, free); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(Plan) Plan{
		"stable port":  func(p Plan) Plan { p.Port = 8093; return p },
		"low port":     func(p Plan) Plan { p.Port = 80; return p },
		"schema":       func(p Plan) Plan { p.Schema = "public"; return p },
		"bundle":       func(p Plan) Plan { p.BundleID = StableBundleID; return p },
		"label":        func(p Plan) Plan { p.Labels["controller"] = "com.zhoushoujianwork.clawflow.controller"; return p },
		"state escape": func(p Plan) Plan { p.State = stable.Root; return p },
		"identity":     func(p Plan) Plan { p.ID = "000000000000"; return p },
		"stable root":  func(p Plan) Plan { p.Root = filepath.Join(stable.Root, "x"); return p },
	}
	for name, mutate := range cases {
		q := plan(t, commitA, 0)
		q.Base = p.Base
		if err := mutate(q).Validate(stable, free); !errors.Is(err, ErrCollision) {
			t.Errorf("%s: want collision, got %v", name, err)
		}
	}
	if err := p.Validate(stable, func(int) bool { return false }); !errors.Is(err, ErrCollision) {
		t.Fatalf("busy port must be refused: %v", err)
	}
	if _, err := os.Stat(p.Root); !os.IsNotExist(err) {
		t.Fatal("validation created the candidate root")
	}
}

func TestPrepareRefusesForeignDirectoryAndSiblingClaims(t *testing.T) {
	stable := testStable(t)
	free := func(int) bool { return true }
	p := plan(t, commitA, 0)
	if err := os.MkdirAll(p.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Root, "user-file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(p, stable, free); !errors.Is(err, ErrCollision) {
		t.Fatalf("non-candidate directory accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.Root, manifestName)); err == nil {
		t.Fatal("manifest written into a foreign directory")
	}
	// Sibling port claim.
	base := t.TempDir()
	first, _ := NewPlan(commitA, base, 31000, Pin{})
	if _, err := Prepare(first, stable, free); err != nil {
		t.Fatal(err)
	}
	second, _ := NewPlan(commitB, base, 31000, Pin{})
	if _, err := Prepare(second, stable, free); !errors.Is(err, ErrCollision) {
		t.Fatalf("shared port accepted: %v", err)
	}
	if _, err := os.Stat(second.Root); !os.IsNotExist(err) {
		t.Fatal("second candidate partially created")
	}
}

func TestPrepareCreatesPrivateLayoutAndIsIdempotent(t *testing.T) {
	stable := testStable(t)
	p := plan(t, commitA, 0)
	m, err := Prepare(p, stable, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{p.Home, p.State, p.ReviewDir, p.Config, p.Locks, p.Worktrees, p.Cache, p.Logs, p.Bin} {
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", dir, err, info)
		}
	}
	again, err := Prepare(p, stable, func(int) bool { return false })
	if err != nil || again.Token != m.Token {
		t.Fatalf("re-prepare must keep identity and ignore its own port: %v", err)
	}
}

func TestConfigIsInertByDefaultAndCarriesNoSecrets(t *testing.T) {
	p := plan(t, commitA, 0)
	cfg, err := p.BuildConfig(ConfigOptions{Fixtures: map[string]string{"fix/repo": "http://127.0.0.1:9000"}})
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.Repos["fix/repo"]
	if r.Enabled || r.AutoMerge || r.SelfUpgrade || cfg.Settings.RunIntervalMinutes != 0 {
		t.Fatalf("candidate config must not dispatch or merge by default: %+v", r)
	}
	if !r.AcceptanceRequired || r.AcceptanceWorker != "different" || r.MaxInFlight != 1 || cfg.Settings.MaxConcurrentAgents != 2 {
		t.Fatalf("lane bounds missing: %+v", r)
	}
	if r.TestingSOPDigest != p.Pin.SOPDigest || strings.Join(r.RequiredCapabilities, ",") != "build,fixture-runtime" {
		t.Fatalf("pin not applied: %+v", r)
	}
	if _, err := p.BuildConfig(ConfigOptions{Fixtures: map[string]string{"o/r": "https://gitlab.com"}}); err == nil {
		t.Fatal("a real forge was accepted as a fixture")
	}
	if _, err := p.BuildConfig(ConfigOptions{Fixtures: map[string]string{"o/r": ""}}); err == nil {
		t.Fatal("an empty base_url (public GitHub) was accepted")
	}
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(p.Config, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteConfig(ConfigOptions{Fixtures: map[string]string{"fix/repo": "http://127.0.0.1:9000"}}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(p.Config, "config.yaml"))
	var back map[string]any
	if err := yaml.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"token", "dsn", "password", "private_key"} {
		if strings.Contains(strings.ToLower(string(data)), banned+":") && strings.Contains(string(data), banned+": \"") {
			t.Fatalf("config carries %s", banned)
		}
	}
	if _, err := os.Stat(filepath.Join(p.Config, "credentials.yaml")); err == nil {
		t.Fatal("credentials were created for the candidate")
	}
}

func TestEnvDropsAmbientSecretsAndStableState(t *testing.T) {
	for k, v := range map[string]string{"GH_TOKEN": "ghp_secret", "GITLAB_TOKEN": "glpat", "CLAWFLOW_DATABASE_DSN": "postgres://u:pw@h/db", "SSH_AUTH_SOCK": "/tmp/agent", "CLAWFLOW_STATE_DIR": "/stable/state", "HOME": "/Users/stable"} {
		t.Setenv(k, v)
	}
	p := plan(t, commitA, 0)
	env := strings.Join(p.Env(""), "\n")
	for _, bad := range []string{"ghp_secret", "glpat", "pw@h", "/tmp/agent", "/stable/state", "/Users/stable"} {
		if strings.Contains(env, bad) {
			t.Fatalf("environment leaks %q", bad)
		}
	}
	for _, want := range []string{"HOME=" + p.Home, "CLAWFLOW_STATE_DIR=" + p.State, devmode.EnvID + "=" + p.ID, devmode.EnvSchema + "=" + p.Schema} {
		if !strings.Contains(env, want) {
			t.Fatalf("missing %s", want)
		}
	}
}

func TestPinDSN(t *testing.T) {
	p := plan(t, commitA, 0)
	got, err := PinDSN("postgres://u:pw@h:5432/db?sslmode=disable", p.Schema)
	if err != nil || devmode.CheckDSN(got) == nil && false {
		t.Fatal(err)
	}
	t.Setenv(devmode.EnvSchema, p.Schema)
	t.Setenv(devmode.EnvID, p.ID)
	if err := devmode.CheckDSN(got); err != nil {
		t.Fatalf("pinned DSN rejected: %v", err)
	}
	kv, _ := PinDSN("host=h dbname=d", p.Schema)
	if err := devmode.CheckDSN(kv); err != nil {
		t.Fatal(err)
	}
	if _, err := PinDSN("postgres://u@h/db", "public"); !errors.Is(err, ErrCollision) {
		t.Fatalf("non-candidate schema pinned: %v", err)
	}
}

func TestLaneIndependence(t *testing.T) {
	p := plan(t, commitA, 0)
	dev, review := p.Lanes()
	if err := CheckIndependence(dev, review); err != nil {
		t.Fatal(err)
	}
	same := review
	same.Name = dev.Name
	if CheckIndependence(dev, same) == nil {
		t.Fatal("shared identity accepted")
	}
	same = review
	same.StateDir = dev.StateDir
	if CheckIndependence(dev, same) == nil {
		t.Fatal("shared session accepted")
	}
	nested := review
	nested.StateDir = filepath.Join(dev.StateDir, "inner")
	if CheckIndependence(dev, nested) == nil {
		t.Fatal("nested session accepted")
	}
	same = review
	same.Lane = dev.Lane
	if CheckIndependence(dev, same) == nil {
		t.Fatal("shared lane accepted")
	}
	args := strings.Join(p.WorkerArgs(review, "/t/token", "AA"), " ")
	for _, want := range []string{"--roles acceptance", "--max-parallel 1", "--run-untagged=false", "--capabilities build,fixture-runtime", "--tags " + review.Lane, "--state-dir " + review.StateDir} {
		if !strings.Contains(args, want) {
			t.Fatalf("worker args miss %q: %s", want, args)
		}
	}
	if strings.Contains(strings.Join(p.WorkerArgs(dev, "/t/token", "AA"), " "), "acceptance") {
		t.Fatal("dev lane must not accept acceptance work")
	}
}

func TestReadinessBlocksWithConcreteOwner(t *testing.T) {
	pin := Pin{SOPRequired: true, SOPDigest: SOPDigest("approved"), Capabilities: []string{"fixture-runtime"}}
	if r := CheckReadiness(pin, "approved", []string{"fixture-runtime"}); r.Status != "passed" || r.Err() != nil {
		t.Fatalf("%+v", r)
	}
	r := CheckReadiness(pin, "", []string{"fixture-runtime"})
	if r.Status != "blocked" || r.NextOwner != "project-maintainer" || r.Err() == nil {
		t.Fatalf("missing SOP: %+v", r)
	}
	r = CheckReadiness(pin, "edited", []string{"fixture-runtime"})
	if r.Status != "blocked" || !strings.Contains(strings.Join(r.Missing, ";"), "digest differs") {
		t.Fatalf("changed SOP: %+v", r)
	}
	r = CheckReadiness(pin, "approved", nil)
	if r.Status != "blocked" || r.NextOwner != "environment-maintainer" {
		t.Fatalf("missing capability: %+v", r)
	}
	if r := CheckReadiness(Pin{SOPRequired: true}, "any", nil); r.Status != "blocked" {
		t.Fatalf("a required SOP without a digest cannot be pinned: %+v", r)
	}
	if r := CheckReadiness(Pin{}, "", nil); r.Status != "passed" {
		t.Fatalf("no requirement should pass: %+v", r)
	}
}

func TestEvidenceBindsIdentityAndRedactsSecrets(t *testing.T) {
	p := plan(t, commitA, 0)
	e := NewEvidence(p, "sha-of-binary")
	e.Redact("s3cr3t-token-value")
	e.Command("start", []string{"--token", "s3cr3t-token-value", "postgres://u:hunter2@h/db"}, 0, "ok password=hunter2 Bearer abcdef")
	for _, l := range []string{"offline", "environment-ready", "candidate-loaded", "lifecycle"} {
		e.Layers[l] = Passed
	}
	e.Finish()
	if e.Status != Passed || e.Layers["native-visual"] != Pending || e.Layers["live-write"] != NotRun {
		t.Fatalf("%+v", e.Layers)
	}
	path := filepath.Join(t.TempDir(), "e.json")
	if err := e.Write(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, leak := range []string{"s3cr3t-token-value", "hunter2", "abcdef"} {
		if strings.Contains(string(data), leak) {
			t.Fatalf("evidence leaks %q", leak)
		}
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatal("evidence must be owner-only")
	}
	if _, err := Verify(path, commitA); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(path, commitB); err == nil {
		t.Fatal("evidence accepted for another commit")
	}
}

func TestEvidenceNeverPassesWhenBlockedFailedOrStableChanged(t *testing.T) {
	p := plan(t, commitA, 0)
	e := NewEvidence(p, "x")
	e.Layers["offline"] = Passed
	e.Layers["environment-ready"] = Blocked
	e.Readiness = Readiness{Status: "blocked", NextOwner: "environment-maintainer", Action: "declare capability"}
	e.Finish()
	if e.Status != Blocked || e.NextOwner != "environment-maintainer" {
		t.Fatalf("%+v", e)
	}
	e = NewEvidence(p, "x")
	for _, l := range []string{"offline", "environment-ready", "candidate-loaded", "lifecycle"} {
		e.Layers[l] = Passed
	}
	e.StableDiff = []string{"stable config changed"}
	e.Finish()
	if e.Status != Failed {
		t.Fatalf("stable change must fail the run: %s", e.Status)
	}
	path := filepath.Join(t.TempDir(), "e.json")
	_ = e.Write(path)
	if _, err := Verify(path, commitA); err == nil {
		t.Fatal("a failed record verified")
	}
}

func TestStableSnapshotDetectsChanges(t *testing.T) {
	stable := testStable(t)
	ids := []string{"1:Mon Jan 1 00:00:00 2026"}
	probe := StableProbe{Processes: func() ([]string, error) { return ids, nil }}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	stable.Ports = []int{ln.Addr().(*net.TCPAddr).Port}
	a, err := TakeStable(stable, probe)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := TakeStable(stable, probe)
	if d := a.Diff(b); len(d) != 0 {
		t.Fatalf("unchanged host reported changes: %v", d)
	}
	// Run history is ignored; config, listener and process identity are not.
	_ = os.MkdirAll(filepath.Join(stable.Root, "data", "runs"), 0o700)
	_ = os.WriteFile(filepath.Join(stable.Root, "data", "runs", "r.json"), []byte("{}"), 0o600)
	if c, _ := TakeStable(stable, probe); len(a.Diff(c)) != 0 {
		t.Fatal("run history must not count")
	}
	_ = os.WriteFile(filepath.Join(stable.Root, "data", "queue.json"), []byte("{}"), 0o600)
	if c, _ := TakeStable(stable, probe); len(a.Diff(c)) == 0 {
		t.Fatal("data change missed")
	}
	_ = os.WriteFile(filepath.Join(stable.Root, "config", "config.yaml"), []byte("repos: {a: {}}\n"), 0o600)
	if c, _ := TakeStable(stable, probe); !strings.Contains(strings.Join(a.Diff(c), ";"), "config") {
		t.Fatal("config change missed")
	}
	ids = []string{"2:Tue Jan 2 00:00:00 2026"}
	ln.Close()
	c, _ := TakeStable(stable, probe)
	if d := strings.Join(a.Diff(c), ";"); !strings.Contains(d, "process") || !strings.Contains(d, "listener") {
		t.Fatalf("process or listener change missed: %s", d)
	}
}

// Crash cleanup must only ever signal processes this candidate started.
func TestStopProcessesTargetsOnlyOwnedProcesses(t *testing.T) {
	stable := testStable(t)
	p := plan(t, commitA, 0)
	m, err := Prepare(p, stable, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(p.Bin, "clawflow")
	foreign := "/usr/local/bin/clawflow"
	running := map[int]string{101: owned + " controller", 202: foreign + " controller", 303: "/bin/sleep 5"}
	var signalled []int
	oldCmd, oldSig, oldAlive := processCommand, signalProcess, alive
	defer func() { processCommand, signalProcess, alive = oldCmd, oldSig, oldAlive }()
	processCommand = func(pid int) string { return running[pid] }
	alive = func(pid int) bool { _, ok := running[pid]; return ok }
	signalProcess = func(pid int, _ syscall.Signal) error {
		signalled = append(signalled, pid)
		delete(running, pid)
		return nil
	}
	_ = m.Record("controller", 101, owned)
	_ = m.Record("stale-reused-pid", 303, owned)  // pid now belongs to /bin/sleep
	_ = m.Record("stable-impostor", 202, foreign) // outside the candidate bin
	stopped, err := m.StopProcesses(time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(signalled) != 1 || signalled[0] != 101 || len(stopped) != 1 || stopped[0] != "controller" {
		t.Fatalf("signalled=%v stopped=%v", signalled, stopped)
	}
	if _, ok := running[202]; !ok {
		t.Fatal("a stable process was stopped")
	}
	if _, ok := running[303]; !ok {
		t.Fatal("a reused pid was stopped")
	}
}

func TestRemoveRefusesUnownedOrRunning(t *testing.T) {
	stable := testStable(t)
	p := plan(t, commitA, 0)
	m, err := Prepare(p, stable, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	// Wrong token (tampered marker) is refused and nothing is deleted.
	bad := *m
	bad.Token = "ffffffffffffffffffffffffffffffff"
	if err := bad.Remove(); !errors.Is(err, ErrCollision) {
		t.Fatalf("remove with a foreign token: %v", err)
	}
	// A recorded live process blocks removal.
	oldCmd, oldAlive := processCommand, alive
	defer func() { processCommand, alive = oldCmd, oldAlive }()
	bin := filepath.Join(p.Bin, "clawflow")
	processCommand = func(int) string { return bin + " controller" }
	alive = func(int) bool { return true }
	_ = m.Record("controller", 4242, bin)
	if err := m.Remove(); err == nil || errors.Is(err, ErrCollision) {
		t.Fatalf("running candidate removed: %v", err)
	}
	alive = func(int) bool { return false }
	if err := m.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.Root); !os.IsNotExist(err) {
		t.Fatal("root still present")
	}
	if _, err := os.Stat(stable.Root); err != nil {
		t.Fatal("stable root disturbed")
	}
	// A symlinked root is never followed.
	p2 := plan(t, commitB, 0)
	target := t.TempDir()
	if err := os.MkdirAll(p2.Base, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p2.Root); err != nil {
		t.Fatal(err)
	}
	m2 := &Manifest{Plan: p2, Token: "x"}
	if err := m2.Remove(); !errors.Is(err, ErrCollision) {
		t.Fatalf("symlinked root: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("symlink target removed")
	}
}

func TestStartRefusesBinaryOutsideCandidate(t *testing.T) {
	stable := testStable(t)
	p := plan(t, commitA, 0)
	m, err := Prepare(p, stable, func(int) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start("controller", "/usr/local/bin/clawflow", []string{"controller"}, ""); !errors.Is(err, ErrCollision) {
		t.Fatalf("stable binary launched: %v", err)
	}
	if _, _, err := m.Output(context.Background(), "/bin/echo", nil, ""); !errors.Is(err, ErrCollision) {
		t.Fatalf("foreign binary executed: %v", err)
	}
}

func TestStableSnapshotChecksBytesRatherThanOnlyModificationTimes(t *testing.T) {
	stable := testStable(t)
	path := filepath.Join(stable.Root, "config", "config.yaml")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := TakeStable(stable, StableProbe{})
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(original), "repos", "other", 1)
	if len(changed) != len(original) {
		t.Fatal("fixture must preserve file size")
	}
	if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := TakeStable(stable, StableProbe{})
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.Diff(after)) == 0 {
		t.Fatal("configuration content change hidden by preserved size and timestamp")
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime().Add(time.Hour), info.ModTime().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	after, err = TakeStable(stable, StableProbe{})
	if err != nil {
		t.Fatal(err)
	}
	if diff := baseline.Diff(after); len(diff) != 0 {
		t.Fatal("unchanged bytes reported as a data mutation", diff)
	}
}

func TestVerifyRejectsContradictoryPassingStatus(t *testing.T) {
	p := plan(t, commitA, 0)
	e := NewEvidence(p, "binary")
	for _, layer := range []string{"offline", "environment-ready", "candidate-loaded", "lifecycle"} {
		e.Layers[layer] = Passed
	}
	e.Finish()
	e.Layers["lifecycle"] = Failed
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := e.Write(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(path, p.Commit); err == nil {
		t.Fatal("claimed pass accepted despite failed lifecycle")
	}
}
