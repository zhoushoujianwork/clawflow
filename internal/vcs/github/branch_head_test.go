package github_test

import (
	"net/http"
	"testing"
)

func TestBranchHead(t *testing.T) {
	c := newTestClient(t, map[string]http.HandlerFunc{
		"GET /repos/o/r/git/ref/heads/fix/x": func(w http.ResponseWriter, _ *http.Request) {
			jsonResp(w, 200, map[string]any{"object": map[string]string{"sha": "abc"}})
		},
		"GET /repos/o/r/git/ref/heads/gone": func(w http.ResponseWriter, _ *http.Request) {
			jsonResp(w, 404, map[string]string{"message": "Not Found"})
		},
		"GET /repos/o/r/git/ref/heads/down": func(w http.ResponseWriter, _ *http.Request) {
			jsonResp(w, 502, map[string]string{"message": "Bad Gateway"})
		},
	})
	if head, err := c.BranchHead("o/r", "fix/x"); err != nil || head != "abc" {
		t.Fatalf("existing branch: %q %v", head, err)
	}
	if head, err := c.BranchHead("o/r", "gone"); err != nil || head != "" {
		t.Fatalf("missing branch: %q %v", head, err)
	}
	if _, err := c.BranchHead("o/r", "down"); err == nil {
		t.Fatal("outage reported as a missing branch")
	}
}
