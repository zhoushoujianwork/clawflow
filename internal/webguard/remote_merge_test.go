package webguard

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// A LAN request must be marked read-only for handlers (reduced views) and its
// response must also pass through the redactor, for both the run detail page
// family and the job observation family.
func TestReadOnlySessionAndRedactionApplyTogether(t *testing.T) {
	p, err := NewRemotePolicy("readonly", nil)
	if err != nil {
		t.Fatal(err)
	}
	p.WithRedactor(testRedactor())
	for _, path := range []string{
		"/runs/grp__smoke/issue-1/2026-10-27T11-00-00Z",
		"/api/decisions",
		"/api/jobs",
		"/api/jobs/0123abcd/logs",
		"/runners",
	} {
		var seen bool
		w := lanServe(t, p, "192.168.1.2:1", "GET", path, func(w http.ResponseWriter, r *http.Request) {
			seen = ReadOnlySession(r.Context())
			io.WriteString(w, "cwd /Users/bob/x key configured-secret-value")
		})
		if w.Code != 200 || !seen {
			t.Errorf("%s: status=%d readonly=%v", path, w.Code, seen)
		}
		if body := w.Body.String(); strings.Contains(body, "bob") || strings.Contains(body, "configured-secret-value") {
			t.Errorf("%s leaked to LAN: %s", path, body)
		}
	}

	// The machine's own session is neither marked read-only nor redacted.
	var seen bool
	w := lanServe(t, p, "127.0.0.1:1", "GET", "/api/jobs", func(w http.ResponseWriter, r *http.Request) {
		seen = ReadOnlySession(r.Context())
		io.WriteString(w, "/Users/bob/x")
	})
	if seen || w.Body.String() != "/Users/bob/x" {
		t.Fatalf("loopback seen=%v body=%q", seen, w.Body.String())
	}
}
