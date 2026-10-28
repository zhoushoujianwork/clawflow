package github

import (
	"fmt"
	"net/http"
	"reflect"
	"testing"
)

func TestCIRerunSuiteMapping(t *testing.T) {
	posts := []string{}
	c := ciTestClient(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/pulls/11": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"head":{"sha":"captured-sha"}}`) },
		"GET /repos/o/r/commits/captured-sha/check-runs": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("filter") != "latest" {
				t.Error("not latest aggregate")
			}
			fmt.Fprint(w, `{"total_count":5,"check_runs":[
    {"id":1,"head_sha":"captured-sha","status":"completed","conclusion":"failure","check_suite":{"id":10},"app":{"slug":"github-actions"}},
    {"id":2,"head_sha":"captured-sha","status":"completed","conclusion":"failure","check_suite":{"id":10},"app":{"slug":"github-actions"}},
    {"id":3,"head_sha":"captured-sha","status":"completed","conclusion":"success","check_suite":{"id":20},"app":{"slug":"github-actions"}},
    {"id":4,"head_sha":"captured-sha","status":"completed","conclusion":"failure","check_suite":{"id":30},"app":{"slug":"external"}},
    {"id":5,"head_sha":"captured-sha","status":"completed","conclusion":"timed_out","check_suite":{"id":40},"app":{"slug":"github-actions"}}
   ]}`)
		},
		"GET /repos/o/r/actions/runs": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("head_sha") != "captured-sha" {
				t.Error("missing head filter")
			}
			fmt.Fprint(w, `{"total_count":5,"workflow_runs":[
    {"id":100,"head_sha":"captured-sha","check_suite_id":10,"status":"completed","conclusion":"failure"},
    {"id":200,"head_sha":"captured-sha","check_suite_id":20,"status":"completed","conclusion":"failure"},
    {"id":300,"head_sha":"captured-sha","check_suite_id":30,"status":"completed","conclusion":"failure"},
    {"id":400,"head_sha":"captured-sha","check_suite_id":40,"status":"completed","conclusion":"timed_out"},
    {"id":500,"head_sha":"captured-sha","check_suite_id":50,"status":"completed","conclusion":"cancelled"}
   ]}`)
		},
		"POST /repos/o/r/actions/runs/100/rerun-failed-jobs": func(w http.ResponseWriter, r *http.Request) { posts = append(posts, r.URL.Path); w.WriteHeader(201) },
		"POST /repos/o/r/actions/runs/400/rerun-failed-jobs": func(w http.ResponseWriter, r *http.Request) { posts = append(posts, r.URL.Path); w.WriteHeader(201) },
	})
	plan, err := c.PlanCIRerun("o/r", 11)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.RunIDs, []int64{100, 400}) || !reflect.DeepEqual(plan.NotRerunnable, []int64{4}) {
		t.Fatalf("%+v", plan)
	}
	for _, id := range plan.RunIDs {
		if err := c.RerunFailedJobs("o/r", id); err != nil {
			t.Fatal(err)
		}
	}
	if len(posts) != 2 {
		t.Fatal(posts)
	}
}

func TestCIRerunRejectsInvalidWorkflowEvidence(t *testing.T) {
	for _, body := range []string{
		`{"total_count":1,"workflow_runs":[{"id":1,"head_sha":"stale","check_suite_id":10,"status":"completed","conclusion":"failure"}]}`,
		`{"total_count":2,"workflow_runs":[]}`,
		`{"workflow_runs":[]}`,
	} {
		t.Run(body, func(t *testing.T) {
			c := ciTestClient(t, map[string]http.HandlerFunc{
				"GET /repos/o/r/pulls/11": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"head":{"sha":"captured-sha"}}`) },
				"GET /repos/o/r/commits/captured-sha/check-runs": func(w http.ResponseWriter, r *http.Request) {
					fmt.Fprint(w, `{"total_count":1,"check_runs":[{"id":1,"head_sha":"captured-sha","status":"completed","conclusion":"failure","check_suite":{"id":10},"app":{"slug":"github-actions"}}]}`)
				},
				"GET /repos/o/r/actions/runs": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) },
			})
			if _, err := c.PlanCIRerun("o/r", 11); err == nil {
				t.Fatal("accepted invalid evidence")
			}
		})
	}
}

func TestCIRerunPostNotRetried(t *testing.T) {
	calls := 0
	c := ciTestClient(t, map[string]http.HandlerFunc{
		"POST /repos/o/r/actions/runs/1/rerun-failed-jobs": func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(503) },
	})
	if err := c.RerunFailedJobs("o/r", 1); err == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestCIRerunExternalAndPendingChecks(t *testing.T) {
	c := ciTestClient(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/pulls/11": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"head":{"sha":"captured-sha"}}`) },
		"GET /repos/o/r/commits/captured-sha/check-runs": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"total_count":2,"check_runs":[
   {"id":1,"head_sha":"captured-sha","status":"completed","conclusion":"failure","check_suite":{"id":10},"app":{"slug":"external"}},
   {"id":2,"head_sha":"captured-sha","status":"in_progress","conclusion":null,"check_suite":{"id":20},"app":{"slug":"github-actions"}}
  ]}`)
		},
	})
	plan, err := c.PlanCIRerun("o/r", 11)
	if err != nil || len(plan.RunIDs) != 0 || !reflect.DeepEqual(plan.NotRerunnable, []int64{1, 2}) {
		t.Fatalf("%+v %v", plan, err)
	}
}

func TestCIRerunWorkflowConclusions(t *testing.T) {
	for _, tc := range []struct {
		status, conclusion string
		want               bool
	}{
		{"completed", "failure", true}, {"completed", "timed_out", true}, {"completed", "cancelled", true},
		{"in_progress", "", false}, {"completed", "success", false}, {"completed", "skipped", false},
	} {
		t.Run(tc.status+tc.conclusion, func(t *testing.T) {
			c := ciTestClient(t, map[string]http.HandlerFunc{
				"GET /repos/o/r/pulls/11": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"head":{"sha":"captured-sha"}}`) },
				"GET /repos/o/r/commits/captured-sha/check-runs": func(w http.ResponseWriter, r *http.Request) {
					fmt.Fprint(w, `{"total_count":1,"check_runs":[{"id":1,"head_sha":"captured-sha","status":"completed","conclusion":"failure","check_suite":{"id":10},"app":{"slug":"github-actions"}}]}`)
				},
				"GET /repos/o/r/actions/runs": func(w http.ResponseWriter, r *http.Request) {
					fmt.Fprintf(w, `{"total_count":1,"workflow_runs":[{"id":100,"head_sha":"captured-sha","check_suite_id":10,"status":%q,"conclusion":%q}]}`, tc.status, tc.conclusion)
				},
			})
			plan, err := c.PlanCIRerun("o/r", 11)
			if err != nil || (len(plan.RunIDs) == 1) != tc.want || (len(plan.NotRerunnable) == 0) != tc.want {
				t.Fatalf("%+v %v", plan, err)
			}
		})
	}
}

func TestCIRerunWorkflowPagination(t *testing.T) {
	pages := 0
	c := ciTestClient(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/pulls/11": func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"head":{"sha":"captured-sha"}}`) },
		"GET /repos/o/r/commits/captured-sha/check-runs": func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"total_count":1,"check_runs":[{"id":1,"head_sha":"captured-sha","status":"completed","conclusion":"failure","check_suite":{"id":10},"app":{"slug":"github-actions"}}]}`)
		},
		"GET /repos/o/r/actions/runs": func(w http.ResponseWriter, r *http.Request) {
			pages++
			fmt.Fprint(w, `{"total_count":101,"workflow_runs":[`)
			if r.URL.Query().Get("page") == "1" {
				for i := 1; i <= 100; i++ {
					if i > 1 {
						fmt.Fprint(w, ",")
					}
					fmt.Fprintf(w, `{"id":%d,"head_sha":"captured-sha","check_suite_id":20,"status":"completed","conclusion":"failure"}`, i)
				}
			} else {
				fmt.Fprint(w, `{"id":101,"head_sha":"captured-sha","check_suite_id":10,"status":"completed","conclusion":"failure"}`)
			}
			fmt.Fprint(w, `]}`)
		},
	})
	plan, err := c.PlanCIRerun("o/r", 11)
	if err != nil || pages != 2 || !reflect.DeepEqual(plan.RunIDs, []int64{101}) {
		t.Fatalf("%+v %v pages=%d", plan, err, pages)
	}
}
