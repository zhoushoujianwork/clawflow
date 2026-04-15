package gitlab_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

func newTestClient(t *testing.T, routes map[string]http.HandlerFunc) *gitlab.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Use RawPath when available — projectID uses %2F which Go's server decodes in Path
		path := r.URL.RawPath
		if path == "" {
			path = r.URL.Path
		}
		key := r.Method + " " + path
		if h, ok := routes[key]; ok {
			h(w, r)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, path)
		http.Error(w, "not found", 404)
	}))
	t.Cleanup(srv.Close)
	return gitlab.New("test-token", srv.URL)
}

func jsonResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// GitLab project path "ns/group/repo" is URL-encoded as "ns%2Fgroup%2Frepo"
const projectPath = "/api/v4/projects/ns%2Fgroup%2Frepo"

func TestListOpenIssues(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/issues": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 200, []map[string]any{
				{"iid": 1, "title": "bug report", "description": "details", "labels": []string{"bug", "in-progress"}},
				{"iid": 2, "title": "feature", "description": "", "labels": []string{}},
			})
		},
	})

	issues, err := client.ListOpenIssues("ns/group/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
	if !issues[0].HasLabel("bug") || !issues[0].HasLabel("in-progress") {
		t.Errorf("expected labels on issue #1, got %v", issues[0].Labels)
	}
}

func TestListOpenPRs(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/merge_requests": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 200, []map[string]any{
				{"iid": 5, "title": "fix issue-3", "description": "Fixes #3", "state": "opened", "source_branch": "fix/issue-3"},
			})
		},
	})

	prs, err := client.ListOpenPRs("ns/group/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || prs[0].HeadBranch != "fix/issue-3" {
		t.Errorf("unexpected PRs: %+v", prs)
	}
}

func TestPRExistsForIssue(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/merge_requests": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 200, []map[string]any{
				{"iid": 5, "title": "fix", "description": "", "state": "opened", "source_branch": "fix/issue-3"},
			})
		},
	})

	exists, err := client.PRExistsForIssue("ns/group/repo", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("expected MR to exist for issue #3")
	}

	exists, _ = client.PRExistsForIssue("ns/group/repo", 99)
	if exists {
		t.Error("expected no MR for issue #99")
	}
}

func TestAddLabel(t *testing.T) {
	// GitLab 11.11: AddLabel fetches current labels then PUTs the merged set
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/issues/7": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 200, map[string]any{"labels": []string{"existing"}})
		},
		"PUT " + projectPath + "/issues/7": func(w http.ResponseWriter, r *http.Request) {
			r.ParseForm()
			labels := r.FormValue("labels")
			// should contain both existing and new label
			if labels == "" {
				t.Error("expected labels in PUT body")
			}
			jsonResp(w, 200, map[string]any{"iid": 7})
		},
	})

	if err := client.AddLabel("ns/group/repo", 7, "in-progress"); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveLabel(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/issues/7": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 200, map[string]any{"labels": []string{"in-progress", "bug"}})
		},
		"PUT " + projectPath + "/issues/7": func(w http.ResponseWriter, r *http.Request) {
			r.ParseForm()
			labels := r.FormValue("labels")
			if labels != "bug" {
				t.Errorf("expected only 'bug' remaining, got %q", labels)
			}
			jsonResp(w, 200, map[string]any{"iid": 7})
		},
	})

	if err := client.RemoveLabel("ns/group/repo", 7, "in-progress"); err != nil {
		t.Fatal(err)
	}
}

func TestPostIssueComment(t *testing.T) {
	var gotBody string
	client := newTestClient(t, map[string]http.HandlerFunc{
		"POST " + projectPath + "/issues/3/notes": func(w http.ResponseWriter, r *http.Request) {
			r.ParseForm()
			gotBody = r.FormValue("body")
			jsonResp(w, 201, map[string]any{"id": 1})
		},
	})

	if err := client.PostIssueComment("ns/group/repo", 3, "hello from clawflow"); err != nil {
		t.Fatal(err)
	}
	if gotBody != "hello from clawflow" {
		t.Errorf("unexpected comment body: %q", gotBody)
	}
}

func TestCreateIssue(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"POST " + projectPath + "/issues": func(w http.ResponseWriter, r *http.Request) {
			r.ParseForm()
			jsonResp(w, 201, map[string]any{
				"iid":         42,
				"title":       r.FormValue("title"),
				"description": r.FormValue("description"),
			})
		},
	})

	issue, err := client.CreateIssue("ns/group/repo", "new issue", "details")
	if err != nil {
		t.Fatal(err)
	}
	if issue.Number != 42 || issue.Title != "new issue" {
		t.Errorf("unexpected issue: %+v", issue)
	}
}

func TestInitLabels_SkipsExisting(t *testing.T) {
	created := []string{}
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/labels": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 200, []map[string]string{{"name": "existing-label"}})
		},
		"POST " + projectPath + "/labels": func(w http.ResponseWriter, r *http.Request) {
			r.ParseForm()
			created = append(created, r.FormValue("name"))
			jsonResp(w, 201, map[string]string{"name": r.FormValue("name")})
		},
	})

	labels := []vcs.Label{
		{Name: "existing-label", Color: "FF0000", Desc: "already there"},
		{Name: "new-label", Color: "00FF00", Desc: "to be created"},
	}
	if err := client.InitLabels("ns/group/repo", labels); err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0] != "new-label" {
		t.Errorf("expected only 'new-label' created, got %v", created)
	}
}
