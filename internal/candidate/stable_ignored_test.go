package candidate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeData(t *testing.T, stable Stable, rel, content string) {
	t.Helper()
	path := filepath.Join(stable.Root, "data", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStableEvidencePassesOnSchedulerActivity(t *testing.T) {
	stable := testStable(t)
	writeData(t, stable, "coordination/d/1.json", "record")
	execs := []string{"1:Mon Jan 1 00:00:00 2026"}
	probe := StableProbe{
		Processes: func() ([]string, error) { return []string{"9:svc"}, nil },
		Executors: func() ([]string, error) { return execs, nil },
	}
	before, err := TakeStable(stable, probe)
	if err != nil {
		t.Fatal(err)
	}
	writeData(t, stable, "preflight/o__r/7.json", "new")
	writeData(t, stable, "coordination/d/1.json.lock", "")
	writeData(t, stable, "coordination/d/2.json.lock", "")
	execs = []string{"2:Mon Jan 1 00:01:00 2026", "3:Mon Jan 1 00:02:00 2026"}
	after, err := TakeStable(stable, probe)
	if err != nil {
		t.Fatal(err)
	}
	if d := before.Diff(after); len(d) != 0 {
		t.Fatalf("scheduler activity reported as contamination: %v", d)
	}
	ign := before.Ignored(after)
	if len(ign) != 2 || !strings.Contains(strings.Join(ign, ";"), "2 started, 1 exited") {
		t.Fatalf("ignored activity not recorded: %v", ign)
	}
	ev := &Evidence{Layers: map[string]string{"offline": Passed, "environment-ready": Passed, "candidate-loaded": Passed, "lifecycle": Passed}}
	ev.StableDiff, ev.StableIgnored = before.Diff(after), ign
	ev.Finish()
	if ev.Status != Passed {
		t.Fatalf("status = %s", ev.Status)
	}
}

func TestStableEvidenceFailsAndListsChanges(t *testing.T) {
	cases := map[string]struct {
		mutate func(t *testing.T, s Stable, ids *[]string)
		want   string
	}{
		"config": {func(t *testing.T, s Stable, _ *[]string) {
			if err := os.WriteFile(filepath.Join(s.Root, "config", "config.yaml"), []byte("changed"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "stable config path modified"},
		"queue order":         {func(t *testing.T, s Stable, _ *[]string) { writeData(t, s, "queue.json", "[2,1]") }, `stable data path added: "queue.json"`},
		"controller identity": {func(t *testing.T, s Stable, ids *[]string) { *ids = []string{"10:controller"} }, "stable process identity changed"},
		"listener":            {func(t *testing.T, s Stable, _ *[]string) { s.Ports = nil }, ""},
	}
	delete(cases, "listener") // port change is covered by the existing listener test
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			stable := testStable(t)
			writeData(t, stable, "coordination/d/1.json", "record")
			ids := []string{"9:controller", "8:worker-manager"}
			probe := StableProbe{Processes: func() ([]string, error) { return ids, nil }}
			before, err := TakeStable(stable, probe)
			if err != nil {
				t.Fatal(err)
			}
			c.mutate(t, stable, &ids)
			after, err := TakeStable(stable, probe)
			if err != nil {
				t.Fatal(err)
			}
			d := before.Diff(after)
			if !strings.Contains(strings.Join(d, ";"), c.want) {
				t.Fatalf("diff %v lacks %q", d, c.want)
			}
			ev := &Evidence{Layers: map[string]string{"offline": Passed, "environment-ready": Passed, "candidate-loaded": Passed, "lifecycle": Passed}, StableDiff: d}
			ev.Finish()
			if ev.Status != Failed || !strings.Contains(ev.NextAction, c.want) {
				t.Fatalf("status=%s action=%q", ev.Status, ev.NextAction)
			}
		})
	}
}

func TestStableListenerChangeFailsEvidence(t *testing.T) {
	a := StableSnapshot{Listeners: map[int]bool{8093: true}}
	b := StableSnapshot{Listeners: map[int]bool{8093: false}}
	d := a.Diff(b)
	if len(d) != 1 || !strings.Contains(d[0], "port 8093") {
		t.Fatalf("diff = %v", d)
	}
	ev := &Evidence{Layers: map[string]string{"offline": Passed, "environment-ready": Passed, "candidate-loaded": Passed, "lifecycle": Passed}, StableDiff: d}
	ev.Finish()
	if ev.Status != Failed || !strings.Contains(ev.NextAction, "port 8093") {
		t.Fatalf("status=%s action=%q", ev.Status, ev.NextAction)
	}
}

func TestStableEvidencePassesOnPlanAndCoordinationWrites(t *testing.T) {
	stable := testStable(t)
	writeData(t, stable, "coordination/d/1.json", "record")
	before, err := TakeStable(stable, StableProbe{})
	if err != nil {
		t.Fatal(err)
	}
	writeData(t, stable, "acceptance-plans/new.json", "plan")
	writeData(t, stable, "coordination/d/1.json", "updated content")
	after, err := TakeStable(stable, StableProbe{})
	if err != nil {
		t.Fatal(err)
	}
	if d := before.Diff(after); len(d) != 0 {
		t.Fatalf("scheduler writes failed evidence: %v", d)
	}
	ign := strings.Join(before.Ignored(after), ";")
	if !strings.Contains(ign, "ignored") {
		t.Fatalf("activity not recorded: %q", ign)
	}
	ev := &Evidence{Layers: map[string]string{"offline": Passed, "environment-ready": Passed, "candidate-loaded": Passed, "lifecycle": Passed}, StableDiff: before.Diff(after), StableIgnored: before.Ignored(after)}
	ev.Finish()
	if ev.Status != Passed {
		t.Fatalf("status=%s", ev.Status)
	}
}

func TestParseProcesses(t *testing.T) {
	const ts = "Mon Jan  1 00:00:00 2026"
	line := func(pid, cmd string) string { return pid + " " + ts + " " + cmd }
	const root = "/Users/u/Library/Application Support/ClawFlow"
	out := strings.Join([]string{
		line("1", "/bin/zsh -c 'cd /x/clawflow-1 && go test ./... worker'"),
		line("2", "grep clawflow worker"),
		line("3", "git -C /x/clawflow log worker"),
		line("4", "/usr/bin/git -C /x/clawflow log worker"),
		line("5", root+"/bin/clawflow controller"),
		line("6", root+"/upgrade/artifacts/x/clawflow worker --coordinator http://x"),
		line("7", root+"/bin/clawflow worker execute --task 1"),
		line("8", "/private/tmp/x/clawflow worker --coordinator http://x"),
		line("9", "/var/folders/y/clawflow controller"),
		line("10", "/Users/u/src/clawflow/clawflow controller"),
		line("11", "clawflow controller"),
		line("12", "/private/tmp/x/clawflow worker execute --task 2"),
	}, "\n")
	got := parseProcesses(out, root, "", false)
	if len(got) != 2 || !strings.HasPrefix(got[0], "5:") || !strings.HasPrefix(got[1], "6:") {
		t.Fatalf("services = %v", got)
	}
	ex := parseProcesses(out, root, "", true)
	if len(ex) != 1 || !strings.HasPrefix(ex[0], "7:") {
		t.Fatalf("executors = %v", ex)
	}
	// Same pid and start but a different command line is a different identity.
	a := parseProcesses(line("5", root+"/bin/clawflow controller"), root, "", false)
	b := parseProcesses(line("5", root+"/bin/clawflow controller --x"), root, "", false)
	if a[0] == b[0] {
		t.Fatal("command line not part of identity")
	}
}
