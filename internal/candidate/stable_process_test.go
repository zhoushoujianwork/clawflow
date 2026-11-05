package candidate

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseProcessesIgnoresAppHelpers(t *testing.T) {
	const prefix = "  42 Thu Nov  6 05:00:00 2026 "
	out := strings.Join([]string{
		prefix + "/Applications/ClawFlow.app/Contents/Helpers/clawflow dashboard snapshot --url https://127.0.0.1:8093",
		prefix + "/Applications/ClawFlow.app/Contents/Helpers/clawflow worker --coordinator https://127.0.0.1:8093",
		prefix + "/Applications/ClawFlow.app/Contents/MacOS/ClawFlow",
		prefix + "/stable root/bin/clawflow controller",
	}, "\n")
	got := parseProcesses(out, "/stable root", "", false)
	if len(got) != 2 || !strings.Contains(got[0], "/stable root/bin/clawflow controller") && !strings.Contains(got[1], "/stable root/bin/clawflow controller") {
		t.Fatalf("services = %q", got)
	}
	for _, id := range got {
		if strings.Contains(id, "Helpers") {
			t.Fatalf("app helper counted as a service: %q", id)
		}
	}
}

func TestDiffNamesChangedProcesses(t *testing.T) {
	a := StableSnapshot{Processes: []string{"1:t:controller", "2:t:worker"}}
	b := StableSnapshot{Processes: []string{"3:t:controller", "2:t:worker"}}
	want := []string{"stable process identity changed", "stable process gone: 1:t:controller", "stable process added: 3:t:controller"}
	if got := a.Diff(b); !reflect.DeepEqual(got, want) {
		t.Fatalf("Diff = %q", got)
	}
}
