package github_test

import (
	"net/http"
	"testing"
)

func TestListPRFeedbackReadsReviewsInlineAndConversation(t *testing.T) {
	user := map[string]string{"login": "alice", "type": "User"}
	c := newTestClient(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/pulls/7/reviews": func(w http.ResponseWriter, _ *http.Request) {
			jsonResp(w, 200, []map[string]any{{"id": 1, "user": user, "body": "needs work", "state": "CHANGES_REQUESTED", "commit_id": "abc", "submitted_at": "t1"}})
		},
		"GET /repos/o/r/pulls/7/comments": func(w http.ResponseWriter, _ *http.Request) {
			jsonResp(w, 200, []map[string]any{
				{"id": 2, "user": user, "body": "rename", "path": "a.go", "line": 0, "original_line": 9, "commit_id": "abc", "created_at": "t2"},
			})
		},
		"GET /repos/o/r/issues/7/comments": func(w http.ResponseWriter, _ *http.Request) {
			jsonResp(w, 200, []map[string]any{{"id": 3, "user": map[string]string{"login": "ci[bot]", "type": "Bot"}, "body": "hi", "created_at": "t3"}})
		},
	})
	got, err := c.ListPRFeedback("o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d items: %+v", len(got), got)
	}
	if got[0].ID != "review:1" || got[0].Kind != "review" || got[0].State != "CHANGES_REQUESTED" || got[0].Author != "alice" {
		t.Fatalf("review: %+v", got[0])
	}
	if got[1].ID != "inline:2" || got[1].Path != "a.go" || got[1].Line != 9 {
		t.Fatalf("inline: %+v", got[1])
	}
	if got[2].ID != "comment:3" || got[2].AuthorType != "Bot" {
		t.Fatalf("comment: %+v", got[2])
	}
}

func TestListPRFeedbackFailsWholeReadOnPageError(t *testing.T) {
	c := newTestClient(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/pulls/7/reviews":  func(w http.ResponseWriter, _ *http.Request) { jsonResp(w, 200, []any{}) },
		"GET /repos/o/r/pulls/7/comments": func(w http.ResponseWriter, _ *http.Request) { jsonResp(w, 502, map[string]string{"message": "x"}) },
	})
	if _, err := c.ListPRFeedback("o/r", 7); err == nil {
		t.Fatal("partial read reported as success")
	}
}
