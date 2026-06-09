package gitlab_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

func TestUpdateIssue(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"PUT " + projectPath + "/issues/42": func(w http.ResponseWriter, r *http.Request) {
			r.ParseForm()
			jsonResp(w, 200, map[string]any{
				"iid":         42,
				"title":       r.FormValue("title"),
				"description": r.FormValue("description"),
				"state":       "opened",
			})
		},
	})

	title := "updated title"
	body := "updated body"
	issue, err := client.UpdateIssue("ns/group/repo", 42, vcs.IssueUpdate{Title: &title, Body: &body})
	if err != nil {
		t.Fatal(err)
	}
	if issue.Number != 42 || issue.Title != title || issue.Body != body || issue.State != "open" {
		t.Fatalf("issue = %#v", issue)
	}
}

func TestGetIssue(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/issues/7": func(w http.ResponseWriter, r *http.Request) {
			jsonResp(w, 200, map[string]any{
				"id":          98765,
				"iid":         7,
				"title":       "a bug",
				"description": "something broke",
				"state":       "opened",
				"labels":      []string{"bug", "feat"},
				"web_url":     "https://gitlab.com/ns/group/repo/-/issues/7",
				"created_at":  "2026-01-01T00:00:00Z",
				"updated_at":  "2026-01-02T00:00:00Z",
			})
		},
	})

	issue, err := client.GetIssue("ns/group/repo", 7)
	if err != nil {
		t.Fatal(err)
	}
	if issue == nil {
		t.Fatal("expected issue, got nil")
	}
	if issue.Number != 7 || issue.Title != "a bug" || issue.Body != "something broke" {
		t.Errorf("unexpected issue: %#v", issue)
	}
	if issue.State != "open" {
		t.Errorf("expected normalized state 'open', got %q", issue.State)
	}
	if issue.URL != "https://gitlab.com/ns/group/repo/-/issues/7" {
		t.Errorf("unexpected url: %q", issue.URL)
	}
	if !issue.HasLabel("bug") || !issue.HasLabel("feat") {
		t.Errorf("expected labels bug+feat, got %v", issue.Labels)
	}
}

func TestGetIssueNotFound(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"GET " + projectPath + "/issues/999": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", 404)
		},
	})

	issue, err := client.GetIssue("ns/group/repo", 999)
	if err != nil {
		t.Fatalf("expected nil error on 404, got %v", err)
	}
	if issue != nil {
		t.Fatalf("expected nil issue on 404, got %#v", issue)
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

func TestUploadAttachment(t *testing.T) {
	var gotContentType, gotFormName, gotFileName string
	client := newTestClient(t, map[string]http.HandlerFunc{
		"POST " + projectPath + "/uploads": func(w http.ResponseWriter, r *http.Request) {
			gotContentType = r.Header.Get("Content-Type")
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("parse multipart: %v", err)
			}
			for name, fhs := range r.MultipartForm.File {
				gotFormName = name
				if len(fhs) > 0 {
					gotFileName = fhs[0].Filename
				}
			}
			jsonResp(w, 201, map[string]string{
				"alt":      "shot",
				"url":      "/uploads/abc123/shot.png",
				"markdown": "![shot](/uploads/abc123/shot.png)",
			})
		},
	})

	dir := t.TempDir()
	imgPath := dir + "/shot.png"
	if err := os.WriteFile(imgPath, []byte("\x89PNG fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	md, err := client.UploadAttachment("ns/group/repo", imgPath)
	if err != nil {
		t.Fatal(err)
	}
	if md != "![shot](/uploads/abc123/shot.png)" {
		t.Errorf("unexpected markdown: %q", md)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data") {
		t.Errorf("expected multipart request, got Content-Type %q", gotContentType)
	}
	if gotFormName != "file" {
		t.Errorf("expected form field 'file', got %q", gotFormName)
	}
	if gotFileName != "shot.png" {
		t.Errorf("expected filename 'shot.png', got %q", gotFileName)
	}
}

func TestUploadAttachmentServerError(t *testing.T) {
	client := newTestClient(t, map[string]http.HandlerFunc{
		"POST " + projectPath + "/uploads": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "file too big", http.StatusRequestEntityTooLarge)
		},
	})
	dir := t.TempDir()
	imgPath := dir + "/big.png"
	if err := os.WriteFile(imgPath, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UploadAttachment("ns/group/repo", imgPath); err == nil {
		t.Fatal("expected error on HTTP 413, got nil")
	}
}
