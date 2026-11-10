package coordinator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func enqueueKind(t *testing.T, s *Store, kind string, issue, pr int, payload string, tags ...string) *Task {
	t.Helper()
	task, err := s.Enqueue(ctx, Task{Kind: kind, Repo: "owner/repo", Issue: issue, PR: pr, Operator: kind, Tags: tags, Payload: json.RawMessage(payload)})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestJobStatusesAcrossLifecycle(t *testing.T) {
	s, _, _ := setup(t)
	register(t, s, "w1", 4)
	queued := enqueueKind(t, s, "implement", 1, 0, `{"title":"first"}`)
	got, err := s.Jobs(ctx, JobFilter{})
	if err != nil || len(got) != 1 || got[0].Status != JobQueued || got[0].Worker != "" || got[0].StartedAt != nil || got[0].Stage != "implement" || got[0].Trigger != "issue_queued" {
		t.Fatalf("queued: %+v err=%v", got, err)
	}

	task := claim(t, s, "w1")
	got, _ = s.Jobs(ctx, JobFilter{})
	if got[0].Status != JobRunning || got[0].Worker != "w1" || got[0].StartedAt == nil || got[0].EndedAt != nil || got[0].ElapsedMs != nil {
		t.Fatalf("running: %+v", got[0])
	}

	result := `{"status":"success","outcome":"agent-implemented","pr_number":7,"pr_url":"https://github.com/owner/repo/pull/7","branch":"fix/issue-1","head_sha":"abc123","output":"done"}`
	if err := s.Complete(ctx, task.ID, "w1", task.FencingToken, json.RawMessage(result)); err != nil {
		t.Fatal(err)
	}
	d, err := s.Job(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != JobSuccess || d.EndedAt == nil || d.ElapsedMs == nil || *d.ElapsedMs < 0 || d.Worker != "w1" {
		t.Fatalf("done: %+v", d.Job)
	}
	if d.Artifacts.PRNumber != 7 || d.Artifacts.PRURL != "https://github.com/owner/repo/pull/7" || d.Artifacts.Branch != "fix/issue-1" || d.Artifacts.HeadSHA != "abc123" || d.Summary != "done" {
		t.Fatalf("artifacts: %+v summary=%q", d.Artifacts, d.Summary)
	}
	if len(d.Attempts) != 1 || d.Attempts[0].Status != "done" || d.Attempts[0].Worker != "w1" {
		t.Fatalf("attempts: %+v", d.Attempts)
	}
	events := []string{}
	for _, e := range d.Timeline {
		events = append(events, e.Event)
	}
	if strings.Join(events[:3], ",") != "queued,started,attempt_done" {
		t.Fatalf("timeline: %v", events)
	}
	for i := 1; i < len(d.Timeline); i++ {
		if d.Timeline[i].At.Before(d.Timeline[i-1].At) {
			t.Fatalf("timeline not ordered: %+v", d.Timeline)
		}
	}
}

func TestJobFailedAndCancelled(t *testing.T) {
	s, _, _ := setup(t)
	register(t, s, "w1", 4)
	enqueueKind(t, s, "implement", 1, 0, `{}`)
	task := claim(t, s, "w1")
	if err := s.Fail(ctx, task.ID, "w1", task.FencingToken, json.RawMessage(`{"status":"failed","error":"tests failed","infra_kind":""}`)); err != nil {
		t.Fatal(err)
	}
	d, _ := s.Job(ctx, task.ID)
	if d.Status != JobFailed || d.Error != "tests failed" || d.Outcome != "failed" {
		t.Fatalf("failed: %+v err=%q", d.Job, d.Error)
	}

	other := enqueueKind(t, s, "reply", 2, 0, `{}`)
	if err := s.db.Exec("UPDATE coordinator_tasks SET state='cancelled' WHERE id=?", other.ID).Error; err != nil {
		t.Fatal(err)
	}
	jobs, _ := s.Jobs(ctx, JobFilter{Status: JobCancelled})
	if len(jobs) != 1 || jobs[0].ID != other.ID || jobs[0].Status != JobCancelled {
		t.Fatalf("cancelled: %+v", jobs)
	}
	if _, err := s.Jobs(ctx, JobFilter{Status: "bogus"}); err == nil {
		t.Fatal("bogus status accepted")
	}
}

func TestJobStageAndTriggerByKind(t *testing.T) {
	s, _, _ := setup(t)
	enqueueKind(t, s, "repair", 3, 30, `{"repair":{"reason":"ci"}}`)
	enqueueKind(t, s, "repair", 4, 40, `{"repair":{"reason":"acceptance"}}`)
	enqueueKind(t, s, "repair", 5, 50, `{"repair":{"reason":"merge_conflict"}}`)
	enqueueKind(t, s, "acceptance", 6, 60, `{}`)
	enqueueKind(t, s, "implement", 7, 0, `{"reopen_event_id":"evt-1"}`)
	enqueueKind(t, s, "reply", 8, 0, `{}`)
	byIssue := map[int]Job{}
	jobs, _ := s.Jobs(ctx, JobFilter{})
	for _, j := range jobs {
		byIssue[j.Issue] = j
	}
	if byIssue[8].Operator != "reply" {
		t.Errorf("operator = %q", byIssue[8].Operator)
	}
	for issue, want := range map[int][2]string{
		3: {"repair-ci", "ci_failed"}, 4: {"repair-acceptance", "acceptance_failed"}, 5: {"repair-conflict", "merge_conflict"},
		6: {"acceptance", "pr_ready_for_review"}, 7: {"implement", "issue_reopened"}, 8: {"reply", "operator"},
	} {
		if j := byIssue[issue]; j.Stage != want[0] || j.Trigger != want[1] {
			t.Errorf("issue %d: stage=%s trigger=%s want %v", issue, j.Stage, j.Trigger, want)
		}
	}
}

func TestJobFiltersAndLimit(t *testing.T) {
	s, _, _ := setup(t)
	for i := 1; i <= 5; i++ {
		enqueueKind(t, s, "implement", i, 0, `{}`)
	}
	if _, err := s.Enqueue(ctx, Task{Kind: "implement", Repo: "other/repo", Issue: 1, Operator: "implement"}); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := s.Jobs(ctx, JobFilter{Repo: "owner/repo"}); len(jobs) != 5 {
		t.Fatalf("repo filter: %d", len(jobs))
	}
	if jobs, _ := s.Jobs(ctx, JobFilter{Repo: "owner/repo", Issue: 3}); len(jobs) != 1 || jobs[0].Issue != 3 {
		t.Fatalf("issue filter: %+v", jobs)
	}
	if jobs, _ := s.Jobs(ctx, JobFilter{Limit: 2}); len(jobs) != 2 {
		t.Fatalf("limit: %d", len(jobs))
	}
	if jobs, _ := s.Jobs(ctx, JobFilter{Limit: 100000}); len(jobs) != 6 {
		t.Fatalf("clamped limit: %d", len(jobs))
	}
}

// A job never exposes the task payload, the prompt, leases or fencing state
// beyond the epoch, or anything from the result other than the whitelisted
// fields.
func TestJobOmitsPayloadAndSecrets(t *testing.T) {
	s, _, _ := setup(t)
	register(t, s, "w1", 2)
	enqueueKind(t, s, "implement", 1, 0, `{"title":"safe title","testing":{"command":"SECRET_COMMAND"},"base_url":"https://user:pw@git.example","acceptance":{"x":"HIDDEN_CONTRACT"}}`)
	task := claim(t, s, "w1")
	_ = s.Complete(ctx, task.ID, "w1", task.FencingToken, json.RawMessage(`{"status":"success","events":[{"x":"RAW_EVENT"}],"development_events":"RAW_TRANSCRIPT","pr_url":"https://tok:en@github.com/o/r/pull/1?x=y#frag"}`))
	d, err := s.Job(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(d)
	list, _ := s.Jobs(ctx, JobFilter{})
	rawList, _ := json.Marshal(list)
	for _, blob := range []string{string(raw), string(rawList)} {
		for _, leak := range []string{"SECRET_COMMAND", "user:pw", "HIDDEN_CONTRACT", "RAW_EVENT", "RAW_TRANSCRIPT", "tok:en", "lease_owner", "fencing"} {
			if strings.Contains(blob, leak) {
				t.Fatalf("%q leaked: %s", leak, blob)
			}
		}
		if !strings.Contains(blob, "safe title") {
			t.Fatalf("title missing: %s", blob)
		}
	}
	// Credentials in the artifact URL are dropped together with the link.
	if d.Artifacts.PRURL != "" {
		t.Fatalf("pr url with userinfo kept: %q", d.Artifacts.PRURL)
	}
}

func TestJobUnknownAndInvalidID(t *testing.T) {
	s, _, _ := setup(t)
	for _, id := range []string{"missing", "", strings.Repeat("a", 200)} {
		if _, err := s.Job(ctx, id); err != ErrStaleLease {
			t.Fatalf("%q: %v", id, err)
		}
	}
}

func TestJobRelatedAndWaitingReason(t *testing.T) {
	s, _, _ := setup(t)
	register(t, s, "w1", 4, "host-a")
	first := enqueueKind(t, s, "implement", 9, 0, `{}`)
	second := enqueueKind(t, s, "repair", 9, 90, `{"repair":{"reason":"ci"}}`)
	third := enqueueKind(t, s, "implement", 10, 0, `{}`, "needs-gpu")
	d, err := s.Job(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, j := range d.Related {
		ids = append(ids, j.ID)
	}
	if len(ids) != 2 || ids[0] != first.ID || ids[1] != second.ID {
		t.Fatalf("related=%v want [%s %s]", ids, first.ID, second.ID)
	}
	w, err := s.Job(ctx, third.ID)
	if err != nil || !strings.Contains(w.Waiting, "needs-gpu") {
		t.Fatalf("waiting=%q err=%v", w.Waiting, err)
	}
}

func TestRunnersListWorkersWithRunningJobs(t *testing.T) {
	s, _, _ := setup(t)
	register(t, s, "busy", 2, "host-a")
	register(t, s, "idle", 1)
	enqueueKind(t, s, "implement", 1, 0, `{"title":"live one"}`)
	task := claim(t, s, "busy")
	now := time.Now().UTC()
	if _, err := s.CreateToken(ctx, "busy", ScopeWorker); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&Token{}).Where("name = ?", "busy").Update("last_seen", now).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.db.Create(&Audit{Worker: "busy", Task: task.ID, Action: "heartbeat", CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	runners, at, err := s.Runners(ctx)
	if err != nil || at.IsZero() || len(runners) != 2 {
		t.Fatalf("runners=%+v at=%v err=%v", runners, at, err)
	}
	var busy, idle Runner
	for _, r := range runners {
		if r.Name == "busy" {
			busy = r
		} else {
			idle = r
		}
	}
	if !busy.Online || busy.LastHeartbeat == nil || len(busy.Tags) != 1 || busy.Tags[0] != "host-a" || len(busy.Running) != 1 || busy.Running[0].ID != task.ID || busy.Running[0].Title != "live one" {
		t.Fatalf("busy=%+v", busy)
	}
	if idle.Online || idle.LastHeartbeat != nil || len(idle.Running) != 0 || idle.Running == nil || idle.Tags == nil {
		t.Fatalf("idle=%+v", idle)
	}
}

func TestReadLogReportsTaskState(t *testing.T) {
	s, _, _ := setup(t)
	task := leased(t, s, "w1", 1)
	read, err := s.ReadLog(ctx, task.ID, 0, 0, 10)
	if err != nil || read.TaskState != "leased" {
		t.Fatalf("state=%q err=%v", read.TaskState, err)
	}
}
