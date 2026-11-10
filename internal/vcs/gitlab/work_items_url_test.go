package gitlab

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// GitLab 18+ returns /-/work_items/N as an issue's web_url. ClawFlow reads and
// writes issues through the Issues API by project path and iid only, so the
// web_url is informational. This pins that no request depends on it.
func TestIssuesAPIIgnoresWorkItemsWebURL(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.EscapedPath(), "/issues/1"):
			_, _ = w.Write([]byte(`{"id":99,"iid":1,"title":"greeting","description":"body","state":"opened","labels":["feat"],` +
				`"web_url":"https://gitlab.example.test/grp/smoke/-/work_items/1","author":{"username":"alice"},"created_at":"2026-10-27T10:00:00Z","updated_at":"2026-10-27T10:00:00Z"}`))
		case r.Method == "GET" && strings.Contains(r.URL.EscapedPath(), "/issues"):
			_, _ = w.Write([]byte(`[{"id":99,"iid":1,"title":"greeting","description":"body","labels":["feat"],` +
				`"web_url":"https://gitlab.example.test/grp/smoke/-/work_items/1","author":{"username":"alice"},"created_at":"2026-10-27T10:00:00Z","updated_at":"2026-10-27T10:00:00Z"}]`))
		case r.Method == "POST" && strings.HasSuffix(r.URL.EscapedPath(), "/issues/1/notes"):
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":5}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	c := New("token", srv.URL)
	list, err := c.ListOpenIssues("grp/smoke")
	if err != nil || len(list) != 1 || list[0].Number != 1 || list[0].State != "open" {
		t.Fatalf("list: %+v %v", list, err)
	}
	one, err := c.GetIssue("grp/smoke", 1)
	if err != nil || one == nil || one.Number != 1 || one.Title != "greeting" || one.URL != "https://gitlab.example.test/grp/smoke/-/work_items/1" {
		t.Fatalf("get: %+v %v", one, err)
	}
	if err := c.PostIssueComment("grp/smoke", 1, "hello"); err != nil {
		t.Fatalf("comment: %v", err)
	}
	for _, p := range paths {
		if strings.Contains(p, "work_items") {
			t.Errorf("request used a Work Items route: %s", p)
		}
		if !strings.Contains(p, "/api/v4/projects/grp%2Fsmoke/issues") {
			t.Errorf("unexpected API route: %s", p)
		}
	}
}
