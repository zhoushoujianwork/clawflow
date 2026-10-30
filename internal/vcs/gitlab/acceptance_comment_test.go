package gitlab_test

import (
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"net/http"
	"testing"
	"time"
)

func TestPublishAcceptanceCommentUsesOwnedPRComment(t *testing.T) {
	body := ""
	posts, updates := 0, 0
	c := newTestClient(t, map[string]http.HandlerFunc{
		"GET /api/v4/user": func(w http.ResponseWriter, r *http.Request) { jsonResp(w, 200, map[string]string{"username": "bot"}) },
		"GET /api/v4/projects/o%2Fr/merge_requests/1/notes": func(w http.ResponseWriter, r *http.Request) {
			notes := []any{}
			if body != "" {
				notes = append(notes, map[string]any{"id": 7, "body": body, "author": map[string]string{"username": "bot"}})
			}
			jsonResp(w, 200, notes)
		},
		"POST /api/v4/projects/o%2Fr/merge_requests/1/notes": func(w http.ResponseWriter, r *http.Request) {
			posts++
			r.ParseForm()
			body = r.Form.Get("body")
			jsonResp(w, 201, map[string]int{"id": 7})
		},
		"PUT /api/v4/projects/o%2Fr/merge_requests/1/notes/7": func(w http.ResponseWriter, r *http.Request) {
			updates++
			r.ParseForm()
			body = r.Form.Get("body")
			jsonResp(w, 200, map[string]int{"id": 7})
		},
	})
	now := time.Now().UTC()
	report := vcs.AcceptanceComment{Attempt: "a", Contract: "c", Head: "head", Started: now, Completed: now.Add(time.Minute), Body: "first"}
	if err := c.PublishAcceptanceComment("o/r", 1, report); err != nil {
		t.Fatal(err)
	}
	if err := c.PublishAcceptanceComment("o/r", 1, report); err != nil {
		t.Fatal(err)
	}
	report.Attempt = "b"
	report.Started = now.Add(2 * time.Minute)
	report.Completed = now.Add(3 * time.Minute)
	report.Body = "second"
	if err := c.PublishAcceptanceComment("o/r", 1, report); err != nil {
		t.Fatal(err)
	}
	if posts != 1 || updates != 1 {
		t.Fatalf("posts=%d updates=%d", posts, updates)
	}
}
