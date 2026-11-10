package webguard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(t *testing.T, peer, method, path string) (int, bool) {
	t.Helper()
	p, err := NewRemotePolicy("readonly", nil)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = peer
	w := httptest.NewRecorder()
	seen := false
	p.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = ReadOnlySession(r.Context())
		w.WriteHeader(200)
	})).ServeHTTP(w, r)
	return w.Code, seen
}

// The run detail page linked from Usage and Work is reachable read-only, and
// handlers can tell the session is read-only.
func TestReadOnlyRunPageIsReachable(t *testing.T) {
	page := "/runs/grp__smoke/issue-1/2026-10-27T11-00-00Z"
	if code, ro := serve(t, "192.168.1.2:10", "GET", page); code != 200 || !ro {
		t.Fatalf("lan GET %s = %d readonly=%v", page, code, ro)
	}
	if code, ro := serve(t, "127.0.0.1:10", "GET", page); code != 200 || ro {
		t.Fatalf("loopback must stay a full session: %d readonly=%v", code, ro)
	}
}

// Only the page is admitted. The raw transcript, other run data and every write stay denied.
func TestReadOnlyRunPageDoesNotOpenRunData(t *testing.T) {
	base := "/runs/grp__smoke/issue-1/2026-10-27T11-00-00Z"
	for _, tc := range []struct{ method, path string }{
		{"GET", "/data/runs/grp__smoke/issue-1/2026-10-27T11-00-00Z/events.jsonl"},
		{"GET", "/data/runs/grp__smoke/issue-1/2026-10-27T11-00-00Z/meta.json"},
		{"GET", "/data/projects.json"},
		{"GET", base + "/events.jsonl"},
		{"GET", "/runs/../settings"},
		{"GET", "/runs/grp__smoke/issue-1/../../../etc/passwd"},
		{"GET", "/runs/grp__smoke/issue-x/2026-10-27T11-00-00Z"},
		{"GET", "/runs/grp__smoke/issue-1/latest"},
		{"GET", "/runs"},
		{"POST", base},
		{"PUT", base},
		{"DELETE", base},
		{"GET", "/settings"},
		{"GET", "/api/settings"},
		{"GET", "/api/settings/reveal"},
		{"GET", "/api/providers"},
		{"GET", "/api/browse-directory"},
		{"GET", "/api/chat/spawn"},
		{"GET", "/ws/pty"},
		{"POST", "/api/run"},
		{"POST", "/api/labels/add"},
		{"POST", "/api/repo/config"},
		{"POST", "/api/project/bind"},
	} {
		if code, _ := serve(t, "192.168.1.2:10", tc.method, tc.path); code != 403 {
			t.Errorf("%s %s = %d, want 403", tc.method, tc.path, code)
		}
	}
}
