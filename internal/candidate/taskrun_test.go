package candidate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func passedRun(commit string) *TaskRun {
	r := &TaskRun{Commit: commit, RuntimeID: IDFor(commit), TaskID: "t1", State: "done", Settled: true,
		ResidualProcesses: []string{}, ResidualLocks: []string{}}
	for _, n := range []string{"claimed", "executed", "written-back", "finished"} {
		r.step(n, true, n+" observed")
	}
	return r
}

// The task verdict follows from the recorded facts. Each defect the issue names
// (a skipped stage, a leftover process, a leftover lock, an unchecked run) fails
// it, and a "passed" label is never read from anywhere.
func TestTaskRunPassesOnlyWhenEveryFactHolds(t *testing.T) {
	if !passedRun(commitA).Passed() {
		t.Fatal("a complete clean run must pass")
	}
	cases := map[string]func(*TaskRun){
		"not claimed":      func(r *TaskRun) { r.Steps[0].Passed = false },
		"not executed":     func(r *TaskRun) { r.Steps[1].Passed = false },
		"not written back": func(r *TaskRun) { r.Steps[2].Passed = false },
		"not finished":     func(r *TaskRun) { r.Steps[3].Passed = false },
		"missing step":     func(r *TaskRun) { r.Steps = r.Steps[:3] },
		"extra step":       func(r *TaskRun) { r.step("bonus", true, "") },
		"reordered":        func(r *TaskRun) { r.Steps[0], r.Steps[1] = r.Steps[1], r.Steps[0] },
		"renamed":          func(r *TaskRun) { r.Steps[1].Name = "claimed" },
		"leftover process": func(r *TaskRun) { r.ResidualProcesses = []string{"agent (pid 7)"} },
		"leftover lock":    func(r *TaskRun) { r.ResidualLocks = []string{"state/locks/x.lock"} },
		"failure recorded": func(r *TaskRun) { r.Failure = "boom" },
		"never settled":    func(r *TaskRun) { r.Settled = false },
		"no steps at all":  func(r *TaskRun) { r.Steps = nil },
	}
	for name, mutate := range cases {
		r := passedRun(commitA)
		mutate(r)
		if r.Passed() {
			t.Errorf("%s: run passed", name)
		}
	}
}

func TestClipBoundsAndRedactsWithoutChangingTheVerdict(t *testing.T) {
	r := passedRun(commitA)
	r.Steps[0].Evidence = strings.Repeat("x", 5000) + " Bearer sk-live-123456 password=hunter22\x1b[31m\n\x00"
	r.Audit = make([]string, 100)
	r.ResidualProcesses = make([]string, 100)
	r.Clip()
	if len(r.Steps[0].Evidence) > maxRunText+len("…") || strings.ContainsAny(r.Steps[0].Evidence, "\x00\x1b\n") {
		t.Fatalf("evidence not bounded or cleaned: %q", r.Steps[0].Evidence[:60])
	}
	if len(r.Audit) > maxRunItems+1 || len(r.ResidualProcesses) > maxRunItems+1 {
		t.Fatalf("lists not bounded: %d %d", len(r.Audit), len(r.ResidualProcesses))
	}
	// Clipping a long residual list must not hide that something was left behind.
	if r.Passed() {
		t.Fatal("clipping turned a run with residual processes into a pass")
	}
	leak := passedRun(commitA)
	leak.Steps[0].Evidence = "Bearer abcdefghijkl password=secretvalue url=http://u:pw@host/x"
	leak.Clip()
	for _, bad := range []string{"abcdefghijkl", "secretvalue", ":pw@"} {
		if strings.Contains(leak.Steps[0].Evidence, bad) {
			t.Fatalf("secret %q survived: %s", bad, leak.Steps[0].Evidence)
		}
	}
}

func passedEvidence(commit string) Evidence {
	p, _ := NewPlan(commit, "/tmp/c", 0, Pin{})
	e := NewEvidence(p, "")
	e.TaskRequired = true
	e.Task = passedRun(commit)
	for _, l := range []string{"environment-ready", "candidate-loaded", "lifecycle"} {
		e.Layers[l] = Passed
	}
	e.Finish()
	return *e
}

func TestEvidenceRequiresARealPassedTaskWhenAsked(t *testing.T) {
	e := passedEvidence(commitA)
	if e.Status != Passed || e.Layers[TaskLayer] != Passed {
		t.Fatalf("status=%s layers=%v", e.Status, e.Layers)
	}
	if err := CheckEvidence(e, commitA, true); err != nil {
		t.Fatal(err)
	}
	// Offline unit tests are CI's job; their absence does not fail the run.
	if e.Layers["offline"] != NotRun {
		t.Fatalf("offline should stay not-run, got %s", e.Layers["offline"])
	}
	if err := CheckEvidence(e, commitB, true); err == nil {
		t.Fatal("evidence for another commit accepted")
	}
	noTask := e
	noTask.TaskRequired, noTask.Task = false, nil
	if err := CheckEvidence(noTask, commitA, true); err == nil || !strings.Contains(err.Error(), "no real task run") {
		t.Fatalf("evidence without a task run accepted: %v", err)
	}
}

func TestEvidenceTaskLayerIsRecomputedNotTrusted(t *testing.T) {
	for name, mutate := range map[string]func(*Evidence){
		"residual process after the label said passed": func(e *Evidence) { e.Task.ResidualProcesses = []string{"agent (pid 3)"} },
		"residual lock":   func(e *Evidence) { e.Task.ResidualLocks = []string{"a.lock"} },
		"unfinished step": func(e *Evidence) { e.Task.Steps[3].Passed = false },
		"task run swapped from another candidate": func(e *Evidence) { e.Task.RuntimeID = "000000000000" },
	} {
		e := passedEvidence(commitA)
		mutate(&e)
		e.Layers[TaskLayer] = Passed // a forged layer must not matter
		e.Status = Passed            // nor a forged status
		if err := CheckEvidence(e, commitA, true); err == nil {
			t.Errorf("%s: forged evidence accepted", name)
		}
	}
	e := passedEvidence(commitA)
	e.Task.Settled = false
	e.Finish()
	if e.Status == Passed {
		t.Fatal("a run that was never settled passed")
	}
}

func TestVerifyWithRoundTripsThroughTheFile(t *testing.T) {
	e := passedEvidence(commitA)
	path := filepath.Join(t.TempDir(), "evidence.json")
	data, _ := json.Marshal(e)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyWith(path, commitA, true); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(path, commitA); err != nil {
		t.Fatalf("the plain verify must accept task evidence too: %v", err)
	}
	e.Task.ResidualLocks = []string{"x.lock"}
	data, _ = json.Marshal(e)
	_ = os.WriteFile(path, data, 0o600)
	if _, err := VerifyWith(path, commitA, true); err == nil {
		t.Fatal("tampered file accepted")
	}
}
