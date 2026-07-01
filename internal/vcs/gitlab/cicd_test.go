package gitlab_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

func TestListProjectRunners(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/runners": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 200, []map[string]any{
				{"id": 11, "description": "shared-1", "active": true, "online": true, "status": "online", "runner_type": "instance_type", "tag_list": []string{"docker", "linux"}},
				{"id": 22, "description": "proj-1", "active": true, "online": false, "status": "offline", "runner_type": "project_type"},
			})
		},
	})

	runners, err := client.ListProjectRunners("ns/group/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(runners) != 2 {
		t.Fatalf("expected 2 runners, got %d", len(runners))
	}
	if runners[0].ID != 11 || !runners[0].Online || runners[0].Status != "online" {
		t.Errorf("runner[0] mismatch: %+v", runners[0])
	}
	if len(runners[0].TagList) != 2 || runners[0].TagList[0] != "docker" {
		t.Errorf("runner[0] tags mismatch: %v", runners[0].TagList)
	}
	if runners[1].Online {
		t.Errorf("runner[1] should be offline: %+v", runners[1])
	}
}

func TestListProjectRunnersError(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/runners": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "forbidden", 403)
		},
	})
	if _, err := client.ListProjectRunners("ns/group/repo"); err == nil {
		t.Fatal("expected error on HTTP 403")
	}
}

func TestListPipelines(t *testing.T) {
	var gotQuery string
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/pipelines": func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.RawQuery
			jsonResp(w, 200, []map[string]any{
				{"id": 1001, "iid": 5, "ref": "main", "sha": "abc123", "status": "failed", "created_at": "2026-06-30T10:00:00Z"},
				{"id": 1000, "iid": 4, "ref": "main", "sha": "def456", "status": "success", "created_at": "2026-06-29T10:00:00Z"},
			})
		},
	})

	pipelines, err := client.ListPipelines("ns/group/repo", gitlab.PipelineListOpts{Ref: "main", Status: "failed", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(pipelines) != 2 {
		t.Fatalf("expected 2 pipelines, got %d", len(pipelines))
	}
	if pipelines[0].ID != 1001 || pipelines[0].Status != "failed" {
		t.Errorf("pipeline[0] mismatch: %+v", pipelines[0])
	}
	// Filters must reach the wire.
	for _, want := range []string{"ref=main", "status=failed", "per_page=10"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
}

func TestListPipelinesLimitClamp(t *testing.T) {
	var gotQuery string
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/pipelines": func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.RawQuery
			jsonResp(w, 200, []map[string]any{})
		},
	})
	// Limit 0 → default 20; over-max clamps to 100.
	if _, err := client.ListPipelines("ns/group/repo", gitlab.PipelineListOpts{Limit: 0}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "per_page=20") {
		t.Errorf("limit 0 should default to 20, query=%q", gotQuery)
	}
	if _, err := client.ListPipelines("ns/group/repo", gitlab.PipelineListOpts{Limit: 500}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "per_page=100") {
		t.Errorf("limit 500 should clamp to 100, query=%q", gotQuery)
	}
}

func TestGetPipelineAndJobs(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/pipelines/1001": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 200, map[string]any{
				"id": 1001, "iid": 5, "ref": "main", "sha": "abc123", "status": "failed",
				"web_url": "https://gl/ns/group/repo/-/pipelines/1001",
			})
		},
		"GET " + projectPath + "/pipelines/1001/jobs": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 200, []map[string]any{
				{"id": 5001, "name": "build", "stage": "build", "status": "success"},
				{"id": 5002, "name": "test", "stage": "test", "status": "failed", "runner": map[string]any{"id": 22, "description": "proj-1"}},
			})
		},
	})

	p, err := client.GetPipeline("ns/group/repo", 1001)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != 1001 || p.Status != "failed" || p.WebURL == "" {
		t.Errorf("pipeline mismatch: %+v", p)
	}

	jobs, err := client.ListPipelineJobs("ns/group/repo", 1001)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}
	if jobs[1].Name != "test" || jobs[1].Status != "failed" {
		t.Errorf("job[1] mismatch: %+v", jobs[1])
	}
	if jobs[1].Runner == nil || jobs[1].Runner.ID != 22 {
		t.Errorf("job[1] runner mismatch: %+v", jobs[1].Runner)
	}
}

func TestGetJobLog(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/jobs/5002/trace": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(200)
			w.Write([]byte("Running with gitlab-runner\n$ make test\nFAIL\n"))
		},
	})

	log, err := client.GetJobLog("ns/group/repo", 5002)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log, "make test") || !strings.Contains(log, "FAIL") {
		t.Errorf("unexpected log: %q", log)
	}
}

func TestGetJobLogNotFound(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/jobs/9999/trace": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "404 Not Found", 404)
		},
	})
	if _, err := client.GetJobLog("ns/group/repo", 9999); err == nil {
		t.Fatal("expected error on HTTP 404")
	}
}
