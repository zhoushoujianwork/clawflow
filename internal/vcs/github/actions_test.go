package github

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/actions"
)

type actionsFixture struct {
	request                 actions.Request
	run                     actionsRun
	pr                      actionsPR
	job                     actionsJob
	checks                  map[string]any
	artifacts               []actions.Artifact
	runs                    []actionsRun
	logs, workflow          string
	parents                 []string
	throttle                string
	changePR, changeAttempt bool
	requests                int
}

func newActionsFixture() *actionsFixture {
	head, base, merge := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	workflow := "approved CI workflow bytes"
	sum := sha256.Sum256([]byte(workflow))
	f := &actionsFixture{workflow: workflow, parents: []string{base, head}}
	f.request = actions.Request{
		Contract: acceptance.Contract{Version: 1, TaskID: "task", Generation: 1, Repository: "o/r", PRNumber: 7, HeadSHA: head, BaseSHA: base, IntegrationSHA: merge, PolicyDigest: "policy", Requirements: "test", Development: acceptance.Identity{SessionID: "dev", RunID: "dev-run"}},
		Policy:   actions.Policy{Version: 1, RepositoryID: 1, WorkflowID: 2, WorkflowPath: ".github/workflows/ci.yml", WorkflowDigest: hex.EncodeToString(sum[:]), Jobs: []actions.RequiredJob{{Name: "Test", Steps: []string{"Build web bundle", "Unit tests", "CLI smoke tests"}}}},
	}
	f.pr.Number = 7
	f.pr.Head.SHA = head
	f.pr.Head.Repo = actionsRepo{1, "o/r"}
	f.pr.Base.SHA = base
	f.pr.Base.Repo = actionsRepo{1, "o/r"}
	f.run = actionsRun{ID: 10, WorkflowID: 2, Attempt: 1, HeadSHA: head, SuiteID: 30, URL: "https://github.com/o/r/actions/runs/10", Path: ".github/workflows/ci.yml", Event: "pull_request", Status: "completed", Conclusion: "success", Repository: actionsRepo{1, "o/r"}, HeadRepository: actionsRepo{1, "o/r"}, PRs: []actionsPR{f.pr}}
	f.run.StartedAt, _ = time.Parse(time.RFC3339, "2026-10-29T00:00:00Z")
	_ = json.Unmarshal([]byte(fmt.Sprintf(`{"id":20,"run_id":10,"run_attempt":1,"head_sha":%q,"check_run_url":"https://api.github.com/repos/o/r/check-runs/40","name":"Test","status":"completed","conclusion":"success","steps":[{"name":"Run actions/checkout@v4","status":"completed","conclusion":"success","started_at":"2026-10-29T00:00:00Z","completed_at":"2026-10-29T00:00:02Z"},{"name":"Build web bundle","status":"completed","conclusion":"success"},{"name":"Unit tests","status":"completed","conclusion":"success"},{"name":"CLI smoke tests","status":"completed","conclusion":"success"}]}`, head)), &f.job)
	f.checks = map[string]any{"id": 40, "head_sha": head, "status": "completed", "conclusion": "success", "app": map[string]any{"slug": "github-actions"}, "check_suite": map[string]any{"id": 30}}
	f.logs = "2026-10-29T00:00:01Z [command]/usr/bin/git log -1 --format=%H\n2026-10-29T00:00:01.1Z " + merge + "\n"
	return f
}

func (f *actionsFixture) client(t *testing.T) *Client {
	t.Helper()
	c := New("private-token", "https://api.github.com")
	prReads, runReads := 0, 0
	c.http = &http.Client{Transport: repairTransport(func(req *http.Request) (*http.Response, error) {
		f.requests++
		if req.Method != "GET" {
			t.Fatalf("observer wrote provider state: %s", req.Method)
		}
		w := httptest.NewRecorder()
		if req.URL.Path == f.throttle {
			w.WriteHeader(429)
			return w.Result(), nil
		}
		var body any
		switch req.URL.Path {
		case "/repos/o/r/pulls/7":
			prReads++
			p := f.pr
			if f.changePR && prReads > 1 {
				p.Base.SHA = strings.Repeat("d", 40)
			}
			body = p
		case "/repos/o/r/actions/workflows/2/runs":
			if req.URL.Query().Get("head_sha") != f.request.Contract.HeadSHA {
				t.Fatal("discovery must pin head")
			}
			runs := f.runs
			if runs == nil {
				runs = []actionsRun{f.run}
			}
			body = map[string]any{"total_count": lenUniqueRuns(runs), "workflow_runs": runs}
		case "/repos/o/r/actions/runs/10":
			runReads++
			run := f.run
			if f.changeAttempt && runReads > 1 {
				run.Attempt++
			}
			body = run
		case fmt.Sprintf("/repos/o/r/actions/runs/10/attempts/%d", f.run.Attempt):
			body = f.run
		case fmt.Sprintf("/repos/o/r/actions/runs/10/attempts/%d/jobs", f.run.Attempt):
			body = map[string]any{"total_count": 1, "jobs": []actionsJob{f.job}}
		case "/repos/o/r/check-runs/40":
			body = f.checks
		case "/repos/o/r/actions/jobs/20/logs":
			fmt.Fprint(w, f.logs)
			return w.Result(), nil
		case "/repos/o/r/git/commits/" + f.request.Contract.IntegrationSHA:
			parents := []map[string]string{}
			for _, p := range f.parents {
				parents = append(parents, map[string]string{"sha": p})
			}
			body = map[string]any{"sha": f.request.Contract.IntegrationSHA, "parents": parents}
		case "/repos/o/r/contents/.github/workflows/ci.yml":
			if req.URL.Query().Get("ref") != f.request.Contract.IntegrationSHA {
				t.Fatal("workflow must be read at tested merge")
			}
			body = map[string]any{"sha": strings.Repeat("f", 40), "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(f.workflow))}
		case "/repos/o/r/actions/runs/10/artifacts":
			body = map[string]any{"total_count": len(f.artifacts), "artifacts": f.artifacts}
		default:
			t.Errorf("unexpected Actions request: %s", req.URL)
			w.WriteHeader(404)
			return w.Result(), nil
		}
		_ = json.NewEncoder(w).Encode(body)
		return w.Result(), nil
	})}
	return c
}

func lenUniqueRuns(runs []actionsRun) int {
	ids := map[int64]bool{}
	for _, r := range runs {
		ids[r.ID] = true
	}
	return len(ids)
}

func TestActionsObservationFixtures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*actionsFixture)
		state   string
		wantErr bool
	}{
		{"valid synthetic merge", func(*actionsFixture) {}, "passed", false},
		{"duplicate event", func(f *actionsFixture) { f.runs = []actionsRun{f.run, f.run} }, "passed", false},
		{"conflicting duplicate", func(f *actionsFixture) {
			other := f.run
			other.Conclusion = "failure"
			f.runs = []actionsRun{f.run, other}
		}, "", true},
		{"workflow failed", func(f *actionsFixture) { f.run.Conclusion = "failure" }, "failed", false},
		{"workflow skipped", func(f *actionsFixture) { f.run.Conclusion = "skipped" }, "failed", false},
		{"workflow cancelled", func(f *actionsFixture) { f.run.Conclusion = "cancelled" }, "failed", false},
		{"rerun pending supersedes pass", func(f *actionsFixture) { f.run.Attempt = 2; f.run.Status = "in_progress" }, "waiting_external", false},
		{"rerun failure supersedes pass", func(f *actionsFixture) { f.run.Attempt = 2; f.run.Conclusion = "failure" }, "failed", false},
		{"mixed job attempt", func(f *actionsFixture) { f.job.Attempt = 2 }, "unknown", false},
		{"missing job", func(f *actionsFixture) { f.job.Name = "Other" }, "unknown", false},
		{"skipped job", func(f *actionsFixture) { f.job.Conclusion = "skipped" }, "failed", false},
		{"cancelled check", func(f *actionsFixture) { f.checks["conclusion"] = "cancelled" }, "failed", false},
		{"missing step", func(f *actionsFixture) { f.job.Steps = f.job.Steps[:3] }, "unknown", false},
		{"skipped step", func(f *actionsFixture) { f.job.Steps[2].Conclusion = "skipped" }, "failed", false},
		{"untrusted app", func(f *actionsFixture) { f.checks["app"] = map[string]string{"slug": "evil"} }, "unknown", false},
		{"changed head", func(f *actionsFixture) { f.pr.Head.SHA = strings.Repeat("e", 40) }, "unknown", false},
		{"changed base", func(f *actionsFixture) { f.pr.Base.SHA = strings.Repeat("e", 40) }, "unknown", false},
		{"base race", func(f *actionsFixture) { f.changePR = true }, "unknown", false},
		{"attempt race", func(f *actionsFixture) { f.changeAttempt = true }, "unknown", false},
		{"wrong merge parents", func(f *actionsFixture) { f.parents[0] = strings.Repeat("e", 40) }, "unknown", false},
		{"checkout is head not merge", func(f *actionsFixture) {
			f.logs = strings.ReplaceAll(f.logs, f.request.Contract.IntegrationSHA, f.request.Contract.HeadSHA)
		}, "unknown", false},
		{"test output cannot spoof checkout", func(f *actionsFixture) { f.logs = strings.ReplaceAll(f.logs, "00:00:01", "00:01:01") }, "unknown", false},
		{"workflow changed cannot self approve", func(f *actionsFixture) { f.workflow = "weakened workflow" }, "unknown", false},
		{"missing artifact", func(f *actionsFixture) { f.request.Policy.Artifacts = []string{"report"} }, "unknown", false},
		{"expired artifact", func(f *actionsFixture) {
			f.request.Policy.Artifacts = []string{"report"}
			f.artifacts = []actions.Artifact{{ID: 50, Name: "report", Expired: true}}
		}, "unknown", false},
		{"rate limit", func(f *actionsFixture) { f.throttle = "/repos/o/r/actions/workflows/2/runs" }, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newActionsFixture()
			tc.change(f)
			o, err := f.client(t).ObserveActions(context.Background(), f.request, actions.Observation{})
			if (err != nil) != tc.wantErr {
				t.Fatalf("error: %v", err)
			}
			if !tc.wantErr && o.State != tc.state {
				t.Fatalf("state %q want %q: %s", o.State, tc.state, o.Reason)
			}
			if o.State == "passed" && (o.TestedSHA != f.request.Contract.IntegrationSHA || len(o.Jobs) != 1 || o.Jobs[0].CheckID != 40 || o.WorkflowBlob == "") {
				t.Fatalf("missing provenance: %+v", o)
			}
		})
	}
}

func TestActionsRestartReattachment(t *testing.T) {
	f := newActionsFixture()
	c := f.client(t)
	o, err := c.ObserveActions(context.Background(), f.request, actions.Observation{})
	if err != nil || o.State != "passed" {
		t.Fatalf("%+v %v", o, err)
	}
	// A fresh adapter, retaining only persisted input, observes the same run.
	got, err := f.client(t).ObserveActions(context.Background(), f.request, o)
	if err != nil || got.State != "passed" || got.RunID != o.RunID {
		t.Fatalf("%+v %v", got, err)
	}
	f.runs = []actionsRun{}
	got, err = f.client(t).ObserveActions(context.Background(), f.request, o)
	if err != nil || got.State != "unknown" || got.RunID != o.RunID {
		t.Fatalf("disappeared run must retain identity: %+v %v", got, err)
	}
}

func TestCheckoutSHARejectsDuplicateProof(t *testing.T) {
	f := newActionsFixture()
	start, _ := time.Parse(time.RFC3339, "2026-10-29T00:00:00Z")
	end := start.Add(2 * time.Second)
	if checkoutSHA(f.logs+f.logs, start, end, f.request.Contract.IntegrationSHA) {
		t.Fatal("duplicate checkout identity passed")
	}
}

func TestActionsArtifactAttemptProvenance(t *testing.T) {
	for _, stale := range []bool{false, true} {
		f := newActionsFixture()
		f.request.Policy.Artifacts = []string{"report"}
		a := actions.Artifact{ID: 50, Name: "report", URL: "https://api.github.com/repos/o/r/actions/artifacts/50/zip", Digest: "sha256:" + strings.Repeat("d", 64), CreatedAt: f.run.StartedAt.Add(time.Minute)}
		a.WorkflowRun.ID = 10
		a.WorkflowRun.HeadSHA = f.run.HeadSHA
		if stale {
			a.CreatedAt = f.run.StartedAt.Add(-time.Minute)
		}
		f.artifacts = []actions.Artifact{a}
		o, err := f.client(t).ObserveActions(context.Background(), f.request, actions.Observation{})
		if err != nil || (o.State == "passed") == stale {
			t.Fatalf("stale=%v: %+v %v", stale, o, err)
		}
	}
}

func TestActionsPagination(t *testing.T) {
	for _, missing := range []bool{false, true} {
		c := ciTestClient(t, map[string]http.HandlerFunc{"GET /pages": func(w http.ResponseWriter, r *http.Request) {
			items := []map[string]int64{}
			if r.URL.Query().Get("page") == "1" {
				for id := int64(1); id <= 100; id++ {
					items = append(items, map[string]int64{"id": id})
				}
			} else if !missing {
				items = append(items, map[string]int64{"id": 101})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 101, "jobs": items})
		}})
		jobs, err := actionsPages(c, "/pages", "jobs", func(j actionsJob) int64 { return j.ID })
		if (err != nil) != missing || (!missing && len(jobs) != 101) {
			t.Fatalf("missing=%v: %d %v", missing, len(jobs), err)
		}
	}
}
