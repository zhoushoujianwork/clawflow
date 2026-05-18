package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

// roundTripFunc lets a test inject a synthetic HTTP response for outbound
// calls to GitHub, so we never touch the real github.com.
type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func jsonRT(status int, body any) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		buf, _ := json.Marshal(body)
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(string(buf))),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	}
}

// newTestHandler returns a Handler wired to a fresh MemoryStore and a fake
// HTTP client. Tests can swap the round-tripper per case.
func newTestHandler(t *testing.T, rt http.RoundTripper) (*Handler, cloud.Store) {
	t.Helper()
	store := cloud.NewMemoryStore()
	h := NewHandler(store, Config{
		AppID:        12345,
		AppSlug:      "daboluocc",
		ClientID:     "Iv23test",
		ClientSecret: "secret",
		PublicURL:    "https://test.example.com",
		SessionKey:   []byte("test-key-32-bytes-aaaaaaaaaaaaa"),
		CookieSecure: false,
		HTTPClient:   &http.Client{Transport: rt},
		Now:          time.Now,
	})
	return h, store
}

func TestEncodeDecodeState_RoundTrip(t *testing.T) {
	key := []byte("test-key-32-bytes-aaaaaaaaaaaaa")
	enc := encodeState("nonce123", "/repos", key)
	nonce, next, ok := decodeState(enc, key)
	if !ok || nonce != "nonce123" || next != "/repos" {
		t.Fatalf("round-trip mismatch: %q %q %v", nonce, next, ok)
	}
	// Tampering with any segment must invalidate.
	bad := strings.Replace(enc, "nonce123", "nonce999", 1)
	if _, _, ok := decodeState(bad, key); ok {
		t.Fatal("tampered state should not verify")
	}
}

func TestSanitizeNext_RejectsOpenRedirect(t *testing.T) {
	cases := map[string]string{
		"":                "/",
		"/repos":          "/repos",
		"//evil.com":      "/",
		"https://evil":    "/",
		"javascript:bad":  "/",
		"/legit/sub":      "/legit/sub",
	}
	for in, want := range cases {
		if got := sanitizeNext(in); got != want {
			t.Errorf("sanitizeNext(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestHandleLogin_SetsStateCookieAndRedirects(t *testing.T) {
	h, _ := newTestHandler(t, jsonRT(200, map[string]string{}))
	req := httptest.NewRequest("GET", "/api/v1/github/app/login?next=/repos", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status: got %d want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://github.com/login/oauth/authorize") {
		t.Fatalf("location: %q", loc)
	}
	if !strings.Contains(loc, "client_id=Iv23test") {
		t.Fatalf("client_id missing: %q", loc)
	}
	cookies := rec.Result().Cookies()
	var stateCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == stateCookieName {
			stateCookie = c
			break
		}
	}
	if stateCookie == nil || stateCookie.Value == "" {
		t.Fatalf("state cookie missing")
	}
}

func TestRequireUser_Unauthorized(t *testing.T) {
	h, _ := newTestHandler(t, jsonRT(200, map[string]string{}))
	inner := h.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	rec := httptest.NewRecorder()
	inner.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRequireUser_ViaBearerToken(t *testing.T) {
	h, store := newTestHandler(t, jsonRT(200, map[string]string{}))
	u, _ := store.UpsertUser(cloud.UpsertUserRequest{GitHubID: 1, Login: "alice"})
	plain := "pat_test_secret"
	if _, err := store.CreateAPIToken(cloud.CreateAPITokenRequest{
		UserID: u.ID, Kind: cloud.APITokenKindPersonal, Plaintext: plain,
	}); err != nil {
		t.Fatalf("create token: %v", err)
	}
	var seen *cloud.User
	inner := h.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	inner.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if seen == nil || seen.ID != u.ID {
		t.Fatalf("user not injected into context")
	}
}

func TestRequireMachine_RejectsPersonalToken(t *testing.T) {
	h, store := newTestHandler(t, jsonRT(200, map[string]string{}))
	u, _ := store.UpsertUser(cloud.UpsertUserRequest{GitHubID: 2, Login: "bob"})
	plain := "pat_personal"
	_, _ = store.CreateAPIToken(cloud.CreateAPITokenRequest{
		UserID: u.ID, Kind: cloud.APITokenKindPersonal, Plaintext: plain,
	})
	inner := h.RequireMachine(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+plain)
	rec := httptest.NewRecorder()
	inner.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for personal token, got %d", rec.Code)
	}
}

func TestHandleLogout_ClearsCookie(t *testing.T) {
	h, store := newTestHandler(t, jsonRT(200, map[string]string{}))
	u, _ := store.UpsertUser(cloud.UpsertUserRequest{GitHubID: 3, Login: "carol"})
	sess, _ := store.CreateSession(u.ID, time.Hour)
	req := httptest.NewRequest("POST", "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sess.ID})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	// The session row should be gone.
	got, _ := store.GetSession(sess.ID)
	if got != nil {
		t.Fatalf("session not deleted")
	}
	// Clearing cookie should be in the response.
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("logout did not clear cookie")
	}
}

// TestDeviceFlow_HappyPath drives the device flow end-to-end with a fake
// GitHub. It exercises:
//   - /device returns the user_code + verification URI to the CLI
//   - /device/poll first returns "pending"
//   - /device/poll then returns the access token, the cloud upserts the user,
//     mints a personal API token, and the response contains the plaintext.
func TestDeviceFlow_HappyPath(t *testing.T) {
	// We need stateful round-tripping: first poll = pending, second = success.
	// Track call counts per URL.
	step := 0
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch {
		case req.URL.String() == "https://github.com/login/device/code":
			body, _ := json.Marshal(map[string]any{
				"device_code":      "dev_xyz",
				"user_code":        "ABCD-1234",
				"verification_uri": "https://github.com/login/device",
				"expires_in":       900,
				"interval":         1,
			})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		case req.URL.String() == "https://github.com/login/oauth/access_token":
			step++
			if step == 1 {
				body, _ := json.Marshal(map[string]string{"error": "authorization_pending"})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			}
			body, _ := json.Marshal(map[string]string{
				"access_token": "gho_user_token",
				"token_type":   "bearer",
			})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		case req.URL.Path == "/user":
			body, _ := json.Marshal(map[string]any{
				"id": int64(101), "login": "alice", "name": "Alice", "avatar_url": "a.png",
			})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		case req.URL.Path == "/user/installations":
			body, _ := json.Marshal(map[string]any{
				"total_count":   0,
				"installations": []any{},
			})
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
		}
		t.Fatalf("unexpected outbound URL: %s", req.URL.String())
		return nil, nil
	})
	h, store := newTestHandler(t, rt)

	// 1) Start the flow.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/auth/device", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	var start map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &start); err != nil {
		t.Fatalf("start json: %v", err)
	}
	userCode, _ := start["user_code"].(string)
	if userCode != "ABCD-1234" {
		t.Fatalf("user_code: %q", userCode)
	}

	// 2) First poll → pending.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/auth/device/poll",
		strings.NewReader(`{"user_code":"ABCD-1234"}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("first poll: %d %s", rec.Code, rec.Body.String())
	}

	// 3) Second poll → ok.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/auth/device/poll",
		strings.NewReader(`{"user_code":"ABCD-1234"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("second poll: %d %s", rec.Code, rec.Body.String())
	}
	var done map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &done); err != nil {
		t.Fatalf("done json: %v", err)
	}
	pat, _ := done["token"].(string)
	if !strings.HasPrefix(pat, "pat_") {
		t.Fatalf("personal token shape: %q", pat)
	}
	// The plaintext should authenticate against the store.
	tok, err := store.LookupAPIToken(pat)
	if err != nil || tok == nil {
		t.Fatalf("token not in store: %v / %v", err, tok)
	}
	// And the upserted user should be alice.
	u, _ := store.GetUserByGitHubID(101)
	if u == nil || u.Login != "alice" {
		t.Fatalf("user not upserted: %+v", u)
	}
}
