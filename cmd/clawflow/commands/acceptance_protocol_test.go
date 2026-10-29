//go:build !windows

package commands

// These tests drive production runAcceptance and production execution.Run across
// a real subprocess boundary. The "Offload" executable is a shell wrapper that
// re-executes this test binary into TestAcceptanceProtocolHelper. Nothing here
// contacts a provider, a real Offload binary, or a VCS host, and the helper does
// not sandbox anything: it only observes argv/cwd/prompt and emits protocol
// records. It does not validate a true read-only source sandbox.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/offloadtest"
)

const (
	acceptanceProtocolHelperEnv     = "CLAWFLOW_ACCEPTANCE_PROTOCOL_HELPER"
	acceptanceProtocolCaptureEnv    = "CLAWFLOW_ACCEPTANCE_PROTOCOL_CAPTURE"
	acceptanceProtocolViolationsEnv = "CLAWFLOW_ACCEPTANCE_PROTOCOL_VIOLATIONS"
	acceptanceProtocolStoreEnv      = "CLAWFLOW_ACCEPTANCE_PROTOCOL_STORE"
	acceptanceProtocolPlanModeEnv   = "CLAWFLOW_ACCEPTANCE_PROTOCOL_PLAN_MODE"
	acceptanceProtocolReviewModeEnv = "CLAWFLOW_ACCEPTANCE_PROTOCOL_REVIEW_MODE"
	acceptanceProtocolProfile       = "acceptance-profile"
	acceptanceProtocolReviewerMark  = "Reviewer-only evidence marker"
)

type acceptanceProtocolCapture struct {
	Phase   string            `json:"phase"` // "plan" or "review"
	Argv    []string          `json:"argv"`  // real argv after the wrapper's "--"
	Args    map[string]string `json:"args"`
	Cwd     string            `json:"cwd"`
	Prompt  string            `json:"prompt"`
	Entries []string          `json:"entries"` // workspace listing just before the final record
	Token   string            `json:"token,omitempty"`
}

// TestAcceptanceProtocolHelper is entered only through the wrapper script below.
// It always exits the process so the test framework never writes to stdout.
func TestAcceptanceProtocolHelper(t *testing.T) {
	if os.Getenv(acceptanceProtocolHelperEnv) != "1" {
		return
	}
	os.Exit(runAcceptanceProtocolHelper())
}

func acceptanceProtocolArgv() []string {
	for i, a := range os.Args {
		if a == "--" {
			return append([]string(nil), os.Args[i+1:]...)
		}
	}
	return nil
}

func sameAcceptanceProtocolPath(a, b string) bool {
	x, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	y, err := filepath.EvalSymlinks(b)
	return err == nil && x == y
}

func readAcceptanceProtocolCaptures(path string) ([]acceptanceProtocolCapture, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []acceptanceProtocolCapture
	d := json.NewDecoder(f)
	for {
		var c acceptanceProtocolCapture
		if err := d.Decode(&c); err == io.EOF {
			return out, nil
		} else if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
}

// acceptanceProtocolPlanPersisted runs inside the reviewer helper, before any
// source tool/progress record. It verifies phase order and that the planner's
// exact output is durable in the fixture-supplied private store outside source.
func acceptanceProtocolPlanPersisted(store, cwd, prompt string, captures []acceptanceProtocolCapture) error {
	if len(captures) == 0 || captures[len(captures)-1].Phase != "plan" {
		return errors.New("review started without an immediately preceding planner invocation")
	}
	planners := 0
	for _, c := range captures {
		if c.Phase == "plan" {
			planners++
		}
	}
	token := captures[len(captures)-1].Token
	if token == "" || !strings.Contains(prompt, token) {
		return errors.New("review prompt lacks the planner output")
	}
	canonStore, err := filepath.EvalSymlinks(store)
	if err != nil {
		return fmt.Errorf("private plan store missing: %w", err)
	}
	canonCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return err
	}
	for _, pair := range [][2]string{{canonStore, canonCwd}, {canonCwd, canonStore}} {
		if rel, err := filepath.Rel(pair[0], pair[1]); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return errors.New("plan store and review source are not separate directories")
		}
	}
	entries, err := os.ReadDir(canonStore)
	if err != nil {
		return err
	}
	plans, found := 0, false
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".plan") {
			continue
		}
		plans++
		info, err := entry.Info()
		if err != nil || !entry.IsDir() || info.Mode().Perm()&0077 != 0 {
			return errors.New("persisted plan is not a private directory")
		}
		b, err := os.ReadFile(filepath.Join(canonStore, entry.Name(), "data.json"))
		if err != nil {
			return err
		}
		found = found || strings.Contains(string(b), token)
	}
	if plans != planners || !found {
		return fmt.Errorf("plan not persisted before review: %d stored for %d planner runs, current found=%t", plans, planners, found)
	}
	return nil
}

func runAcceptanceProtocolHelper() int {
	violate := func(format string, a ...any) int {
		if path := os.Getenv(acceptanceProtocolViolationsEnv); path != "" {
			if f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600); err == nil {
				fmt.Fprintf(f, format+"\n", a...)
				_ = f.Close()
			}
		}
		return 3
	}
	args := offloadtest.Args()
	argv := acceptanceProtocolArgv()
	promptBytes, err := io.ReadAll(os.Stdin)
	if err != nil {
		return violate("stdin: %v", err)
	}
	prompt := string(promptBytes)
	phase, mode := "review", os.Getenv(acceptanceProtocolReviewModeEnv)
	if args["-tools-disabled"] == "true" {
		phase, mode = "plan", os.Getenv(acceptanceProtocolPlanModeEnv)
	}
	// Both phases are fresh sessions with the pinned profile. The planner is
	// read-only; only the reviewer may use the command-capable policy.
	forbiddenFlags := []string{"-w", "-r", "-execution-policy", "-codex-sandbox", "-m"}
	if phase == "review" {
		forbiddenFlags = []string{"-r", "-codex-sandbox", "-m"}
		if _, ok := args["-w"]; !ok || args["-execution-policy"] != "managed-development" {
			return violate("review: command-capable policy flags missing: %v", argv)
		}
	}
	for _, forbidden := range forbiddenFlags {
		if _, ok := args[forbidden]; ok {
			return violate("%s: forbidden flag %s", phase, forbidden)
		}
	}
	caller := args["-caller-run-id"]
	if args["-p"] == "" || caller == "" || args["-events-file"] == "" || args["-d"] == "" {
		return violate("%s: missing required flag in %v", phase, argv)
	}
	if st, err := os.Stat(args["-instructions-file"]); err != nil || st.Mode().Perm() != 0600 {
		return violate("%s: instructions file missing or not private", phase)
	}
	cwd, err := os.Getwd()
	if err != nil || !sameAcceptanceProtocolPath(cwd, args["-d"]) {
		return violate("%s: cwd %q differs from -d %q", phase, cwd, args["-d"])
	}
	events, err := os.OpenFile(args["-events-file"], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return violate("%s: events file: %v", phase, err)
	}
	defer events.Close()

	runID := phase + "-run-" + execution.NewSessionKey()
	session := phase + "-native-" + execution.NewSessionKey()
	eventCaller := caller
	if mode == "wrong-caller" {
		eventCaller = "wrong-" + caller
	}
	seq := 0
	emit := func(typ, sess string, extra map[string]any) {
		seq++
		e := map[string]any{"schema_version": 1, "type": typ, "run_id": runID, "caller_run_id": eventCaller, "seq": seq, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "session": sess, "backend": "fixture", "model": "fixture-model"}
		for k, v := range extra {
			e[k] = v
		}
		_ = json.NewEncoder(events).Encode(e)
	}
	if phase == "review" {
		captures, err := readAcceptanceProtocolCaptures(os.Getenv(acceptanceProtocolCaptureEnv))
		if err != nil {
			return violate("review: capture unreadable: %v", err)
		}
		if err := acceptanceProtocolPlanPersisted(os.Getenv(acceptanceProtocolStoreEnv), cwd, prompt, captures); err != nil {
			return violate("review: %v", err)
		}
	}

	entries, _ := os.ReadDir(cwd)
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	text := "Independent acceptance plan token " + caller
	policy := "default"
	if phase == "review" {
		policy = "managed-development"
	}
	capture := acceptanceProtocolCapture{Phase: phase, Argv: argv, Args: args, Cwd: cwd, Prompt: prompt, Entries: names}
	if phase == "plan" {
		capture.Token = text
	} else {
		verdict := "passed"
		findings := "[]"
		if mode == "verdict-failed" {
			verdict = "failed"
			findings = `[{"kind":"requirement","summary":"missing behavior","requirement":"Required behavior","evidence":"TestBehavior fails"}]`
		}
		text = `{"status":"` + verdict + `","summary":"Independent protocol review complete","evidence":["` + acceptanceProtocolReviewerMark + `","command: go test ./... => ok"],"findings":` + findings + `}`
	}
	f, err := os.OpenFile(os.Getenv(acceptanceProtocolCaptureEnv), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		return violate("%s: capture: %v", phase, err)
	}
	if err := json.NewEncoder(f).Encode(capture); err != nil {
		return violate("%s: capture write: %v", phase, err)
	}
	_ = f.Close()
	// Record invocation before an intentionally invalid event lets the real
	// adapter terminate this helper. Plan validation above still precedes all
	// reviewer source-tool events.
	emit("start", "", nil)
	if phase == "review" {
		emit("progress", session, map[string]any{"native_type": "tool_use", "tool_category": "read"})
	} else {
		emit("progress", session, map[string]any{"native_type": "message"})
	}

	status, code, errorKind := "ok", 0, ""
	if mode == "failed" {
		status, code, errorKind = "error", 1, "execution"
		text = ""
	}
	switch mode {
	case "malformed-final":
		_, _ = io.WriteString(events, "{\"schema_version\":1,\"type\":\"final\",\n")
	case "missing-final":
	case "session-mutation":
		emit("final", session+"-mutated", map[string]any{"status": status, "exit_code": code})
	default:
		extra := map[string]any{"status": status, "exit_code": code}
		if errorKind != "" {
			extra["error_kind"] = errorKind
		}
		emit("final", session, extra)
	}

	resultCaller, resultSession := caller, session
	switch mode {
	case "wrong-caller-result":
		resultCaller = "wrong-" + caller
	case "result-session-mutation":
		resultSession = session + "-mutated"
	case "wrong-policy":
		policy = "default"
	}
	result := map[string]any{"run_id": runID, "caller_run_id": resultCaller, "session": resultSession, "profile": args["-p"], "backend": "fixture", "model": "fixture-model", "status": status, "exit_code": code, "result": text, "error_kind": errorKind, "error": "", "usage": nil, "execution_policy": policy}
	_ = json.NewEncoder(os.Stdout).Encode(result)
	return code
}

type acceptanceProtocolEnv struct {
	f                   *acceptanceFake
	capture, violations string
	git                 *[][]string
}

// acceptanceProtocolSetup reuses acceptanceFixture (fake VCS and Git, isolated
// HOME, development provenance) but restores the production execution.Run and
// points the configured command at the re-exec wrapper.
func acceptanceProtocolSetup(t *testing.T) *acceptanceProtocolEnv {
	t.Helper()
	f, _ := acceptanceFixture(t)
	// The redactor rewrites request text containing any *_TOKEN/_KEY/_SECRET/
	// _PASSWORD value, so blank ambient credentials for a hermetic run.
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		u := strings.ToUpper(k)
		if ok && v != "" && (strings.HasSuffix(u, "_TOKEN") || strings.HasSuffix(u, "_KEY") || strings.HasSuffix(u, "_SECRET") || strings.HasSuffix(u, "_PASSWORD")) {
			t.Setenv(k, "")
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	command := filepath.Join(dir, "offload")
	script := "#!/bin/sh\nexec '" + strings.ReplaceAll(exe, "'", "'\\''") + "' -test.run=^TestAcceptanceProtocolHelper$ -- \"$@\"\n"
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	e := &acceptanceProtocolEnv{f: f, capture: filepath.Join(dir, "capture.jsonl"), violations: filepath.Join(dir, "violations.txt"), git: &[][]string{}}
	t.Setenv("CLAWFLOW_OFFLOAD_COMMAND", command)
	t.Setenv("CLAWFLOW_OFFLOAD_PROFILE", acceptanceProtocolProfile)
	t.Setenv(acceptanceProtocolHelperEnv, "1")
	t.Setenv(acceptanceProtocolCaptureEnv, e.capture)
	t.Setenv(acceptanceProtocolViolationsEnv, e.violations)
	t.Setenv(acceptanceProtocolStoreEnv, acceptanceStorePath("o/r", 11))
	t.Setenv(acceptanceProtocolPlanModeEnv, "")
	t.Setenv(acceptanceProtocolReviewModeEnv, "")
	acceptanceNativeRun = execution.Run
	baseGit := acceptanceGit
	acceptanceGit = func(ctx context.Context, dir string, args ...string) (string, error) {
		*e.git = append(*e.git, append([]string{}, args...))
		return baseGit(ctx, dir, args...)
	}
	return e
}

func (e *acceptanceProtocolEnv) run() (acceptance.Record, error) {
	// A generous budget absorbs process startup in a loaded suite; each phase's
	// -t value is derived from the time remaining on this context.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return runAcceptance(ctx, e.f, "o/r", 11)
}

func (e *acceptanceProtocolEnv) captures(t *testing.T) []acceptanceProtocolCapture {
	t.Helper()
	c, err := readAcceptanceProtocolCaptures(e.capture)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (e *acceptanceProtocolEnv) assertNoViolations(t *testing.T) {
	t.Helper()
	b, err := os.ReadFile(e.violations)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(b) != 0 {
		t.Fatalf("helper observed protocol violations:\n%s", b)
	}
}

// assertNoWrites checks the VCS fake and the Git seam for merge/network writes.
func (e *acceptanceProtocolEnv) assertNoWrites(t *testing.T) {
	t.Helper()
	if len(e.f.merged) != 0 || len(e.f.deletedBranches) != 0 || len(e.f.comments) != 0 || len(e.f.expected) != 0 {
		t.Fatalf("unexpected VCS writes: merged=%v deleted=%v comments=%v conditional=%v", e.f.merged, e.f.deletedBranches, e.f.comments, e.f.expected)
	}
	allowed := []string{"config", "init", "remote", "check-ref-format", "fetch", "rev-parse", "checkout", "status", "rev-list", "merge-base"}
	for _, args := range *e.git {
		if !slices.Contains(allowed, args[0]) {
			t.Fatalf("unexpected git operation %v", args)
		}
	}
}

func (e *acceptanceProtocolEnv) store(t *testing.T) (*acceptance.Store, acceptance.Contract) {
	t.Helper()
	c, _, _, _, err := acceptanceContract(context.Background(), e.f, "o/r", 11)
	if err != nil {
		t.Fatal(err)
	}
	s, err := acceptance.OpenStore(acceptanceStorePath("o/r", 11))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, c
}

func (e *acceptanceProtocolEnv) planDirs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(acceptanceStorePath("o/r", 11))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	n := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".plan") {
			n++
		}
	}
	return n
}

func hasAcceptanceProtocolFlag(argv []string, flag string) bool { return slices.Contains(argv, flag) }

func TestAcceptanceProtocolPlannerReviewerThroughExecutionRun(t *testing.T) {
	e := acceptanceProtocolSetup(t)
	r, err := e.run()
	if err != nil {
		t.Fatal(err)
	}
	e.assertNoViolations(t)
	caps := e.captures(t)
	if len(caps) != 2 || caps[0].Phase != "plan" || caps[1].Phase != "review" {
		t.Fatalf("phase order: %+v", caps)
	}
	plan, review := caps[0], caps[1]

	for _, c := range caps {
		for _, flag := range []string{"-json", "-no-salvage"} {
			if !hasAcceptanceProtocolFlag(c.Argv, flag) {
				t.Fatalf("%s argv lacks %s: %v", c.Phase, flag, c.Argv)
			}
		}
		forbidden := []string{"-w", "-r", "-execution-policy", "-codex-sandbox", "-m"}
		if c.Phase == "review" {
			// Only the reviewer runs commands, and only in its disposable checkout.
			forbidden = []string{"-r", "-codex-sandbox", "-m"}
			for _, flag := range []string{"-w", "-execution-policy"} {
				if !hasAcceptanceProtocolFlag(c.Argv, flag) {
					t.Fatalf("reviewer argv lacks %s: %v", flag, c.Argv)
				}
			}
		}
		for _, flag := range forbidden {
			if hasAcceptanceProtocolFlag(c.Argv, flag) {
				t.Fatalf("%s argv has forbidden flag %s: %v", c.Phase, flag, c.Argv)
			}
		}
		if c.Args["-p"] != acceptanceProtocolProfile {
			t.Fatalf("%s profile %q", c.Phase, c.Args["-p"])
		}
		// The helper compared live inodes while the directory existed; the
		// directory is gone now, so compare the recorded strings.
		if filepath.Clean(c.Cwd) != filepath.Clean(c.Args["-d"]) {
			t.Fatalf("%s cwd %q differs from -d %q", c.Phase, c.Cwd, c.Args["-d"])
		}
		if len(c.Entries) != 0 {
			t.Fatalf("%s workspace was modified: %v", c.Phase, c.Entries)
		}
		d, err := time.ParseDuration(c.Args["-t"])
		if err != nil || d < 15*time.Second {
			t.Fatalf("%s timeout budget %q is too small for a loaded suite", c.Phase, c.Args["-t"])
		}
		if _, err := os.Stat(c.Cwd); !os.IsNotExist(err) {
			t.Fatalf("%s temporary workspace was not cleaned", c.Phase)
		}
	}
	if !hasAcceptanceProtocolFlag(plan.Argv, "-tools-disabled") || hasAcceptanceProtocolFlag(review.Argv, "-tools-disabled") {
		t.Fatalf("tools policy: planner=%v reviewer=%v", plan.Argv, review.Argv)
	}
	if plan.Cwd == review.Cwd || plan.Args["-caller-run-id"] == review.Args["-caller-run-id"] {
		t.Fatalf("phases share workspace or caller identity: %+v %+v", plan.Args, review.Args)
	}
	for _, forbidden := range []string{e.f.pr.HeadSHA, e.f.pr.BaseSHA, "dev-session", "dev-run", "dev-caller", "Implementation", "fix/issue-42", review.Cwd, review.Args["-d"], acceptanceProtocolReviewerMark} {
		if strings.Contains(plan.Prompt, forbidden) {
			t.Fatalf("planner received source or developer input %q", forbidden)
		}
	}
	if !strings.Contains(plan.Prompt, "Meet the original goal") {
		t.Fatal("planner lacks requirements")
	}
	for _, want := range []string{e.f.pr.HeadSHA, plan.Token} {
		if !strings.Contains(review.Prompt, want) {
			t.Fatalf("reviewer prompt lacks %q", want)
		}
	}
	for _, forbidden := range []string{"dev-session", "dev-run"} {
		if strings.Contains(review.Prompt, forbidden) {
			t.Fatalf("reviewer received developer identity %q", forbidden)
		}
	}

	// The record carries identities parsed from the subprocess protocol.
	if r.Verdict != acceptance.Passed || !r.Completed || !r.Before.Clean || !r.After.Clean || r.Before.HeadSHA != e.f.pr.HeadSHA || r.After.HeadSHA != e.f.pr.HeadSHA {
		t.Fatalf("record: %+v", r)
	}
	if !strings.HasPrefix(r.Plan.Planner.SessionID, "plan-native-") || !strings.HasPrefix(r.Reviewer.SessionID, "review-native-") || r.Plan.Planner.SessionID == r.Reviewer.SessionID || r.Plan.Planner.RunID == r.Reviewer.RunID {
		t.Fatalf("native identities: planner=%+v reviewer=%+v", r.Plan.Planner, r.Reviewer)
	}
	if r.Plan.Planner.Policy != acceptance.PlannerPolicy || r.Reviewer.Policy != acceptance.ReviewerPolicy {
		t.Fatalf("policies: planner=%q reviewer=%q", r.Plan.Planner.Policy, r.Reviewer.Policy)
	}
	for _, n := range []acceptance.Native{r.Plan.Planner, r.Reviewer} {
		if n.Profile != acceptanceProtocolProfile || n.Backend != "fixture" || n.Status != "ok" || n.ExitCode != 0 {
			t.Fatalf("native identity: %+v", n)
		}
	}
	if r.Plan.Text != plan.Token {
		t.Fatalf("stored plan text %q", r.Plan.Text)
	}

	store, c := e.store(t)
	stored, err := store.LoadRecord(c, r.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if err := acceptance.Evaluate(c, stored); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadPlan(c, r.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := currentAcceptance(context.Background(), e.f, "o/r", 11); err != nil {
		t.Fatal(err)
	}
	e.assertNoWrites(t)
}

func TestAcceptanceProtocolFailedPlannerSkipsReviewer(t *testing.T) {
	e := acceptanceProtocolSetup(t)
	t.Setenv(acceptanceProtocolPlanModeEnv, "failed")
	r, err := e.run()
	if !execution.IsKind(err, "execution") {
		t.Fatalf("want execution failure, got %v", err)
	}
	e.assertNoViolations(t)
	caps := e.captures(t)
	if len(caps) != 1 || caps[0].Phase != "plan" {
		t.Fatalf("reviewer must not run after a failed planner: %+v", caps)
	}
	if e.planDirs(t) != 0 || r.Plan.Text != "" || r.Reviewer.SessionID != "" || r.Verdict != acceptance.Blocked || !r.Completed {
		t.Fatalf("failed planner left plan or review state: plans=%d record=%+v", e.planDirs(t), r)
	}
	store, c := e.store(t)
	latest, err := store.LatestRecord(c)
	if err != nil || latest.AttemptID != r.AttemptID || latest.Verdict != acceptance.Blocked || acceptance.Evaluate(c, latest) == nil {
		t.Fatalf("stored latest record: %+v err=%v", latest, err)
	}
	if _, _, err := currentAcceptance(context.Background(), e.f, "o/r", 11); err == nil {
		t.Fatal("blocked planner attempt passed the gate")
	}
	e.assertNoWrites(t)
}

// Each case first stores a genuine pass through the subprocess boundary, then
// repeats the review with a reviewer protocol or result fault. The latest record
// must be nonpassing, the older pass must not be reused, and the merge gate must
// refuse without any merge or network write.
func TestAcceptanceProtocolFaultsBlockAfterEarlierPass(t *testing.T) {
	cases := []struct {
		name, mode, kind string
		verdict          acceptance.Verdict
	}{
		{"reviewer-execution-failed", "failed", "execution", acceptance.Blocked},
		{"malformed-final", "malformed-final", "protocol_invalid_event", acceptance.Blocked},
		{"missing-final", "missing-final", "protocol_missing_final", acceptance.Blocked},
		{"wrong-caller-in-events", "wrong-caller", "protocol_invalid_event", acceptance.Blocked},
		{"wrong-caller-in-result", "wrong-caller-result", "protocol_final_mismatch", acceptance.Blocked},
		{"session-mutation-in-events", "session-mutation", "protocol_session_mismatch", acceptance.Blocked},
		{"session-mutation-in-result", "result-session-mutation", "protocol_final_mismatch", acceptance.Blocked},
		{"wrong-policy", "wrong-policy", "protocol_policy_mismatch", acceptance.Blocked},
		{"valid-protocol-failed-verdict", "verdict-failed", "", acceptance.Failed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := acceptanceProtocolSetup(t)
			first, err := e.run()
			if err != nil || first.Verdict != acceptance.Passed {
				t.Fatalf("initial pass: %+v %v", first, err)
			}
			t.Setenv(acceptanceProtocolReviewModeEnv, tc.mode)
			r, err := e.run()
			if err == nil {
				t.Fatal("faulty review was accepted")
			}
			if tc.kind != "" && !execution.IsKind(err, tc.kind) {
				t.Fatalf("want %s, got %v", tc.kind, err)
			}
			e.assertNoViolations(t)
			if caps := e.captures(t); len(caps) != 4 || caps[2].Phase != "plan" || caps[3].Phase != "review" {
				t.Fatalf("captures: %+v", caps)
			}
			if r.Verdict != tc.verdict || !r.Completed || r.AttemptID == first.AttemptID || acceptance.Evaluate(r.Contract, r) == nil {
				t.Fatalf("record must be completed and nonpassing: %+v", r)
			}
			store, c := e.store(t)
			latest, err := store.LatestRecord(c)
			if err != nil || latest.AttemptID != r.AttemptID || latest.Verdict != tc.verdict || acceptance.Evaluate(c, latest) == nil {
				t.Fatalf("latest stored record: %+v err=%v", latest, err)
			}
			if _, _, err := currentAcceptance(context.Background(), e.f, "o/r", 11); err == nil {
				t.Fatal("older pass was reused after a faulty review")
			}
			if err := explicitAcceptanceMerge(context.Background(), e.f, "o/r", 11); err == nil {
				t.Fatal("merge gate accepted a faulty review")
			}
			e.assertNoWrites(t)
		})
	}
}
