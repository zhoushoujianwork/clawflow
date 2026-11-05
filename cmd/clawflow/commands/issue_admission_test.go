package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/retry"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

type admissionClient struct {
	vcs.Client
	issue   *vcs.Issue
	allowed bool
	err     error
	checks  int
}

func (c *admissionClient) AuthorHasWriteAccess(string, string) (bool, error) {
	c.checks++
	return c.allowed, c.err
}

func (c *admissionClient) GetIssue(string, int) (*vcs.Issue, error) {
	if c.issue == nil {
		return nil, nil
	}
	copy := *c.issue
	return &copy, nil
}

func useRealIssueAdmission(t *testing.T) {
	t.Helper()
	old := issueAdmissionFn
	issueAdmissionFn = checkIssueAdmission
	t.Cleanup(func() { issueAdmissionFn = old })
}

func TestIssueAdmissionRules(t *testing.T) {
	useRealIssueAdmission(t)
	t.Run("allowlist ignores case and avoids lookup", func(t *testing.T) {
		client := &admissionClient{err: errors.New("must not be called")}
		record, reason := issueAdmissionFn(client, "o/r", config.Repo{AllowedIssueAuthors: []string{" Release-Bot "}}, vcs.Issue{Author: "release-bot"})
		if reason != "" || record == nil || record.Basis != "allowlist" || record.Author != "release-bot" || client.checks != 0 {
			t.Fatalf("record=%+v reason=%q checks=%d", record, reason, client.checks)
		}
	})
	t.Run("write permission", func(t *testing.T) {
		client := &admissionClient{allowed: true}
		record, reason := issueAdmissionFn(client, "o/r", config.Repo{}, vcs.Issue{Author: "alice"})
		if reason != "" || record == nil || record.Basis != "write_permission" || client.checks != 1 {
			t.Fatalf("record=%+v reason=%q checks=%d", record, reason, client.checks)
		}
	})
	for _, tc := range []struct {
		name   string
		author string
		client *admissionClient
		want   string
	}{
		{name: "denied", author: "guest", client: &admissionClient{}, want: "author_not_admitted:guest"},
		{name: "lookup error", author: "alice", client: &admissionClient{err: errors.New("offline")}, want: "author_check_failed"},
		{name: "empty author", client: &admissionClient{err: errors.New("must not be called")}, want: "author_check_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record, reason := issueAdmissionFn(tc.client, "o/r", config.Repo{}, vcs.Issue{Author: tc.author})
			if record != nil || reason != tc.want {
				t.Fatalf("record=%+v reason=%q, want %q", record, reason, tc.want)
			}
		})
	}
}

func TestIssueAdmissionExecutionGateIsQuiet(t *testing.T) {
	useRealIssueAdmission(t)
	client := &admissionClient{issue: &vcs.Issue{Number: 7, State: "open", Author: "guest"}}
	job := &runJob{
		repo: "o/r", client: client, repoCfg: config.Repo{},
		op: &operator.Operator{Name: "implement"}, sub: &operator.Subject{Number: 7, State: "open", Author: "stale-author"},
	}
	err := prepareImplementation(job)
	if !errors.Is(err, errIssueAdmissionDeferred) || issueAdmissionReason(err) != "author_not_admitted:guest" || client.checks != 1 {
		t.Fatalf("err=%v checks=%d", err, client.checks)
	}
	if job.sub.Author != "guest" {
		t.Fatalf("execution did not use fresh author: %q", job.sub.Author)
	}
}

// The embedded nil vcs.Client panics on any label or comment write, so reaching
// the assertions proves the denial wrote nothing.
func TestIssueAdmissionDenialWritesNothingOnRunPath(t *testing.T) {
	useRealIssueAdmission(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	client := &admissionClient{issue: &vcs.Issue{Number: 7, State: "open", Author: "guest"}}
	job := &runJob{
		repo: "o/r", client: client, repoCfg: config.Repo{},
		op: &operator.Operator{Name: "implement"}, sub: &operator.Subject{Number: 7, State: "open"},
		hooks: &jobHooks{SkipIndex: true, AcquireLock: func(context.Context, *runJob) error { return nil }},
	}
	ctx := context.WithValue(context.Background(), reservedAgentSlot{}, true)
	fired, limited, failed := runOneOperator(ctx, job, time.Second)
	if fired || limited || failed || job.result.Status != "skipped" || job.result.Error != "" || len(job.result.Effects) != 0 || client.checks != 1 {
		t.Fatalf("fired=%v limited=%v failed=%v result=%+v checks=%d", fired, limited, failed, job.result, client.checks)
	}
}

func TestExecutionAdmissionTrustsControllerRecordOnWorker(t *testing.T) {
	useRealIssueAdmission(t)
	record := &issueAdmission{Author: "Release-Bot", Basis: "allowlist", CheckedAt: time.Now().UTC()}
	cases := []struct {
		name       string
		hooks      *jobHooks
		record     *issueAdmission
		lookupErr  error
		author     string
		wantReason string
		wantChecks int
	}{
		{name: "worker uses the controller record even when its own lookup fails", hooks: &jobHooks{}, record: record, lookupErr: errors.New("forbidden"), author: "release-bot"},
		{name: "worker refuses a record for another author", hooks: &jobHooks{}, record: record, author: "guest", wantReason: "author_check_failed"},
		{name: "worker without a record checks locally", hooks: &jobHooks{}, author: "guest", wantReason: "author_not_admitted:guest", wantChecks: 1},
		{name: "local run always checks again", record: record, author: "release-bot", wantReason: "author_not_admitted:release-bot", wantChecks: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &admissionClient{err: tc.lookupErr}
			job := &runJob{repo: "o/r", client: client, hooks: tc.hooks, admission: tc.record}
			got, reason := executionAdmission(job, vcs.Issue{Number: 7, Author: tc.author})
			if reason != tc.wantReason || client.checks != tc.wantChecks {
				t.Fatalf("reason=%q checks=%d, want %q and %d", reason, client.checks, tc.wantReason, tc.wantChecks)
			}
			if tc.wantReason == "" && got != tc.record {
				t.Fatalf("record not preserved: %+v", got)
			}
		})
	}
}

func TestIssueAdmissionScanGateAndQueueVisibility(t *testing.T) {
	useRealIssueAdmission(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GH_TOKEN", "test-only")
	t.Setenv("CLAWFLOW_PILOT_BUDGET_PATH", "")
	withRunLog(t)

	permission := "deny"
	permissionCalls := 0
	writes := 0
	issues := []map[string]any{
		{"id": 1, "number": 1, "title": "one", "body": "", "state": "open", "user": map[string]string{"login": "guest"}, "labels": []any{}},
		{"id": 2, "number": 2, "title": "two", "body": "", "state": "open", "user": map[string]string{"login": "GUEST"}, "labels": []any{}},
		{"id": 3, "number": 3, "title": "question", "body": "", "state": "open", "user": map[string]string{"login": "guest"}, "labels": []map[string]string{{"name": "question"}}},
		{"id": 4, "number": 4, "title": "anonymous", "body": "", "state": "open", "labels": []any{}},
	}
	original := http.DefaultTransport
	http.DefaultTransport = reopenedLifecycleTransport(func(r *http.Request) (*http.Response, error) {
		status := 200
		var body any = []any{}
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/o/r":
			body = map[string]int{"id": 1}
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/issues":
			body = issues
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/sub_issues"):
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/pulls":
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/collaborators/guest/permission":
			permissionCalls++
			switch permission {
			case "allow":
				body = map[string]string{"permission": "write"}
			case "error":
				status = 500
			default:
				body = map[string]string{"permission": "read"}
			}
		default:
			if r.Method != "GET" {
				writes++
			}
			return nil, fmt.Errorf("unexpected fake VCS call: %s %s", r.Method, r.URL.Path)
		}
		data, _ := json.Marshal(body)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })

	reg := operator.NewRegistry()
	if err := reg.LoadEmbedded(os.DirFS("../../../skills"), "."); err != nil {
		t.Fatal(err)
	}
	rc := config.Repo{Enabled: true, Platform: "github", BaseURL: "https://vcs.invalid", LocalPath: t.TempDir(), BaseBranch: "main"}
	scan := func() ([]snapshot.PendingEntry, []*runJob) {
		var captured []snapshot.IssueEntry
		pending, jobs, err := scanRepoOnce(context.Background(), reg, "o/r", rc, true, 0, &captured)
		if err != nil {
			t.Fatal(err)
		}
		return pending, jobs
	}

	pending, jobs := scan()
	if permissionCalls != 1 {
		t.Fatalf("same-login lookups were not cached: %d", permissionCalls)
	}
	if writes != 0 {
		t.Fatalf("denial wrote remote state %d time(s)", writes)
	}
	for _, entry := range pending {
		if entry.Operator == "implement" {
			t.Fatalf("denied implementation entered pending: %+v", pending)
		}
	}
	if len(jobs) != 1 || jobs[0].op.Name != "reply-question" || jobs[0].sub.Number != 3 {
		t.Fatalf("non-implement operator was affected: pending=%+v jobs=%+v", pending, jobs)
	}
	state, err := queue.LoadState("o/r")
	if err != nil {
		t.Fatal(err)
	}
	rows := queue.BuildList([]queue.RepoState{state}, nil, "o/r")
	wantStates := map[int]string{1: "blocked:author_not_admitted:guest", 2: "blocked:author_not_admitted:GUEST", 4: "blocked:author_check_failed"}
	for _, row := range rows {
		if want, ok := wantStates[row.Number]; ok && row.State != want {
			t.Fatalf("issue %d state=%q, want %q", row.Number, row.State, want)
		}
		delete(wantStates, row.Number)
	}
	if len(wantStates) != 0 {
		t.Fatalf("queue omitted denied issues: %v (rows=%+v)", wantStates, rows)
	}
	dashboard := dashboardRepo("o/r", rc)
	if len(dashboard.IssueQueue) != len(rows) {
		t.Fatalf("dashboard queue lost rows: %+v", dashboard.IssueQueue)
	}
	d := pipelineTestDeps(newPipelineFake(), map[string]config.Repo{"o/r": rc})
	d.globalQueue = func() []queue.ListEntry { return rows }
	pipelineSnapshot := buildPipeline(context.Background(), d)
	if len(pipelineSnapshot.GlobalQueue) != len(rows) || pipelineSnapshot.GlobalQueue[0].State != rows[0].State {
		t.Fatalf("pipeline queue lost reason: %+v", pipelineSnapshot.GlobalQueue)
	}

	permission = "error"
	permissionCalls = 0
	pending, jobs = scan()
	if permissionCalls != retry.DefaultConfig.MaxAttempts || len(jobs) != 1 {
		t.Fatalf("error scan: calls=%d pending=%+v jobs=%+v", permissionCalls, pending, jobs)
	}
	state, _ = queue.LoadState("o/r")
	rows = queue.BuildList([]queue.RepoState{state}, nil, "o/r")
	for _, row := range rows {
		if (row.Number == 1 || row.Number == 2 || row.Number == 4) && row.State != "blocked:author_check_failed" {
			t.Fatalf("lookup error state for #%d = %q", row.Number, row.State)
		}
	}

	permission = "allow"
	permissionCalls = 0
	pending, jobs = scan()
	if permissionCalls != 1 {
		t.Fatalf("retry did not recheck permission: %d", permissionCalls)
	}
	var implementPending int
	for _, entry := range pending {
		if entry.Operator == "implement" {
			implementPending++
		}
	}
	if implementPending != 2 {
		t.Fatalf("working retry did not admit implementations: pending=%+v jobs=%+v", pending, jobs)
	}
}

func TestIssueAdmissionEmptyAuthorBlocksQueue(t *testing.T) {
	in := plannerInput("o/r", vcs.Issue{Number: 1, State: "open", Author: ""})
	in.ImplementState[1] = "author_check_failed"
	in.ImplementJobs[1] = false
	plan := planRepoQueue(in)
	rows := queue.BuildList([]queue.RepoState{plan.State}, nil, "o/r")
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].State, "blocked:author_check_failed") {
		t.Fatalf("rows=%+v", rows)
	}
}
