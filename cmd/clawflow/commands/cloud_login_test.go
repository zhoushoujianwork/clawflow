package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestPostCloudJSON_HappyPath verifies the small helper that the
// device-login flow uses to POST against the cloud's auth endpoints. It is
// the only piece worth testing in isolation; the full device flow has
// timing dependencies and is covered end-to-end by the auth package tests.
func TestPostCloudJSON_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"echo": body["k"], "ok": true})
	}))
	defer srv.Close()

	got, err := postCloudJSON(context.Background(), srv.URL, map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("postCloudJSON: %v", err)
	}
	if got["echo"] != "v" || got["ok"] != true {
		t.Fatalf("unexpected response: %#v", got)
	}
}

func TestPostCloudJSON_ErrorIncludesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"missing user_code"}`))
	}))
	defer srv.Close()

	_, err := postCloudJSON(context.Background(), srv.URL, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !contains(err.Error(), "missing user_code") {
		t.Fatalf("error should surface body: %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
