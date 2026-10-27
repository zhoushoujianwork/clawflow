package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func queueCmdSetup(t *testing.T) {
	t.Helper()
	writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{
		"o/r": {Platform: "github", Enabled: true}, "g/sub/p": {Platform: "gitlab", Enabled: true},
	}})
}

func runQueueCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewQueueCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func rankStrings(t *testing.T) string {
	t.Helper()
	order, err := queue.LoadRank()
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, k := range order {
		s = append(s, k.String())
	}
	return strings.Join(s, ",")
}

func TestQueueMoveCommand(t *testing.T) {
	queueCmdSetup(t)
	steps := []struct {
		args []string
		want string
	}{
		{[]string{"move", "o/r#3", "--top"}, "o/r#3"},
		{[]string{"move", "g/sub/p#9", "--bottom"}, "o/r#3,g/sub/p#9"},
		{[]string{"move", "o/r#5", "--before", "g/sub/p#9"}, "o/r#3,o/r#5,g/sub/p#9"},
		{[]string{"move", "o/r#3", "--after", "o/r#5"}, "o/r#5,o/r#3,g/sub/p#9"},
		{[]string{"move", "g/sub/p#9", "--top"}, "g/sub/p#9,o/r#5,o/r#3"},
	}
	for _, s := range steps {
		if _, err := runQueueCmd(t, s.args...); err != nil {
			t.Fatalf("%v: %v", s.args, err)
		}
		if got := rankStrings(t); got != s.want {
			t.Fatalf("%v: rank = %s, want %s", s.args, got, s.want)
		}
	}
	for _, bad := range [][]string{
		{"move", "o/r#3"},
		{"move", "o/r#3", "--top", "--bottom"},
		{"move", "nonsense", "--top"},
		{"move", "x/unknown#1", "--top"},
		{"move", "o/r#3", "--before", "bad"},
		{"move", "o/r#3", "--before", "o/r#3"},
	} {
		before := rankStrings(t)
		if _, err := runQueueCmd(t, bad...); err == nil {
			t.Errorf("%v should fail", bad)
		}
		if rankStrings(t) != before {
			t.Errorf("%v must not change the rank", bad)
		}
	}
}

func TestQueueListCommand(t *testing.T) {
	queueCmdSetup(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(queue.SaveState(queue.RepoState{Repo: "o/r", Entries: []queue.EntryRecord{
		{Number: 1, Title: "old", Priority: "P2", Rank: -1, State: "eligible", Created: "2026-01-01T00:00:00Z"},
		{Number: 2, Title: "urgent", Priority: "P0", Rank: -1, State: "in_flight", Created: "2026-01-02T00:00:00Z"},
		{Number: 3, Title: "stuck", Priority: "P2", Rank: -1, State: "blocked", Reason: "label_agent-failed", Created: "2026-01-03T00:00:00Z"},
	}}))
	must(queue.SaveState(queue.RepoState{Repo: "g/sub/p", Entries: []queue.EntryRecord{
		{Number: 4, Title: "gl", Priority: "P2", Rank: -1, State: "waiting", Reason: "dependency", Created: "2025-12-01T00:00:00Z"},
	}}))
	_, err := runQueueCmd(t, "move", "o/r#3", "--top")
	must(err)

	out, err := runQueueCmd(t, "list", "--json")
	must(err)
	var rows []queue.ListEntry
	must(json.Unmarshal([]byte(out), &rows))
	var keys []string
	for _, r := range rows {
		keys = append(keys, r.Key)
	}
	if strings.Join(keys, ",") != "o/r#2,o/r#3,g/sub/p#4,o/r#1" {
		t.Fatalf("order = %v", keys)
	}
	if rows[1].Rank != 1 || rows[1].State != "blocked:label_agent-failed" || rows[2].State != "waiting:dependency" || rows[0].Tier != "P0" {
		t.Fatalf("rows = %+v", rows)
	}

	out, err = runQueueCmd(t, "list", "--repo", "g/sub/p")
	must(err)
	if !strings.Contains(out, "g/sub/p#4") || strings.Contains(out, "o/r#1") || !strings.Contains(out, "waiting:dependency") {
		t.Fatalf("filtered table:\n%s", out)
	}
	if out, err = runQueueCmd(t, "list", "--repo", "none/none", "--json"); err != nil || strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty json = %q %v", out, err)
	}
}

func TestQueueImportOrderAppendsAndSkipsDuplicates(t *testing.T) {
	queueCmdSetup(t)
	if _, err := runQueueCmd(t, "move", "o/r#7", "--top"); err != nil {
		t.Fatal(err)
	}
	lister := &fakeQueueLister{subs: []vcs.Issue{{Number: 5, State: "open"}, {Number: 7, State: "open"}, {Number: 6, State: "closed"}, {Number: 4, State: "open"}}}
	added, err := importQueueIssue(lister, "o/r", 100)
	if err != nil || len(added) != 2 {
		t.Fatalf("added = %v %v", added, err)
	}
	if got := rankStrings(t); got != "o/r#7,o/r#5,o/r#4" {
		t.Fatalf("rank = %s", got)
	}
	if added, err = importQueueIssue(lister, "o/r", 100); err != nil || len(added) != 0 {
		t.Fatalf("re-import must add nothing: %v %v", added, err)
	}
	if _, err := importQueueIssue(&fakeQueueLister{err: vcs.ErrNotSupported}, "g/sub/p", 1); err == nil {
		t.Fatal("unsupported platform must error for the explicit command")
	}
}

func TestQueueAddAndRemoveCommands(t *testing.T) {
	queueCmdSetup(t)
	steps := []struct {
		args []string
		want string
	}{
		{[]string{"add", "o/r#1"}, "o/r#1"},
		{[]string{"add", "g/sub/p#2"}, "o/r#1,g/sub/p#2"},
		{[]string{"add", "o/r#3", "--top"}, "o/r#3,o/r#1,g/sub/p#2"},
		{[]string{"add", "o/r#4", "--after", "o/r#1"}, "o/r#3,o/r#1,o/r#4,g/sub/p#2"},
		{[]string{"add", "o/r#5", "--before", "o/r#1"}, "o/r#3,o/r#5,o/r#1,o/r#4,g/sub/p#2"},
		{[]string{"remove", "o/r#5"}, "o/r#3,o/r#1,o/r#4,g/sub/p#2"},
	}
	for _, s := range steps {
		if _, err := runQueueCmd(t, s.args...); err != nil {
			t.Fatalf("%v: %v", s.args, err)
		}
		if got := rankStrings(t); got != s.want {
			t.Fatalf("%v: rank = %s, want %s", s.args, got, s.want)
		}
	}
	// Adding an already ranked key repositions it without duplicating.
	if _, err := runQueueCmd(t, "add", "o/r#4"); err != nil || rankStrings(t) != "o/r#3,o/r#1,g/sub/p#2,o/r#4" {
		t.Fatalf("re-add: %s %v", rankStrings(t), err)
	}
	for _, bad := range [][]string{
		{"add", "x/unknown#1"},
		{"add", "nonsense"},
		{"add", "o/r#9", "--top", "--before", "o/r#1"},
		{"remove", "o/r#99"},
		{"remove", "bad"},
	} {
		before := rankStrings(t)
		if _, err := runQueueCmd(t, bad...); err == nil {
			t.Errorf("%v should fail", bad)
		}
		if rankStrings(t) != before {
			t.Errorf("%v must not change the rank", bad)
		}
	}
}

func TestQueueListShowsScopeAndHidesOutOfScope(t *testing.T) {
	writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{
		"o/r": {Platform: "github", Enabled: true, QueueScope: "ranked"},
		"o/l": {Platform: "github", Enabled: true, QueueScope: "labeled"},
		"o/a": {Platform: "github", Enabled: true},
	}})
	entries := []queue.EntryRecord{
		{Number: 1, Priority: "P2", Rank: -1, State: "eligible", Created: "2026-01-01T00:00:00Z"},
		{Number: 2, Priority: "P2", Rank: -1, State: "eligible", Created: "2026-01-02T00:00:00Z", Labeled: true},
		{Number: 3, Priority: "P2", Rank: -1, State: "in_flight", Created: "2026-01-03T00:00:00Z"},
	}
	for _, repo := range []string{"o/r", "o/l", "o/a"} {
		if err := queue.SaveState(queue.RepoState{Repo: repo, Entries: entries}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runQueueCmd(t, "add", "o/r#1"); err != nil {
		t.Fatal(err)
	}
	out, err := runQueueCmd(t, "list", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []queue.ListEntry
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.Key] = r.Scope
	}
	want := map[string]string{
		"o/r#1": "ranked", "o/r#2": "ranked", "o/r#3": "ranked", // ranked or labeled or in flight
		"o/l#2": "labeled", "o/l#3": "labeled", // unlabeled #1 hidden
		"o/a#1": "all", "o/a#2": "all", "o/a#3": "all",
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s scope = %q, want %q (rows %v)", k, got[k], v, got)
		}
	}
	if tbl, _ := runQueueCmd(t, "list"); !strings.Contains(tbl, "SCOPE") {
		t.Fatalf("table lacks scope column:\n%s", tbl)
	}
}
