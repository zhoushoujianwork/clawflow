package cloud

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/zhoushoujianwork/clawflow/internal/operator"
)

const testWebhookSecret = "test-webhook-secret-abc123"

// buildRegistry creates an operator.Registry with a single embedded evaluate-bug
// operator for testing. The operator triggers on issues that carry the "bug"
// label and have not yet been evaluated.
func buildRegistry(t *testing.T) *operator.Registry {
	t.Helper()
	skill := `---
name: evaluate-bug
description: "Evaluate a bug report"
operator:
  trigger:
    target: "issue"
    labels_required: ["bug"]
    labels_excluded: ["agent-evaluated", "agent-running"]
  lock_label: "agent-running"
  outcomes: ["agent-evaluated", "agent-skipped"]
---

Evaluate this bug.
`
	sys := fstest.MapFS{
		"skills/evaluate-bug/SKILL.md": &fstest.MapFile{Data: []byte(skill)},
	}
	reg := operator.NewRegistry()
	if err := reg.LoadEmbedded(sys, "skills"); err != nil {
		t.Fatalf("build registry: %v", err)
	}
	return reg
}

// githubSig returns the X-Hub-Signature-256 header value for body + secret.
func githubSig(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// newWebhookServer wires a test HTTP server with one VCSConnection already
// registered. The connection uses testWebhookSecret.
func newWebhookServer(t *testing.T, reg *operator.Registry) (*httptest.Server, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	_, err := store.RegisterConnection(VCSConnection{
		Repo:     "acme/backend",
		Platform: "github",
		GitHubApp: &GitHubAppInstallation{
			AppID:          42,
			InstallationID: 99,
			WebhookSecret:  testWebhookSecret,
		},
	})
	if err != nil {
		t.Fatalf("register connection: %v", err)
	}
	srv := httptest.NewServer(NewServer(store, reg))
	return srv, store
}

// postWebhook sends a POST to /api/v1/github/app/webhook with the given event type,
// payload, and signature. Returns the response.
func postWebhook(t *testing.T, srv *httptest.Server, eventType string, payload any, sig string) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/github/app/webhook", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", eventType)
	req.Header.Set("X-Hub-Signature-256", sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

// issuesLabeledPayload builds a minimal GitHub issues.labeled event payload.
func issuesLabeledPayload(repo string, number int, labels []string) map[string]any {
	ghLabels := make([]map[string]string, len(labels))
	for i, l := range labels {
		ghLabels[i] = map[string]string{"name": l}
	}
	return map[string]any{
		"action": "labeled",
		"issue": map[string]any{
			"number": number,
			"state":  "open",
			"labels": ghLabels,
		},
		"repository": map[string]any{
			"full_name": repo,
		},
	}
}

// TestWebhookGitHub_IssueLabeledTrigger verifies that a "bug"-labeled issue
// event produces exactly one pending job for the evaluate-bug operator.
func TestWebhookGitHub_IssueLabeledTrigger(t *testing.T) {
	reg := buildRegistry(t)
	srv, store := newWebhookServer(t, reg)
	defer srv.Close()

	payload := issuesLabeledPayload("acme/backend", 7, []string{"bug"})
	body, _ := json.Marshal(payload)
	sig := githubSig(body, testWebhookSecret)

	resp := postWebhook(t, srv, "issues", payload, sig)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	// Exactly one pending job should exist for evaluate-bug.
	expectedKey := "github:acme/backend:issue:7:operator:evaluate-bug"
	var found *JobRecord
	store.mu.Lock()
	for _, j := range store.Jobs {
		if j.Spec.DedupeKey == expectedKey {
			found = j
		}
	}
	store.mu.Unlock()
	if found == nil {
		t.Fatal("expected job with dedupe key not found in store")
	}
	if found.Status != JobStatusPending {
		t.Fatalf("job status = %q, want %q", found.Status, JobStatusPending)
	}
	if found.Spec.Operator != "evaluate-bug" {
		t.Fatalf("job operator = %q, want evaluate-bug", found.Spec.Operator)
	}
	if found.Spec.Number != 7 {
		t.Fatalf("job number = %d, want 7", found.Spec.Number)
	}
}

// TestWebhookGitHub_DuplicateDelivery verifies that sending the same event
// twice does not create a second pending job (dedupe).
func TestWebhookGitHub_DuplicateDelivery(t *testing.T) {
	reg := buildRegistry(t)
	srv, store := newWebhookServer(t, reg)
	defer srv.Close()

	payload := issuesLabeledPayload("acme/backend", 8, []string{"bug"})
	body, _ := json.Marshal(payload)
	sig := githubSig(body, testWebhookSecret)

	for range 2 {
		resp := postWebhook(t, srv, "issues", payload, sig)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", resp.StatusCode)
		}
	}

	// Count jobs for this issue.
	store.mu.Lock()
	var count int
	for _, j := range store.Jobs {
		if j.Spec.Number == 8 && j.Spec.Repo == "acme/backend" {
			count++
		}
	}
	store.mu.Unlock()
	if count != 1 {
		t.Fatalf("duplicate delivery created %d jobs, want 1", count)
	}
}

// TestWebhookGitHub_BadSignature verifies that a delivery with a wrong
// signature is rejected with 401 and no job is created.
func TestWebhookGitHub_BadSignature(t *testing.T) {
	reg := buildRegistry(t)
	srv, store := newWebhookServer(t, reg)
	defer srv.Close()

	payload := issuesLabeledPayload("acme/backend", 9, []string{"bug"})
	body, _ := json.Marshal(payload)
	_ = body
	// Deliberately use the wrong secret.
	badSig := githubSig([]byte(`{"action":"labeled"}`), "wrong-secret")

	resp := postWebhook(t, srv, "issues", payload, badSig)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	// Store must remain untouched.
	store.mu.Lock()
	jobCount := len(store.Jobs)
	store.mu.Unlock()
	if jobCount != 0 {
		t.Fatalf("bad-sig delivery created %d jobs, want 0", jobCount)
	}
}

// TestWebhookGitHub_UnknownEvent verifies that unsupported GitHub event types
// are silently ignored with 204 and no job is enqueued.
func TestWebhookGitHub_UnknownEvent(t *testing.T) {
	reg := buildRegistry(t)
	srv, store := newWebhookServer(t, reg)
	defer srv.Close()

	payload := map[string]any{"zen": "Speak like a human.", "hook_id": 1}
	body, _ := json.Marshal(payload)
	sig := githubSig(body, testWebhookSecret)

	resp := postWebhook(t, srv, "ping", payload, sig)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	store.mu.Lock()
	jobCount := len(store.Jobs)
	store.mu.Unlock()
	if jobCount != 0 {
		t.Fatalf("ping event created %d jobs, want 0", jobCount)
	}
}

// TestWebhookGitHub_BadSigBodyNotInResponse verifies that handler error
// responses do not contain the request body or the signature value.
func TestWebhookGitHub_BadSigBodyNotInResponse(t *testing.T) {
	reg := buildRegistry(t)
	srv, _ := newWebhookServer(t, reg)
	defer srv.Close()

	secretPayload := `{"secret_data":"super-sensitive","action":"labeled","issue":{"number":10,"labels":[{"name":"bug"}]},"repository":{"full_name":"acme/backend"}}`
	badSig := "sha256=deadbeef"

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/github/app/webhook", bytes.NewBufferString(secretPayload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-Hub-Signature-256", badSig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	responseBody := buf.String()

	if bytes.Contains([]byte(responseBody), []byte("secret_data")) {
		t.Errorf("response body contains request payload: %s", responseBody)
	}
	if bytes.Contains([]byte(responseBody), []byte(testWebhookSecret)) {
		t.Errorf("response body contains webhook secret")
	}
	if bytes.Contains([]byte(responseBody), []byte("deadbeef")) {
		t.Errorf("response body contains signature value: %s", responseBody)
	}
}

// TestWebhookGitHub_NoTriggerMatch verifies that an issue with labels that
// don't match any operator produces no jobs (but still returns 204).
func TestWebhookGitHub_NoTriggerMatch(t *testing.T) {
	reg := buildRegistry(t)
	srv, store := newWebhookServer(t, reg)
	defer srv.Close()

	// "enhancement" label does not trigger evaluate-bug.
	payload := issuesLabeledPayload("acme/backend", 11, []string{"enhancement"})
	body, _ := json.Marshal(payload)
	sig := githubSig(body, testWebhookSecret)

	resp := postWebhook(t, srv, "issues", payload, sig)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	store.mu.Lock()
	jobCount := len(store.Jobs)
	store.mu.Unlock()
	if jobCount != 0 {
		t.Fatalf("non-matching event created %d jobs, want 0", jobCount)
	}
}

// TestWebhookGitHub_ExcludedLabelSkipsJob verifies that an issue with an
// excluded label ("agent-evaluated") is not re-triggered.
func TestWebhookGitHub_ExcludedLabelSkipsJob(t *testing.T) {
	reg := buildRegistry(t)
	srv, store := newWebhookServer(t, reg)
	defer srv.Close()

	// Issue has "bug" (required) but also "agent-evaluated" (excluded).
	payload := issuesLabeledPayload("acme/backend", 12, []string{"bug", "agent-evaluated"})
	body, _ := json.Marshal(payload)
	sig := githubSig(body, testWebhookSecret)

	resp := postWebhook(t, srv, "issues", payload, sig)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	store.mu.Lock()
	jobCount := len(store.Jobs)
	store.mu.Unlock()
	if jobCount != 0 {
		t.Fatalf("excluded-label event created %d jobs, want 0", jobCount)
	}
}

// TestVerifyGitHubSignature tests the HMAC helper directly.
func TestVerifyGitHubSignature(t *testing.T) {
	body := []byte(`{"action":"labeled"}`)
	secret := "my-secret"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	validSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	tests := []struct {
		name   string
		sig    string
		want   bool
	}{
		{"valid", validSig, true},
		{"wrong prefix", "sha1=" + hex.EncodeToString(mac.Sum(nil)), false},
		{"empty", "", false},
		{"tampered", "sha256=aabbccdd", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := verifyGitHubSignature(body, secret, tc.sig)
			if got != tc.want {
				t.Fatalf("verifyGitHubSignature = %v, want %v", got, tc.want)
			}
		})
	}
}
