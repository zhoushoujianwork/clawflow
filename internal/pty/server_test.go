package pty

import (
	"net/http/httptest"
	"testing"
)

func TestCheckOriginGuard(t *testing.T) {
	// Reset package state after the test so other tests (and any future
	// ones) see the default permissive guard.
	orig := originGuard
	t.Cleanup(func() { originGuard = orig })

	// Unconfigured: permissive (backward compatible with embedders that
	// don't install a guard).
	originGuard = nil
	req := httptest.NewRequest("GET", "/ws/pty", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	if !checkOrigin(req) {
		t.Fatal("nil guard should allow any origin")
	}

	// Configured: only whitelisted origins may open the PTY WebSocket.
	ConfigureOriginGuard("127.0.0.1", 8090)

	good := httptest.NewRequest("GET", "/ws/pty", nil)
	good.Header.Set("Origin", "http://127.0.0.1:8090")
	if !checkOrigin(good) {
		t.Error("same-origin WS handshake should be allowed")
	}

	bad := httptest.NewRequest("GET", "/ws/pty", nil)
	bad.Header.Set("Origin", "http://evil.example.com")
	if checkOrigin(bad) {
		t.Error("cross-origin WS handshake must be rejected")
	}

	// Non-browser WS client without an Origin header is allowed.
	noOrigin := httptest.NewRequest("GET", "/ws/pty", nil)
	if !checkOrigin(noOrigin) {
		t.Error("WS handshake without Origin should be allowed")
	}
}
