//go:build !windows

package execution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// env builds a hermetic environment: only the given variables, with a system
// PATH appended so fixture scripts can run cat/sleep.
func env(m map[string]string) func(string) (string, bool) {
	all := map[string]string{}
	for k, v := range m {
		all[k] = v
	}
	if v, ok := all["PATH"]; ok {
		all["PATH"] = v + ":/usr/bin:/bin"
	} else {
		all["PATH"] = "/usr/bin:/bin"
	}
	return func(k string) (string, bool) { v, ok := all[k]; return v, ok }
}

var hermetic = ReadinessOptions{Env: env(nil)}

// offloadScript prints RequiredFlags help for -h and a profiles listing.
func offloadScript(t *testing.T, dir string, profilesJSON string, declareProfiles bool) string {
	t.Helper()
	help := helpFor(RequiredFlags, "")
	if declareProfiles {
		help += "  offload profiles [--json]     list profiles\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "help.txt"), []byte(help), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "profiles.json"), []byte(profilesJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `case "$1" in
-h) cat '` + filepath.Join(dir, "help.txt") + `' >&2; exit 2;;
profiles) if [ "$2" = "--json" ]; then cat '` + filepath.Join(dir, "profiles.json") + `'; else echo "  base claude"; fi; exit 0;;
*) echo "TASK STARTED" > '` + filepath.Join(dir, "task-started") + `'; exit 9;;
esac
`
	return writeScript(t, dir, "offload", body)
}

const oneProfile = `{"schema_version":1,"config_exists":true,"profiles":[{"name":"base"}]}`

func TestReadinessReadyIsLiveAuthUnverifiedAndSpawnsNoTask(t *testing.T) {
	shortProbe(t, 30*time.Second)
	dir := t.TempDir()
	cmd := offloadScript(t, dir, oneProfile, true)
	r := CheckReadiness(context.Background(), Config{Command: cmd, Profile: "base"}, nil, ReadinessOptions{Env: env(map[string]string{})})
	if r.State != StateReady || r.Command != cmd {
		t.Fatalf("%+v", r)
	}
	joined := strings.Join(r.Unverified, ",")
	if !strings.Contains(joined, "live_authentication") || !strings.Contains(joined, "provider_execution") {
		t.Fatalf("unverified=%v", r.Unverified)
	}
	if _, err := os.Stat(filepath.Join(dir, "task-started")); err == nil {
		t.Fatal("readiness started a task")
	}
	if strings.Contains(r.Summary, "authenticated") && !strings.Contains(r.Summary, "not verified") {
		t.Fatalf("summary overclaims: %q", r.Summary)
	}
}

func TestReadinessMissingExecutableShowsGuidance(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "offload")
	r := CheckReadiness(context.Background(), Config{Command: missing, Profile: "base"}, nil, ReadinessOptions{Env: env(map[string]string{})})
	if r.State != StateBlocked || r.Reason != ReasonMissing {
		t.Fatalf("%+v", r)
	}
	if r.ConfiguredCommand != missing || !strings.Contains(r.Summary, missing) {
		t.Fatalf("configured path not shown: %+v", r)
	}
	g := r.Guidance
	if g == nil || g.Link != OffloadInstallDocs || g.Recheck != RecheckCommand || !strings.Contains(strings.Join(g.Steps, "\n"), OffloadInstallCmd) {
		t.Fatalf("guidance=%+v", g)
	}
	// The only install command is the one the Offload README documents.
	for _, s := range g.Steps {
		if strings.Contains(s, "curl") || strings.Contains(s, "brew ") {
			t.Fatalf("invented installer: %q", s)
		}
	}
}

func TestReadinessWorkerPathMismatch(t *testing.T) {
	shortProbe(t, 30*time.Second)
	dir := t.TempDir()
	offloadScript(t, dir, oneProfile, true)
	cfg := Config{Profile: "base"} // bare "offload" resolved through the worker PATH
	shell := CheckReadiness(context.Background(), cfg, nil, ReadinessOptions{Env: env(map[string]string{"PATH": dir})})
	worker := CheckReadiness(context.Background(), cfg, nil, ReadinessOptions{Env: env(map[string]string{"PATH": "/usr/bin:/bin"}), Worker: "w1", Environment: "service"})
	if shell.State != StateReady || worker.State != StateBlocked || worker.Reason != ReasonMissing || worker.Worker != "w1" {
		t.Fatalf("shell=%+v worker=%+v", shell.State, worker)
	}
}

func TestReadinessCommandOverrideAndRoleProfile(t *testing.T) {
	shortProbe(t, 30*time.Second)
	dir := t.TempDir()
	good := offloadScript(t, dir, `{"schema_version":1,"config_exists":true,"profiles":[{"name":"base"},{"name":"review"}]}`, true)
	c := Config{Command: filepath.Join(t.TempDir(), "gone"), Profile: "base", RoleProfiles: map[string]string{"acceptance": "review"}}
	// Environment command override wins over the configured path.
	r := CheckReadiness(context.Background(), c, []string{"operator", "acceptance"}, ReadinessOptions{Env: env(map[string]string{"CLAWFLOW_OFFLOAD_COMMAND": good})})
	if r.State != StateReady || strings.Join(r.Profiles, ",") != "base,review" {
		t.Fatalf("%+v", r)
	}
	// A role profile that does not exist blocks only through the role selection.
	c.RoleProfiles["acceptance"] = "missing"
	r = CheckReadiness(context.Background(), c, []string{"operator", "acceptance"}, ReadinessOptions{Env: env(map[string]string{"CLAWFLOW_OFFLOAD_COMMAND": good})})
	if r.State != StateBlocked || r.Reason != ReasonProfileNotFound {
		t.Fatalf("%+v", r)
	}
	r = CheckReadiness(context.Background(), c, []string{"operator"}, ReadinessOptions{Env: env(map[string]string{"CLAWFLOW_OFFLOAD_COMMAND": good})})
	if r.State != StateReady {
		t.Fatalf("operator-only worker must not need the review profile: %+v", r)
	}
	// An environment profile overrides every role.
	r = CheckReadiness(context.Background(), c, []string{"acceptance"}, ReadinessOptions{Env: env(map[string]string{"CLAWFLOW_OFFLOAD_COMMAND": good, "CLAWFLOW_OFFLOAD_PROFILE": "base"})})
	if r.State != StateReady || strings.Join(r.Profiles, ",") != "base" {
		t.Fatalf("%+v", r)
	}
}

func TestReadinessAbsentProfileConfiguration(t *testing.T) {
	shortProbe(t, 30*time.Second)
	cmd := offloadScript(t, t.TempDir(), `{"schema_version":1,"config_exists":false,"profiles":[]}`, true)
	r := CheckReadiness(context.Background(), Config{Command: cmd, Profile: "base"}, nil, hermetic)
	if r.State != StateBlocked || r.Reason != ReasonProfileNotFound || !strings.Contains(strings.Join(r.Guidance.Steps, " "), "offload config") {
		t.Fatalf("%+v", r)
	}
	r = CheckReadiness(context.Background(), Config{Command: cmd}, nil, hermetic)
	if r.Reason != ReasonProfileMissing {
		t.Fatalf("empty profile: %+v", r)
	}
	// A nonempty string alone is never proof the profile exists.
	r = CheckReadiness(context.Background(), Config{Command: cmd, Profile: "anything"}, nil, hermetic)
	if r.State == StateReady {
		t.Fatalf("nonempty profile string treated as existing: %+v", r)
	}
}

func TestReadinessProfileListingUnsupportedStaysUnverified(t *testing.T) {
	shortProbe(t, 30*time.Second)
	cmd := offloadScript(t, t.TempDir(), oneProfile, false)
	r := CheckReadiness(context.Background(), Config{Command: cmd, Profile: "whatever"}, nil, hermetic)
	if r.State != StateReady || !strings.Contains(strings.Join(r.Unverified, ","), "profile_existence") {
		t.Fatalf("%+v", r)
	}
}

func TestReadinessAliasAcceptedFromListing(t *testing.T) {
	shortProbe(t, 30*time.Second)
	dir := t.TempDir()
	cmd := offloadScript(t, dir, oneProfile, true)
	// Replace plain listing with an alias line.
	body, _ := os.ReadFile(cmd)
	os.WriteFile(cmd, []byte(strings.Replace(string(body), `echo "  base claude"`, `echo "* base claude (aliases: cc, fast)"`, 1)), 0o700)
	r := CheckReadiness(context.Background(), Config{Command: cmd, Profile: "fast"}, nil, hermetic)
	if r.State != StateReady {
		t.Fatalf("%+v", r)
	}
}

func TestReadinessIncompatibleFlagsAndNotExecutableAndTimeout(t *testing.T) {
	dir := t.TempDir()
	old := writeScript(t, dir, "offload", "echo 'Usage of offload:' >&2\necho '  -json\tdesc' >&2\nexit 2\n")
	shortProbe(t, 30*time.Second)
	r := CheckReadiness(context.Background(), Config{Command: old, Profile: "p"}, nil, hermetic)
	if r.Reason != ReasonIncompatible || !strings.Contains(r.Report.Checks[3].Message, "required flags") {
		t.Fatalf("%+v", r)
	}
	plain := filepath.Join(t.TempDir(), "offload")
	os.WriteFile(plain, []byte("#!/bin/sh\n"), 0o600)
	r = CheckReadiness(context.Background(), Config{Command: plain, Profile: "p"}, nil, hermetic)
	if r.Reason != ReasonNotExecutable {
		t.Fatalf("%+v", r)
	}
	slow := writeScript(t, t.TempDir(), "offload", "sleep 30\n")
	shortProbe(t, 300*time.Millisecond)
	start := time.Now()
	r = CheckReadiness(context.Background(), Config{Command: slow, Profile: "p"}, nil, hermetic)
	if r.Reason != ReasonTimeout || time.Since(start) > 5*time.Second {
		t.Fatalf("%+v after %s", r, time.Since(start))
	}
}

func TestCacheCoalescesAndRecoversAfterInstall(t *testing.T) {
	var runs atomic.Int32
	now := time.Unix(1000, 0)
	state := StateBlocked
	c := &Cache{ReadyTTL: time.Minute, BlockedTTL: 15 * time.Second, Now: func() time.Time { return now },
		Run: func(context.Context) Readiness { runs.Add(1); return Readiness{State: state} }}
	for i := 0; i < 20; i++ {
		c.Get(context.Background(), false)
	}
	if runs.Load() != 1 {
		t.Fatalf("probes=%d", runs.Load())
	}
	state = StateReady // user installs Offload
	if c.Get(context.Background(), false).State != StateBlocked {
		t.Fatal("expected cached result inside TTL")
	}
	now = now.Add(16 * time.Second)
	if got := c.Get(context.Background(), false); got.State != StateReady || runs.Load() != 2 {
		t.Fatalf("blocked result did not expire: %+v runs=%d", got, runs.Load())
	}
	c.Get(context.Background(), false)
	if runs.Load() != 2 {
		t.Fatal("ready result re-probed inside TTL")
	}
	if c.Get(context.Background(), true); runs.Load() != 3 {
		t.Fatal("explicit recheck must probe")
	}
}

func TestSummarizeViewerOnlyAndWorkers(t *testing.T) {
	now := time.Unix(5, 0)
	if d := Summarize(now, []Readiness{{State: StateNotRequired}}); d.State != StateNotRequired || !strings.Contains(d.Summary, "not required") {
		t.Fatalf("%+v", d)
	}
	d := Summarize(now, []Readiness{{Worker: "a", State: StateReady}, {Worker: "b", State: StateBlocked}})
	if d.State != StateBlocked || !strings.Contains(d.Summary, "b") || strings.Contains(d.Summary, "a,") {
		t.Fatalf("%+v", d)
	}
	if d := Summarize(now, []Readiness{{Worker: "a", State: StateReady}, {Worker: "c", State: StateUnreported}}); d.State != StateUnreported {
		t.Fatalf("%+v", d)
	}
}

func TestStatusCacheTTLAndPrivacy(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1000, 0)
	doc := Summarize(now, []Readiness{{Worker: "w", State: StateBlocked, Reason: ReasonMissing}})
	if err := WriteStatusCache(dir, doc); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(dir, StatusCacheFile))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode())
	}
	if _, ok := ReadStatusCache(dir, now.Add(10*time.Second)); !ok {
		t.Fatal("fresh blocked cache rejected")
	}
	if _, ok := ReadStatusCache(dir, now.Add(20*time.Second)); ok {
		t.Fatal("stale blocked cache accepted")
	}
}

func TestReleaseErrorTypes(t *testing.T) {
	err := ValidateConfig(Config{Command: filepath.Join(t.TempDir(), "none"), Profile: "p"})
	if reason, ok := IsUnavailable(err); !ok || reason != ReasonMissing {
		t.Fatalf("%v", err)
	}
}
