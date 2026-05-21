package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// setupTestHome creates a temporary home directory and points the test process
// at it so config/credentials reads and writes don't touch the real user home.
// It also clears token env vars that LoadCredentials merges in.
// Returns a cleanup function.
func setupTestHome(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	// Pre-create the config directory so Save/Load don't fail on mkdir.
	if err := os.MkdirAll(filepath.Join(dir, ".clawflow", "config"), 0o755); err != nil {
		t.Fatalf("setup: mkdir: %v", err)
	}
	origHome := os.Getenv("HOME")
	origGHToken := os.Getenv("GH_TOKEN")
	origGitLabToken := os.Getenv("GITLAB_TOKEN")
	os.Setenv("HOME", dir)
	os.Unsetenv("GH_TOKEN")
	os.Unsetenv("GITLAB_TOKEN")
	return dir, func() {
		os.Setenv("HOME", origHome)
		if origGHToken != "" {
			os.Setenv("GH_TOKEN", origGHToken)
		}
		if origGitLabToken != "" {
			os.Setenv("GITLAB_TOKEN", origGitLabToken)
		}
	}
}

// TestHandleSyncStatus_NoCredentials verifies that the endpoint returns a
// zero-value response (not an error) when no credentials file exists yet.
func TestHandleSyncStatus_NoCredentials(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/sync/status", nil)
	w := httptest.NewRecorder()
	HandleSyncStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp syncStatusResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.GHTokenSet {
		t.Error("gh_token_set should be false when no credentials exist")
	}
	if resp.GistID != "" {
		t.Errorf("gist_id should be empty, got %q", resp.GistID)
	}
}

// TestHandleSyncStatus_MethodNotAllowed verifies that non-GET requests are rejected.
func TestHandleSyncStatus_MethodNotAllowed(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/sync/status", nil)
	w := httptest.NewRecorder()
	HandleSyncStatus(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

// TestHandleSyncPush_NoToken verifies that push returns 400 when no token is configured.
func TestHandleSyncPush_NoToken(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/sync/push", nil)
	w := httptest.NewRecorder()
	HandleSyncPush(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}

	var resp syncPushResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error field")
	}
}

// TestHandleSyncPull_NoToken verifies that pull returns 400 when no token is configured.
func TestHandleSyncPull_NoToken(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/sync/pull", nil)
	w := httptest.NewRecorder()
	HandleSyncPull(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}

	var resp syncPullResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error field")
	}
}

// TestHandleSyncPull_NoGistID verifies that pull returns 400 when a token is
// present but no Gist ID has been stored yet.
func TestHandleSyncPull_NoGistID(t *testing.T) {
	home, cleanup := setupTestHome(t)
	defer cleanup()

	// Write credentials with a token but no gist_id.
	credsYAML := "gh_token: ghp_testtoken\n"
	if err := os.WriteFile(filepath.Join(home, ".clawflow", "config", "credentials.yaml"), []byte(credsYAML), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/sync/pull", nil)
	w := httptest.NewRecorder()
	HandleSyncPull(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}

	var resp syncPullResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error field")
	}
}

// TestHandleLogin_MissingToken verifies that login returns 400 when the
// request body omits the token field.
func TestHandleLogin_MissingToken(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	body, _ := json.Marshal(map[string]string{"token": ""})
	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleLogin(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}

	var resp loginResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == "" {
		t.Error("expected non-empty error field")
	}
}

// TestHandleLogin_InvalidJSON verifies that login returns 400 on malformed JSON.
func TestHandleLogin_InvalidJSON(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewBufferString("{bad json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleLogin(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleLogin_MethodNotAllowed verifies that GET is rejected.
func TestHandleLogin_MethodNotAllowed(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/login", nil)
	w := httptest.NewRecorder()
	HandleLogin(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}
