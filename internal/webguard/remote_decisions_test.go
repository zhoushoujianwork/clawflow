package webguard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The decisions list is read-only for LAN viewers: GET and HEAD pass, every
// write is denied with a JSON body, and the CIDR allowlist still applies.
func TestRemotePolicyDecisionsIsReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name, peer, method string
		cidrs              []string
		want               int
	}{
		{"lan read", "192.168.1.2:10", "GET", nil, 200},
		{"lan head", "192.168.1.2:10", "HEAD", nil, 200},
		{"lan post", "192.168.1.2:10", "POST", nil, 403},
		{"lan put", "192.168.1.2:10", "PUT", nil, 403},
		{"lan delete", "192.168.1.2:10", "DELETE", nil, 403},
		{"cidr allowed", "192.168.1.2:10", "GET", []string{"192.168.0.0/16"}, 200},
		{"cidr denied", "192.168.1.2:10", "GET", []string{"10.0.0.0/8"}, 403},
		{"loopback write", "127.0.0.1:10", "POST", nil, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewRemotePolicy("readonly", tc.cidrs)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(tc.method, "/api/decisions", nil)
			r.RemoteAddr = tc.peer
			w := httptest.NewRecorder()
			p.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })).ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
			if tc.want == 403 && w.Header().Get("Content-Type") != "application/json" {
				t.Fatal("denial must be JSON")
			}
		})
	}
}
