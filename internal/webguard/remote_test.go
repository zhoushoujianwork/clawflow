package webguard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRemotePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, peer, method, path, forwarded string
		cidrs                               []string
		want                                int
	}{
		{"loopback write", "127.0.0.1:10", "POST", "/api/settings", "", nil, 200},
		{"ipv6 loopback", "[::1]:10", "DELETE", "/api/settings", "", nil, 200},
		{"mapped loopback", "[::ffff:127.0.0.1]:10", "PUT", "/api/settings", "", nil, 200},
		{"pipeline", "192.168.1.2:10", "GET", "/api/pipeline", "", nil, 200},
		{"head", "192.168.1.2:10", "HEAD", "/data/issues.json", "", nil, 200},
		{"post", "192.168.1.2:10", "POST", "/api/pipeline", "", nil, 403},
		{"put", "192.168.1.2:10", "PUT", "/api/pipeline", "", nil, 403},
		{"delete", "192.168.1.2:10", "DELETE", "/api/pipeline", "", nil, 403},
		{"settings", "192.168.1.2:10", "GET", "/api/settings", "", nil, 403},
		{"provider", "192.168.1.2:10", "GET", "/api/providers", "", nil, 403},
		{"chat", "192.168.1.2:10", "GET", "/api/chat/spawn", "", nil, 403},
		{"browse", "192.168.1.2:10", "GET", "/api/browse-directory", "", nil, 403},
		{"websocket", "192.168.1.2:10", "GET", "/ws/pty", "", nil, 403},
		{"raw data", "192.168.1.2:10", "GET", "/data/web.pid", "", nil, 403},
		{"directory", "192.168.1.2:10", "GET", "/data/", "", nil, 403},
		{"unknown", "192.168.1.2:10", "GET", "/api/future", "", nil, 403},
		{"traversal", "192.168.1.2:10", "GET", "/assets/../data/web.pid", "", nil, 403},
		{"allowed cidr", "192.168.1.2:10", "GET", "/api/pipeline", "", []string{"10.0.0.0/8", "192.168.0.0/16"}, 200},
		{"denied cidr", "192.168.1.2:10", "GET", "/", "", []string{"10.0.0.0/8"}, 403},
		{"loopback bypass cidr", "[::1]:10", "POST", "/api/run", "", []string{"10.0.0.0/8"}, 200},
		{"spoof loopback", "192.168.1.2:10", "POST", "/api/run", "127.0.0.1", nil, 403},
		{"spoof cidr", "192.168.1.2:10", "GET", "/", "10.0.0.1", []string{"10.0.0.0/8"}, 403},
		{"invalid peer", "invalid", "GET", "/", "", nil, 403},
		{"scoped ipv6 read", "[fe80::1234%en0]:12345", "GET", "/api/pipeline", "", nil, 200},
		{"scoped ipv6 head", "[fe80::1234%en0]:12345", "HEAD", "/api/pipeline", "", nil, 200},
		{"scoped ipv6 cidr allowed", "[fe80::1234%en0]:12345", "GET", "/api/pipeline", "", []string{"fe80::/10"}, 200},
		{"scoped ipv6 cidr denied", "[fe80::1234%en0]:12345", "GET", "/api/pipeline", "", []string{"fd00::/8"}, 403},
		{"scoped ipv6 write denied", "[fe80::1234%en0]:12345", "POST", "/api/run", "127.0.0.1", nil, 403},
		{"scoped ipv6 sensitive denied", "[fe80::1234%en0]:12345", "GET", "/api/settings", "", nil, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewRemotePolicy("readonly", tc.cidrs)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest(tc.method, tc.path, nil)
			r.RemoteAddr = tc.peer
			r.Header.Set("X-Forwarded-For", tc.forwarded)
			r.Header.Set("X-Real-IP", tc.forwarded)
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

func TestRemoteSessionAndOff(t *testing.T) {
	for _, mode := range []string{"off", "readonly"} {
		p, _ := NewRemotePolicy(mode, nil)
		for _, peer := range []string{"127.0.0.1:10", "192.168.1.2:10"} {
			r := httptest.NewRequest("GET", "/api/session", nil)
			r.RemoteAddr = peer
			w := httptest.NewRecorder()
			p.Middleware(http.HandlerFunc(p.Session)).ServeHTTP(w, r)
			want := "{\"readonly\":false}\n"
			if mode == "readonly" && peer == "192.168.1.2:10" {
				want = "{\"readonly\":true}\n"
			}
			if w.Body.String() != want {
				t.Fatalf("session=%s want=%s", w.Body.String(), want)
			}
		}
	}
	for _, mode := range []string{"invalid", "off"} {
		if _, err := NewRemotePolicy(mode, []string{"10.0.0.0/8"}); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
	if _, err := NewRemotePolicy("readonly", []string{"bad"}); err == nil {
		t.Fatal("invalid CIDR accepted")
	}
}

func TestRemoteHeadAndOffCompatibility(t *testing.T) {
	for _, mode := range []string{"readonly", "off"} {
		p, _ := NewRemotePolicy(mode, nil)
		method, path := "HEAD", "/api/pipeline"
		if mode == "off" {
			method, path = "POST", "/api/settings"
		}
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = "192.168.1.2:10"
		w := httptest.NewRecorder()
		p.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if mode == "readonly" && r.Method != "GET" {
				t.Fatal("HEAD not mapped to read")
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("{}"))
		})).ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		if mode == "readonly" && w.Body.Len() != 0 {
			t.Fatal("HEAD included body")
		}
		if mode == "off" && w.Body.String() != "{}" {
			t.Fatal("off changed existing behavior")
		}
	}
}

func TestRemoteReadAllowlist(t *testing.T) {
	p, _ := NewRemotePolicy("readonly", nil)
	for _, path := range []string{"/", "/dashboard", "/pipeline", "/usage", "/assets/app.js", "/favicon.svg", "/api/session", "/api/pipeline", "/api/run/status", "/api/runs", "/api/usage", "/data/meta.json", "/data/repos.json", "/data/pending.json", "/data/issues.json"} {
		t.Run(path, func(t *testing.T) {
			r := httptest.NewRequest("GET", path, nil)
			r.RemoteAddr = "[fd00::1]:1234"
			w := httptest.NewRecorder()
			p.Middleware(New("0.0.0.0", 8090).Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))).ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("%s: %d", path, w.Code)
			}
		})
	}
}

func TestRemoteIPv6CIDR(t *testing.T) {
	p, _ := NewRemotePolicy("readonly", []string{"fd00::/8"})
	for _, peer := range []string{"[fd00::2]:10", "[2001:db8::1]:10"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = peer
		w := httptest.NewRecorder()
		p.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })).ServeHTTP(w, r)
		want := 200
		if peer == "[2001:db8::1]:10" {
			want = 403
		}
		if w.Code != want {
			t.Fatalf("%s: %d", peer, w.Code)
		}
	}
}
