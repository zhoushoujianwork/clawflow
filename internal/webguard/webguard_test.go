package webguard

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newGuard builds the default loopback guard used by `clawflow web` on
// its 127.0.0.1:8090 default.
func newGuard() *Guard { return New("127.0.0.1", 8090) }

// serve runs a request through guard.Middleware wrapping a handler that
// records whether it was reached, and returns (statusCode, reached).
func serve(t *testing.T, g *Guard, req *http.Request) (int, bool) {
	t.Helper()
	reached := false
	h := g.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, reached
}

func TestSameOriginPostAllowed(t *testing.T) {
	g := newGuard()
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8090/api/run", nil)
	req.Host = "127.0.0.1:8090"
	req.Header.Set("Origin", "http://127.0.0.1:8090")
	if code, reached := serve(t, g, req); !reached || code != http.StatusOK {
		t.Fatalf("same-origin POST: want pass-through 200, got code=%d reached=%v", code, reached)
	}
}

func TestSameOriginLocalhostVariantAllowed(t *testing.T) {
	g := newGuard()
	// Dashboard opened at localhost:8090 instead of 127.0.0.1.
	req := httptest.NewRequest(http.MethodPost, "/api/run", nil)
	req.Host = "localhost:8090"
	req.Header.Set("Origin", "http://localhost:8090")
	if code, reached := serve(t, g, req); !reached || code != http.StatusOK {
		t.Fatalf("localhost variant: want 200, got code=%d reached=%v", code, reached)
	}
}

func TestCrossOriginPostRejected(t *testing.T) {
	g := newGuard()
	// Malicious page firing a text/plain "simple" POST.
	req := httptest.NewRequest(http.MethodPost, "/api/chat/spawn", nil)
	req.Host = "127.0.0.1:8090"
	req.Header.Set("Origin", "http://evil.example.com")
	if code, reached := serve(t, g, req); reached || code != http.StatusForbidden {
		t.Fatalf("cross-origin POST: want 403 blocked, got code=%d reached=%v", code, reached)
	}
}

func TestHostMismatchRejected(t *testing.T) {
	g := newGuard()
	// DNS-rebinding: the attacker's domain resolved to 127.0.0.1, so the
	// connection lands here but carries the attacker's Host.
	req := httptest.NewRequest(http.MethodGet, "/api/settings/reveal", nil)
	req.Host = "attacker.example.com"
	if code, reached := serve(t, g, req); reached || code != http.StatusForbidden {
		t.Fatalf("rebinding Host: want 403 blocked, got code=%d reached=%v", code, reached)
	}
}

func TestEmptyHostRejectedWhenEnforcing(t *testing.T) {
	g := newGuard()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = ""
	if code, reached := serve(t, g, req); reached || code != http.StatusForbidden {
		t.Fatalf("empty Host: want 403 blocked, got code=%d reached=%v", code, reached)
	}
}

func TestNoOriginCurlAllowed(t *testing.T) {
	g := newGuard()
	// curl / CLI scripts: valid Host, no Origin header → must pass.
	req := httptest.NewRequest(http.MethodPost, "/api/run", nil)
	req.Host = "127.0.0.1:8090"
	if code, reached := serve(t, g, req); !reached || code != http.StatusOK {
		t.Fatalf("no-Origin curl: want 200 pass-through, got code=%d reached=%v", code, reached)
	}
}

func TestLANHostAllowed(t *testing.T) {
	g := New("192.168.1.50", 8090)
	req := httptest.NewRequest(http.MethodPost, "/api/run", nil)
	req.Host = "192.168.1.50:8090"
	req.Header.Set("Origin", "http://192.168.1.50:8090")
	if code, reached := serve(t, g, req); !reached || code != http.StatusOK {
		t.Fatalf("LAN host same-origin: want 200, got code=%d reached=%v", code, reached)
	}
	// Loopback still trusted even when bound to a LAN IP.
	if !g.HostAllowed("127.0.0.1:8090") {
		t.Fatal("LAN bind should still trust loopback Host")
	}
}

func TestWildcardHostDisablesHostCheck(t *testing.T) {
	g := New("0.0.0.0", 8090)
	if g.enforceHost {
		t.Fatal("0.0.0.0 bind must not enforce Host whitelist")
	}
	// Any Host passes...
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "whatever.example.com"
	if code, reached := serve(t, g, req); !reached || code != http.StatusOK {
		t.Fatalf("wildcard bind GET: want 200, got code=%d reached=%v", code, reached)
	}
	// ...but Origin/CSRF is still enforced on writes.
	req2 := httptest.NewRequest(http.MethodPost, "/api/run", nil)
	req2.Host = "whatever.example.com"
	req2.Header.Set("Origin", "http://evil.example.com")
	if code, reached := serve(t, g, req2); reached || code != http.StatusForbidden {
		t.Fatalf("wildcard bind cross-origin POST: want 403, got code=%d reached=%v", code, reached)
	}
}

func TestIPv6LoopbackOriginAllowed(t *testing.T) {
	g := newGuard()
	if !g.OriginAllowed("http://[::1]:8090") {
		t.Fatal("IPv6 loopback origin should be whitelisted (bracketed)")
	}
	if !g.HostAllowed("[::1]:8090") {
		t.Fatal("IPv6 loopback Host should be whitelisted (bracketed)")
	}
}

func TestTrustedHostsEnv(t *testing.T) {
	t.Setenv(TrustedHostsEnv, "proxy.internal, dash.example.com:9000")
	g := New("127.0.0.1", 8090)

	// Bare host gets both the bind port and the bare form.
	if !g.HostAllowed("proxy.internal:8090") {
		t.Error("trusted bare host should match at bind port")
	}
	if !g.HostAllowed("proxy.internal") {
		t.Error("trusted bare host should match without port (80/443 proxy)")
	}
	// Explicit host:port trusted verbatim.
	if !g.HostAllowed("dash.example.com:9000") {
		t.Error("trusted host:port should match verbatim")
	}
	if !g.OriginAllowed("https://dash.example.com:9000") {
		t.Error("trusted host should accept https origin")
	}
	// Unrelated host still rejected.
	if g.HostAllowed("other.example.com:8090") {
		t.Error("untrusted host must stay blocked")
	}
}

func TestWSOriginCheckLogic(t *testing.T) {
	// Mirrors the /ws/pty CheckOrigin path: whitelist via OriginAllowed.
	g := newGuard()
	cases := []struct {
		origin string
		want   bool
	}{
		{"http://127.0.0.1:8090", true},
		{"http://localhost:8090", true},
		{"http://evil.example.com", false},
		{"", true}, // non-browser WS client
	}
	for _, c := range cases {
		if got := g.OriginAllowed(c.origin); got != c.want {
			t.Errorf("OriginAllowed(%q) = %v, want %v", c.origin, got, c.want)
		}
	}
}
