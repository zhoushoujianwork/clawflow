package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type queueLabelFake struct {
	vcs.Client
	calls []string
	err   error
}

func (f *queueLabelFake) AddLabel(repo string, n int, labels ...string) error {
	f.calls = append(f.calls, fmt.Sprintf("add %s#%d %s", repo, n, strings.Join(labels, ",")))
	return f.err
}
func (f *queueLabelFake) RemoveLabel(repo string, n int, labels ...string) error {
	f.calls = append(f.calls, fmt.Sprintf("remove %s#%d %s", repo, n, strings.Join(labels, ",")))
	return f.err
}
func mockQueueLabels(t *testing.T) *queueLabelFake {
	f := &queueLabelFake{}
	old := queueEnrollmentClient
	queueEnrollmentClient = func(repo string) (vcs.Client, string, config.Repo, error) { return f, repo, config.Repo{}, nil }
	t.Cleanup(func() { queueEnrollmentClient = old })
	return f
}
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
		{Number: 1, Title: "old", Labeled: true, Priority: "P2", Rank: -1, State: "eligible", Created: "2026-01-01T00:00:00Z"},
		{Number: 2, Title: "urgent", Labeled: true, Priority: "P0", Rank: -1, State: "in_flight", Created: "2026-01-02T00:00:00Z"},
		{Number: 3, Title: "stuck", Labeled: true, Priority: "P2", Rank: -1, State: "blocked", Reason: "label_agent-failed", Created: "2026-01-03T00:00:00Z"},
	}}))
	must(queue.SaveState(queue.RepoState{Repo: "g/sub/p", Entries: []queue.EntryRecord{
		{Number: 4, Title: "gl", Labeled: true, Priority: "P2", Rank: -1, State: "waiting", Reason: "dependency", Created: "2025-12-01T00:00:00Z"},
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

func TestQueueAddAndRemoveCommands(t *testing.T) {
	f := mockQueueLabels(t)
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
	if len(f.calls) != len(steps) || f.calls[0] != "add o/r#1 clawflow" || f.calls[5] != "remove o/r#5 clawflow" {
		t.Fatalf("label calls: %v", f.calls)
	}
	if _, err := runQueueCmd(t, "remove", "o/r#99"); err != nil {
		t.Fatal(err)
	}
	// Adding an already ranked key repositions it without duplicating.
	if _, err := runQueueCmd(t, "add", "o/r#4"); err != nil || rankStrings(t) != "o/r#3,o/r#1,g/sub/p#2,o/r#4" {
		t.Fatalf("re-add: %s %v", rankStrings(t), err)
	}
	for _, bad := range [][]string{
		{"add", "x/unknown#1"},
		{"add", "nonsense"},
		{"add", "o/r#9", "--top", "--before", "o/r#1"},

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

func TestQueueLabelFailurePreservesRank(t *testing.T) {
	queueCmdSetup(t)
	f := mockQueueLabels(t)
	if _, err := runQueueCmd(t, "move", "o/r#1", "--top"); err != nil {
		t.Fatal(err)
	}
	f.err = fmt.Errorf("provider unavailable")
	for _, args := range [][]string{{"add", "o/r#2"}, {"remove", "o/r#1"}} {
		if _, err := runQueueCmd(t, args...); err == nil {
			t.Fatal("expected label error")
		}
		if got := rankStrings(t); got != "o/r#1" {
			t.Fatalf("rank changed: %s", got)
		}
	}
}

func TestQueueListNotEnrolledCount(t *testing.T) {
	queueCmdSetup(t)
	for repo, count := range map[string]int{"o/r": 2, "g/sub/p": 3} {
		if err := queue.SaveState(queue.RepoState{Repo: repo, NotEnrolled: count}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ repo, want string }{{"", "o/r: not enrolled: 2"}, {"o/r", "o/r: not enrolled: 2"}} {
		out, err := runQueueCmd(t, "list", "--repo", tc.repo)
		if tc.repo == "" && !strings.Contains(out, "g/sub/p: not enrolled: 3") {
			t.Fatalf("missing per-repo count: %s", out)
		}
		if tc.repo != "" && strings.Contains(out, "g/sub/p") {
			t.Fatalf("unexpected repo: %s", out)
		}
		if err != nil || !strings.Contains(out, tc.want) {
			t.Fatalf("output %q, error %v", out, err)
		}
	}
}

func TestQueueListOnlyEnrolledCandidates(t *testing.T) {
	queueCmdSetup(t)
	if err := queue.SaveState(queue.RepoState{Repo: "o/r", Entries: []queue.EntryRecord{
		{Number: 1, Priority: "P0", State: "eligible"},
		{Number: 2, Priority: "P2", State: "eligible", Labeled: true},
		{Number: 3, Priority: "P2", State: "in_flight"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runQueueCmd(t, "move", "o/r#1", "--top"); err != nil {
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
	if len(rows) != 2 || rows[0].Number != 2 || rows[1].Number != 3 {
		t.Fatalf("rows=%+v", rows)
	}
	table, err := runQueueCmd(t, "list")
	if err != nil || strings.Contains(table, "SCOPE") {
		t.Fatalf("table=%q err=%v", table, err)
	}
}

func TestRetiredQueueCommandsAbsent(t *testing.T) {
	cmd := newRepoSetCmd()
	for _, flag := range []string{"queue" + "-issue", "queue" + "-scope"} {
		if cmd.Flags().Lookup(flag) != nil {
			t.Fatalf("retired flag %q still registered", flag)
		}
	}
	for _, child := range NewQueueCmd().Commands() {
		if child.Name() == "import" {
			t.Fatal("retired import command registered")
		}
	}
}
