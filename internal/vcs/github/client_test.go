package github_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
)

func newTestClient(t *testing.T, routes map[string]http.HandlerFunc) *github.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		if h, ok := routes[key]; ok {
			h(w, r)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		http.Error(w, "not found", 404)
	}))
	t.Cleanup(srv.Close)
	return github.New("test-token", srv.URL)
}

func jsonResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func TestListOpenIssues(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET /repos/owner/repo/issues": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 200, []map[string]any{
				{"number": 1, "title": "real issue", "body": "body", "labels": []map[string]string{{"name": "bug"}}},
				{"number": 2, "title": "a PR", "body": "", "pull_request": map[string]any{}},
			})
		},
	})

	issues, err := client.ListOpenIssues("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue (PR filtered), got %d", len(issues))
	}
	if issues[0].Number != 1 || issues[0].Title != "real issue" {
		t.Errorf("unexpected issue: %+v", issues[0])
	}
	if !issues[0].HasLabel("bug") {
		t.Error("expected label 'bug'")
	}
}

func TestPRExistsForIssue(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET /repos/owner/repo/pulls": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 200, []map[string]any{
				{"number": 10, "title": "fix", "body": "Fixes #5", "state": "open", "head": map[string]string{"ref": "main"}},
			})
		},
	})

	exists, err := client.PRExistsForIssue("owner/repo", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("expected PR to exist for issue #5")
	}

	exists, _ = client.PRExistsForIssue("owner/repo", 99)
	if exists {
		t.Error("expected no PR for issue #99")
	}
}

func TestAddLabel(t *testing.T) {
	var gotLabels []string
	client := newTestClient(t, map[string]http.HandlerFunc{
		"POST /repos/owner/repo/issues/7/labels": func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Labels []string `json:"labels"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			gotLabels = body.Labels
			jsonResp(w, 200, []map[string]string{})
		},
	})

	if err := client.AddLabel("owner/repo", 7, "in-progress", "bug"); err != nil {
		t.Fatal(err)
	}
	if len(gotLabels) != 2 {
		t.Errorf("expected 2 labels, got %v", gotLabels)
	}
}

func TestRemoveLabel(t *testing.T) {
	removed := []string{}
	client := newTestClient(t, map[string]http.HandlerFunc{
		"DELETE /repos/owner/repo/issues/7/labels/in-progress": func(w http.ResponseWriter, r *http.Request) {
			removed = append(removed, "in-progress")
			w.WriteHeader(204)
		},
	})

	if err := client.RemoveLabel("owner/repo", 7, "in-progress"); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "in-progress" {
		t.Errorf("unexpected removed: %v", removed)
	}
}

func TestPostIssueComment(t *testing.T) {
	var gotBody string
	client := newTestClient(t, map[string]http.HandlerFunc{
		"POST /repos/owner/repo/issues/3/comments": func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Body string `json:"body"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			gotBody = body.Body
			jsonResp(w, 201, map[string]any{"id": 1})
		},
	})

	if err := client.PostIssueComment("owner/repo", 3, "hello from clawflow"); err != nil {
		t.Fatal(err)
	}
	if gotBody != "hello from clawflow" {
		t.Errorf("unexpected comment body: %q", gotBody)
	}
}

func TestCreateIssue(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"POST /repos/owner/repo/issues": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 201, map[string]any{"number": 42, "title": "new issue", "body": "details"})
		},
	})

	issue, err := client.CreateIssue("owner/repo", "new issue", "details")
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
		"GET /repos/owner/repo/labels": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 200, []map[string]string{{"name": "existing-label"}})
		},
		"POST /repos/owner/repo/labels": func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Name string `json:"name"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			created = append(created, body.Name)
			jsonResp(w, 201, map[string]string{"name": body.Name})
		},
	})

	labels := []vcs.Label{
		{Name: "existing-label", Color: "FF0000", Desc: "already there"},
		{Name: "new-label", Color: "00FF00", Desc: "to be created"},
	}
	if err := client.InitLabels("owner/repo", labels); err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0] != "new-label" {
		t.Errorf("expected only 'new-label' created, got %v", created)
	}
}
