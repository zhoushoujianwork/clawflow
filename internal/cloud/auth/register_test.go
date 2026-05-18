package auth_test

// This test lives in auth_test (separate package) so it can import both
// internal/cloud and internal/cloud/auth without creating an import cycle.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
	"github.com/zhoushoujianwork/clawflow/internal/cloud/auth"
)

// TestWorkerRegisterMintsMachineToken verifies the full hand-off the worker
// bootstrap relies on:
//
//  1. POST /api/worker/register requires a personal token (RequireUser).
//  2. The handler mints a kind="machine" api_token whose plaintext IS the
//     returned WorkerToken.
//  3. The returned WorkerToken authenticates subsequent /api/worker/heartbeat
//     calls (RequireMachine accepts it).
//  4. A personal token rejected by RequireMachine cannot impersonate a worker.
func TestWorkerRegisterMintsMachineToken(t *testing.T) {
	store := cloud.NewMemoryStore()
	authH := auth.NewHandler(store, auth.Config{
		AppID:        12345,
		ClientID:     "Iv23test",
		ClientSecret: "secret",
		PublicURL:    "https://test.example.com",
		SessionKey:   []byte("test-key-32-bytes-aaaaaaaaaaaaa"),
		CookieSecure: false,
	})
	srv := httptest.NewServer(cloud.NewServerWithAuth(store, nil, authH))
	defer srv.Close()

	// Seed: a logged-in user with one personal token. Mirrors what
	// `clawflow cloud login` would have produced.
	user, err := store.UpsertUser(cloud.UpsertUserRequest{
		GitHubID: 101, Login: "alice",
	})
	if err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	personalPlain := "pat_personal_secret_value"
	if _, err := store.CreateAPIToken(cloud.CreateAPITokenRequest{
		UserID: user.ID, Kind: cloud.APITokenKindPersonal, Plaintext: personalPlain,
	}); err != nil {
		t.Fatalf("create personal token: %v", err)
	}

	// 1. Register without auth → 401.
	resp, err := http.Post(srv.URL+"/api/worker/register", "application/json",
		strings.NewReader(`{"hostname":"laptop","version":"v1"}`))
	if err != nil {
		t.Fatalf("anon register: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anon register status = %d, want 401", resp.StatusCode)
	}

	// 2. Register with the personal token → 200 + worker token in response.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/worker/register",
		strings.NewReader(`{"hostname":"laptop","version":"v1"}`))
	req.Header.Set("Authorization", "Bearer "+personalPlain)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authed register: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authed register status = %d", resp.StatusCode)
	}
	var regResp cloud.RegisterWorkerResponse
	if err := decodeJSON(resp, &regResp); err != nil {
		t.Fatalf("decode register: %v", err)
	}
	if regResp.MachineID == "" || regResp.WorkerID == "" || regResp.WorkerToken == "" {
		t.Fatalf("incomplete register response: %+v", regResp)
	}

	// 3. The minted machine token must be looked-up-able and tied to this
	// user + machine.
	tok, err := store.LookupAPIToken(regResp.WorkerToken)
	if err != nil || tok == nil {
		t.Fatalf("worker token not in api_tokens: %v / %v", err, tok)
	}
	if tok.Kind != cloud.APITokenKindMachine {
		t.Fatalf("expected machine token, got kind=%q", tok.Kind)
	}
	if tok.UserID != user.ID || tok.MachineID != regResp.MachineID {
		t.Fatalf("token ownership mismatch: %+v", tok)
	}

	// 4. Heartbeat with the machine token → success.
	hbReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/worker/heartbeat",
		bytes.NewBufferString(`{"machine_id":"`+regResp.MachineID+`","worker_id":"`+regResp.WorkerID+`","status":"online","capacity":1}`))
	hbReq.Header.Set("Authorization", "Bearer "+regResp.WorkerToken)
	hbReq.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hbReq)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat status = %d", resp.StatusCode)
	}

	// 5. Heartbeat with the PERSONAL token → 401 (RequireMachine excludes
	// kind=personal even though it's a valid user credential).
	hbReq2, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/worker/heartbeat",
		bytes.NewBufferString(`{"machine_id":"`+regResp.MachineID+`","worker_id":"`+regResp.WorkerID+`","status":"online","capacity":1}`))
	hbReq2.Header.Set("Authorization", "Bearer "+personalPlain)
	hbReq2.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(hbReq2)
	if err != nil {
		t.Fatalf("heartbeat with personal token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("heartbeat with personal token status = %d, want 401", resp.StatusCode)
	}

}

// decodeJSON decodes resp.Body into out and closes the body.
func decodeJSON(resp *http.Response, out any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}
