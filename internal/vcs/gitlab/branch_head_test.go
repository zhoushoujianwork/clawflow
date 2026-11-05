package gitlab_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestBranchHead(t *testing.T) {
	c := newTestClient(t, map[string]http.HandlerFunc{
		"GET /api/v4/projects/o%2Fr/repository/branches/fix%2Fx": func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"id": "abc"}})
		},
		"GET /api/v4/projects/o%2Fr/repository/branches/gone": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(404)
		},
	})
	if head, err := c.BranchHead("o/r", "fix/x"); err != nil || head != "abc" {
		t.Fatalf("existing branch: %q %v", head, err)
	}
	if head, err := c.BranchHead("o/r", "gone"); err != nil || head != "" {
		t.Fatalf("missing branch: %q %v", head, err)
	}
}
