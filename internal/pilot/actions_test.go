package pilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type actionClient struct {
	vcs.Client
	issue vcs.Issue
	calls []string
}

func (c *actionClient) GetIssue(string, int) (*vcs.Issue, error) { return &c.issue, nil }
func (c *actionClient) PostIssueCommentIdempotent(repo string, n int, body, id string) error {
	c.calls = append(c.calls, "comment:"+body)
	return nil
}
func (c *actionClient) RemoveLabel(repo string, n int, labels ...string) error {
	c.calls = append(c.calls, "remove:"+strings.Join(labels, ","))
	return nil
}
func (c *actionClient) AddLabel(repo string, n int, labels ...string) error {
	c.calls = append(c.calls, "add:"+strings.Join(labels, ","))
	return nil
}
func (c *actionClient) UpdateIssue(repo string, n int, u vcs.IssueUpdate) (vcs.Issue, error) {
	c.issue.Body = *u.Body
	c.calls = append(c.calls, "edit")
	return c.issue, nil
}
func (c *actionClient) CreateIssue(repo, title, body string) (vcs.Issue, error) {
	c.calls = append(c.calls, "create")
	return vcs.Issue{Number: 42}, nil
}
func testExecutor(t *testing.T) (ActionExecutor, *actionClient) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	c := &actionClient{issue: vcs.Issue{Number: 1, State: "open", Body: "original", Labels: []string{"agent-failed", "agent-skipped"}}}
	e := ActionExecutor{Path: filepath.Join(t.TempDir(), "audit.jsonl"), Actor: "pilot", DefaultRepo: "o/r", Repos: []string{"o/r"}, Now: func() time.Time { return time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC) }, Client: func(string) (vcs.Client, error) { return c, nil }}
	return e, c
}
func actionSet(run string, actions ...string) ActionSet {
	s := ActionSet{RunID: run, Actions: []json.RawMessage{}}
	for _, a := range actions {
		s.Actions = append(s.Actions, json.RawMessage(a))
	}
	return s
}
func TestActionsParserAndSchema(t *testing.T) {
	good := `{"run_id":"wake","actions":[]}`
	s, err := ParseActions("```pilot-actions\ninvalid\n```\n```pilot-actions\n" + good + "\n```")
	if err != nil || s.RunID != "wake" {
		t.Fatalf("%+v %v", s, err)
	}
	for _, text := range []string{"none", "```pilot-actions\n" + good, "```pilot-actions\n" + good + "\n```\n```pilot-actions\nbad\n```"} {
		if _, err := ParseActions(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	for _, raw := range []string{`{"run_id":"a","actions":[],"extra":1}`, `{"run_id":"a","actions":[]} {}`, `{"run_id":"../a","actions":[]}`, `{"run_id":"a","actions":null}`} {
		if _, err := DecodeActions([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{
		`{"id":"a","type":"shell"}`, `{"id":"a","type":"restart_service"}`,
		`{"id":"a","type":"label_add","issue":1,"label":"agent-implemented"}`,
		`{"id":"a","type":"comment","issue":0,"body":"x"}`,
		`{"id":"a","type":"note","body":"x","issue":0}`,
		`{"id":"a","type":"create_issue","title":"x"}`,
		`{"id":"a","type":"edit_spec","issue":1,"section":"../x","body":"x"}`,
		`{"id":"a","type":"queue_move","issue":1,"position":"bad"}`,
		`{"id":"a","type":"note","body":"x","unknown":true}`} {
		if _, err := validateAction([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, kind := range []string{"rerun_ci", "override_merge"} {
		if _, err := validateAction([]byte(`{"id":"a","type":"` + kind + `"}`)); err == nil || err.Error() != "not_implemented" {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}
func TestActionsAuditLimitsIdempotence(t *testing.T) {
	e, c := testExecutor(t)
	e.MaxWake = 2
	e.MaxDay = 3
	s := actionSet("wake", `{"id":"a","type":"comment","issue":1,"body":"why"}`, `{"id":"b","type":"release","issue":1,"body":"fixed"}`, `{"id":"c","type":"note","body":"extra"}`)
	r, err := e.Apply(s, false)
	if err != nil {
		t.Fatal(err)
	}
	if r[0].Outcome != "applied" || r[1].Outcome != "applied" || r[2].Outcome != "wake_limit" {
		t.Fatalf("%+v", r)
	}
	if strings.Join(c.calls, "|") != "comment:why|comment:fixed|remove:agent-failed|remove:agent-skipped" {
		t.Fatal(c.calls)
	}
	count := len(c.calls)
	r, err = e.Apply(s, false)
	if err != nil || r[0].Outcome != "already_applied" || len(c.calls) != count {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = e.Apply(actionSet("next", `{"id":"a","type":"note","body":"one"}`, `{"id":"b","type":"note","body":"two"}`), false)
	if err != nil || r[1].Outcome != "day_limit" {
		t.Fatalf("%+v %v", r, err)
	}
	records, err := ReadActionRecords(e.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) < 8 || records[0].Outcome != "pending" || records[0].Actor != "pilot" || records[0].RunID != "wake" {
		t.Fatal(records)
	}
	logs, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".clawflow/logs/pilot.log"))
	if err != nil || !strings.Contains(string(logs), "pilot/action") {
		t.Fatalf("%s %v", logs, err)
	}
}
func TestActionEffectsAndDryRun(t *testing.T) {
	e, c := testExecutor(t)
	e.MaxWake = 20
	e.MaxDay = 30
	s := actionSet("wake", `{"id":"edit","type":"edit_spec","issue":1,"section":"Decided","body":"decision"}`, `{"id":"create","type":"create_issue","title":"new","body":"spec"}`, `{"id":"label","type":"label_add","issue":1,"label":"bug"}`, `{"id":"human","type":"ask_human","issue":1,"body":"question"}`, `{"id":"add","type":"queue_add","issue":1}`)
	r, err := e.Apply(s, true)
	if err != nil || len(c.calls) != 0 || r[0].Outcome != "valid" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := os.Stat(e.Path); !os.IsNotExist(err) {
		t.Fatalf("dry run created audit: %v", err)
	}
	if _, err := os.Stat(e.Path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("dry run lock")
	}
	r, err = e.Apply(s, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range r {
		if v.Outcome != "applied" {
			t.Fatal(v)
		}
	}
	if r[1].CreatedIssue != 42 || !strings.Contains(c.issue.Body, "decision") || !strings.HasPrefix(c.issue.Body, "original") {
		t.Fatalf("%+v %s", r, c.issue.Body)
	}
	rank, err := queue.LoadRank()
	if err != nil || len(rank) != 1 || rank[0].Number != 1 {
		t.Fatalf("%+v %v", rank, err)
	}
	_, err = e.Apply(actionSet("wake2", `{"id":"remove","type":"queue_remove","issue":1}`, `{"id":"move","type":"queue_move","issue":1,"position":"top"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := replaceSpec(c.issue.Body, "Decided", "new")
	if err != nil || strings.Contains(updated, "decision") || strings.Count(updated, "new") != 1 {
		t.Fatalf("%s %v", updated, err)
	}
	if _, err := replaceSpec("<!-- clawflow:spec:x -->", "x", "new"); err == nil {
		t.Fatal("unpaired markers accepted")
	}
}
func TestActionsRejectionsAndUncertainAttempt(t *testing.T) {
	e, c := testExecutor(t)
	r, err := e.Apply(actionSet("wake", `{"id":"a","type":"override_merge"}`, `{"id":"b","type":"note","body":"x","repo":"other/repo"}`, `{"id":"c","type":"label_remove","issue":1,"label":"agent-failed"}`), false)
	if err != nil || r[0].Outcome != "not_implemented" || r[1].Outcome != "repo_not_allowed" || r[2].Outcome != "label_not_allowed" || len(c.calls) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	c.issue.Labels = []string{"agent-running"}
	s := actionSet("blocked", `{"id":"a","type":"comment","issue":1,"body":"x"}`)
	r, err = e.Apply(s, false)
	if err != nil || r[0].Outcome != "failed" {
		t.Fatalf("%+v %v", r, err)
	}
	c.issue.Labels = nil
	r, err = e.Apply(s, false)
	if err != nil || r[0].Outcome != "uncertain_previous_attempt" || len(c.calls) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	s.Actions[0] = json.RawMessage(`{"id":"a","type":"comment","issue":1,"body":"different"}`)
	r, err = e.Apply(s, false)
	if err != nil || r[0].Outcome != "id_conflict" {
		t.Fatalf("%+v %v", r, err)
	}
}
func TestConcurrentActionApplication(t *testing.T) {
	e, c := testExecutor(t)
	s := actionSet("wake", `{"id":"a","type":"comment","issue":1,"body":"x"}`)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.Apply(s, false); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(c.calls) != 1 {
		t.Fatal(c.calls)
	}
}
func TestCreateLimitAndDayReset(t *testing.T) {
	e, _ := testExecutor(t)
	e.MaxDay = 2
	s := actionSet("wake", `{"id":"a","type":"create_issue","title":"a","body":"x"}`, `{"id":"b","type":"create_issue","title":"b","body":"x"}`, `{"id":"c","type":"create_issue","title":"c","body":"x"}`)
	e.MaxDay = 20
	r, err := e.Apply(s, false)
	if err != nil || r[2].Outcome != "create_limit" {
		t.Fatalf("%+v %v", r, err)
	}
	e.MaxDay = 2
	e.Now = func() time.Time { return time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC) }
	r, err = e.Apply(actionSet("next", `{"id":"a","type":"note","body":"x"}`), false)
	if err != nil || r[0].Outcome != "applied" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestPendingAuditPreventsReplayAfterCrash(t *testing.T) {
	e, c := testExecutor(t)
	s := actionSet("crash", `{"id":"a","type":"create_issue","title":"new","body":"spec"}`)
	if _, err := e.Apply(s, false); err != nil {
		t.Fatal(err)
	}
	records, err := ReadActionRecords(e.Path)
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := json.Marshal(records[0])
	if err := os.WriteFile(e.Path, append(pending, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := e.Apply(s, false)
	if err != nil || r[0].Outcome != "uncertain_previous_attempt" || len(c.calls) != 1 {
		t.Fatalf("%+v %v calls=%v", r, err, c.calls)
	}
}
