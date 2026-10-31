package pilot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/store"
	"github.com/zhoushoujianwork/clawflow/internal/store/storetest"
)

func TestScheduleActionSchemaAndBudget(t *testing.T) {
	good := `{"id":"add","type":"schedule_add","schedule_id":"morning","schedule":{"cron":"0 9 * * *","timezone":"Asia/Shanghai","prompt":"summarize","ops":2,"timeout_seconds":60,"enabled":true}}`
	if _, err := validateAction(json.RawMessage(good)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(good, `"ops":2`, `"ops":0`, 1),
		strings.Replace(good, `"timezone":"Asia/Shanghai"`, `"timezone":"invalid"`, 1),
		strings.Replace(good, `"enabled":true`, `"enabled":true,"failures":0`, 1),
		strings.Replace(good, `"enabled":true`, `"enabled":true,"owner":"someone"`, 1),
		strings.Replace(good, `"schedule_id":"morning"`, `"schedule_id":"../morning"`, 1),
	} {
		if _, err := validateAction(json.RawMessage(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	e, _ := testExecutor(t)
	e.MaxWake = 1
	results, err := e.Apply(actionSet("check-budget", good, strings.Replace(good, `"id":"add"`, `"id":"another"`, 1)), true)
	if err != nil || len(results) != 2 || results[0].Outcome != "valid" || results[1].Outcome != "wake_limit" {
		t.Fatalf("%+v %v", results, err)
	}
}

func TestStewardScheduleActions(t *testing.T) {
	ctx := context.Background()
	e, _ := testExecutor(t)
	s, err := store.EnsureReady(ctx, &config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e.Config = &config.Config{}
	e.DefaultRepo = ""
	spec := `"schedule":{"cron":"0 9 * * *","timezone":"Asia/Shanghai","prompt":"summarize","ops":2,"timeout_seconds":60,"enabled":true}`
	add := `{"id":"add","type":"schedule_add","schedule_id":"morning",` + spec + `}`
	update := `{"id":"update","type":"schedule_update","schedule_id":"morning",` + strings.Replace(spec, "summarize", "updated", 1) + `}`
	for _, raw := range []string{add, update} {
		results, err := e.Apply(actionSet("manage", raw), false)
		if err != nil || len(results) != 1 || results[0].Outcome != "applied" {
			t.Fatalf("%+v %v", results, err)
		}
	}
	j, err := s.ScheduledCheck(ctx, "morning")
	if err != nil || j.Prompt != "updated" || j.Owner != "pilot" {
		t.Fatalf("%+v %v", j, err)
	}
	results, err := e.Apply(actionSet("manage", add), false)
	if err != nil || results[0].Outcome != "already_applied" {
		t.Fatalf("%+v %v", results, err)
	}
	results, err = e.Apply(actionSet("manage", `{"id":"remove","type":"schedule_remove","schedule_id":"morning"}`), false)
	if err != nil || results[0].Outcome != "applied" {
		t.Fatalf("%+v %v", results, err)
	}
	jobs, err := s.ScheduledChecks(ctx)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("%+v %v", jobs, err)
	}
	audit, err := ReadActionRecords(e.Path)
	if err != nil || len(audit) < 6 {
		t.Fatalf("audit=%d %v", len(audit), err)
	}
}

func TestScheduledInvocationRecordsResult(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.Migrate(ctx, store.Migrations); err != nil {
		t.Fatal(err)
	}
	j := store.ScheduledCheck{ID: "check", Cron: "* * * * *", Timezone: "UTC", Prompt: "only summarize", Ops: 3, TimeoutSeconds: 45, Owner: "owner", Enabled: true}
	if err := s.SaveScheduledCheck(ctx, &j, true); err != nil {
		t.Fatal(err)
	}
	runs, err := s.ClaimScheduledChecks(ctx, time.Now().UTC())
	if err != nil || len(runs) != 1 {
		t.Fatalf("%+v %v", runs, err)
	}
	// Changing the definition after admission cannot change the claimed budget.
	j.Ops = 100
	if err := s.SaveScheduledCheck(ctx, &j, false); err != nil {
		t.Fatal(err)
	}
	runScheduledCheck(ctx, s, runs[0], func(_ context.Context, prompt string, ops int, timeout time.Duration) (StewardResult, error) {
		if prompt != "only summarize" || ops != 3 || timeout != 45*time.Second {
			t.Fatalf("unexpected invocation %s %d %s", prompt, ops, timeout)
		}
		return StewardResult{Answer: "nothing stuck", Actions: []ActionRecord{{Outcome: "applied"}}}, nil
	})
	rows, err := s.ScheduledCheckRuns(ctx, j.ID)
	if err != nil || len(rows) != 1 || rows[0].Status != "success" || rows[0].Summary != "nothing stuck" || len(rows[0].Actions) == 0 {
		t.Fatalf("%+v %v", rows, err)
	}
	if string(rows[0].Usage) != "null" {
		t.Fatalf("invented usage: %s", rows[0].Usage)
	}
}
