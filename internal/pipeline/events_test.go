package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLine(t *testing.T) {
	ev, ok := ParseLine(`2026-10-27T04:09:43Z INFO  run/automerge_sweep repo=o/r open_prs=1 err="a \"b\" c" token=abc123 path=/x/y`)
	if !ok {
		t.Fatal("not parsed")
	}
	if ev.Time != "2026-10-27T04:09:43Z" || ev.Level != "info" || ev.Event != "run/automerge_sweep" {
		t.Fatalf("head: %+v", ev)
	}
	want := map[string]string{"repo": "o/r", "open_prs": "1", "err": `a "b" c`, "token": "[redacted]", "path": "/x/y"}
	for k, v := range want {
		if ev.Fields[k] != v {
			t.Fatalf("field %s = %q, want %q (%v)", k, ev.Fields[k], v, ev.Fields)
		}
	}
}

func TestParseLineNoFieldsAndWarn(t *testing.T) {
	ev, ok := ParseLine("2026-10-27T04:09:43Z WARN  run/exit")
	if !ok || ev.Event != "run/exit" || ev.Level != "warn" || len(ev.Fields) != 0 {
		t.Fatalf("%+v %v", ev, ok)
	}
	ev, ok = ParseLine(`2026-10-27T04:09:43Z INFO  run/queue_blocked repo=o/r reason="sub issues\nunreadable" tail`)
	if !ok || ev.Fields["reason"] != "sub issues\nunreadable" {
		t.Fatalf("%+v %v", ev, ok)
	}
}

func TestParseLineRejectsGarbage(t *testing.T) {
	for _, l := range []string{"", "   ", "not a log line", "2026-10-27T04:09:43Z INFO", "yesterday INFO run/x a=b", "2026-10-27T04:09:43Z INFO nosuchslash a=b"} {
		if _, ok := ParseLine(l); ok {
			t.Fatalf("parsed %q", l)
		}
	}
}

func TestParseLineTruncatesLongValues(t *testing.T) {
	ev, _ := ParseLine("2026-10-27T04:09:43Z INFO  run/end err=" + strings.Repeat("x", 5000))
	if n := len([]rune(ev.Fields["err"])); n != maxFieldLen+1 {
		t.Fatalf("len=%d", n)
	}
}

func TestParseEventsFiltersAreas(t *testing.T) {
	log := strings.Join([]string{
		"2026-10-27T04:00:00Z INFO  run/start pid=1 version=v1",
		"2026-10-27T04:00:01Z INFO  run/lock pid=1 repo=o/r issue=1 op=implement",
		"2026-10-27T04:00:02Z INFO  run/harvest_skip repo=o/r reason=ci_not_success pr=3",
		"2026-10-27T04:00:03Z INFO  run/automerge_sweep repo=o/r merged=0",
		"2026-10-27T04:00:04Z INFO  run/queue_pick repo=o/r queue=9 issue=4 reason=head_eligible",
		"2026-10-27T04:00:05Z INFO  run/self_upgrade from=v1 to=v2",
		"2026-10-27T04:00:06Z INFO  run/target_skip repo=o/r issue=4 reason=issue_not_open",
		"2026-10-27T04:00:07Z INFO  run/exit pid=1",
		"web/something else entirely",
		"2026-10-27T04:00:08Z INFO  web/reconcile_tick fixed=0",
	}, "\n")
	got := ParseEvents(strings.NewReader(log))
	var names []string
	for _, e := range got {
		names = append(names, e.Event)
	}
	want := "run/harvest_skip run/automerge_sweep run/queue_pick run/self_upgrade run/target_skip run/exit"
	if strings.Join(names, " ") != want {
		t.Fatalf("events = %v", names)
	}
}

func TestLastDecision(t *testing.T) {
	evs := ParseEvents(strings.NewReader(strings.Join([]string{
		"2026-10-27T04:00:00Z INFO  run/queue_pick repo=o/r queue=9 issue=4 reason=head_eligible",
		"2026-10-27T04:01:00Z INFO  run/queue_wait repo=other/x queue=1 issue=8 reason=in_flight_locked",
		"2026-10-27T04:02:00Z INFO  run/queue_blocked repo=o/r queue=9 issue=5 reason=not_a_leaf",
	}, "\n")))
	d := LastDecision(evs, "o/r")
	if d == nil || d.Action != "blocked" || d.Issue != 5 || d.Reason != "not_a_leaf" || d.At != "2026-10-27T04:02:00Z" {
		t.Fatalf("%+v", d)
	}
	if d := LastDecision(evs, "other/x"); d == nil || d.Action != "wait" {
		t.Fatalf("%+v", d)
	}
	if LastDecision(evs, "none/none") != nil {
		t.Fatal("expected nil")
	}
}

func TestRelevantKeepsLast50ForTrackedRepos(t *testing.T) {
	var evs []Event
	for i := 0; i < 80; i++ {
		repo := "o/r"
		if i%4 == 0 {
			repo = "untracked/x"
		}
		evs = append(evs, Event{Time: fmt.Sprintf("t%03d", i), Event: "run/automerge_sweep", Fields: map[string]string{"repo": repo}})
	}
	evs = append(evs, Event{Time: "t999", Event: "run/exit", Fields: map[string]string{}})
	got := Relevant(evs, map[string]bool{"o/r": true})
	if len(got) != MaxEvents {
		t.Fatalf("len=%d", len(got))
	}
	if got[len(got)-1].Time != "t999" {
		t.Fatalf("newest must be last and repo-less events kept: %v", got[len(got)-1])
	}
	for _, e := range got {
		if e.Fields["repo"] == "untracked/x" {
			t.Fatalf("leaked untracked repo event %v", e)
		}
	}
	if got[0].Time >= got[1].Time {
		t.Fatalf("not oldest-first: %v %v", got[0].Time, got[1].Time)
	}
}

func TestReadEventsTail(t *testing.T) {
	if evs, err := ReadEventsTail(filepath.Join(t.TempDir(), "missing.log"), 1024); err != nil || evs != nil {
		t.Fatalf("missing file: %v %v", evs, err)
	}
	path := filepath.Join(t.TempDir(), "run.log")
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&sb, "2026-10-27T04:00:00Z INFO  run/automerge_sweep repo=o/r n=%d\n", i)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	evs, err := ReadEventsTail(path, 1000)
	if err != nil || len(evs) == 0 || len(evs) >= 200 {
		t.Fatalf("tail: %d %v", len(evs), err)
	}
	if evs[len(evs)-1].Fields["n"] != "199" {
		t.Fatalf("last = %v", evs[len(evs)-1])
	}
}

func TestLockTimeoutIsBoardEvent(t *testing.T) {
	events := ParseEvents(strings.NewReader("2026-10-27T04:00:00Z WARN run/lock_timeout repo=o/r reason=busy"))
	if len(events) != 1 || events[0].Event != "run/lock_timeout" {
		t.Fatalf("events=%+v", events)
	}
}
