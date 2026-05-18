package chat

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
)

// fakeAuth is the test-side AuthExtractor that returns a fixed user
// based on an X-Test-User header. The cloud server uses the real
// auth.Handler; this stub keeps the test independent of that wiring.
type fakeAuth struct{}

type ctxKey int

const ctxKeyUser ctxKey = 0

func (fakeAuth) UserFromContext(ctx context.Context) *cloud.User {
	u, _ := ctx.Value(ctxKeyUser).(*cloud.User)
	return u
}

// withUser is the test wrapper that injects a user into r.Context() so
// fakeAuth can pull it out. Production uses auth.Handler middleware.
func withUser(h http.Handler, u *cloud.User) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), ctxKeyUser, u)
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// fakeClaude builds a path to a shell script that mimics `claude -p`
// just enough for our tests: it echoes its stdin to stdout, prepended
// with a header line. The script is written into t.TempDir() and the
// returned path can be passed as Config.ClaudeBin.
func fakeClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	body := "#!/bin/sh\n" +
		"echo '{\"type\":\"system\",\"subtype\":\"init\"}'\n" +
		"cat\n" +
		"echo ''\n" +
		"echo '{\"type\":\"result\",\"result\":\"ok\"}'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return script
}

func TestSessionLifecycle(t *testing.T) {
	t.Parallel()

	store := cloud.NewMemoryStore()
	workdir := t.TempDir()
	// Pretend the repo is already cloned at workdir so we don't hit
	// EnsureClone's git path during this test.
	cfg := Config{
		ClonesDir:       filepath.Dir(workdir),
		ClaudeBin:       fakeClaude(t),
		AnthropicAPIKey: "test-key",
		Store:           store,
		Now:             time.Now,
	}
	h, err := NewHandler(cfg, fakeAuth{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	defer h.Shutdown()

	// Create a session directly (skip the create handler so we don't
	// have to plumb the clone path through the fake auth setup).
	now := time.Now()
	s := newSession("user-1", "acme/widgets", workdir, now)
	procCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	s.procCancel = cancel
	if err := s.start(procCtx, cfg.ClaudeBin, cfg.AnthropicAPIKey, "hello\n"); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Drain events until we see end or error. Anything taking longer
	// than five seconds means the subprocess is wedged.
	deadline := time.After(5 * time.Second)
	gotEnd := false
	gotOutput := false
	for !gotEnd {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				gotEnd = true
				break
			}
			switch ev.Type {
			case "output":
				gotOutput = true
			case "end":
				gotEnd = true
			case "error":
				t.Fatalf("subprocess errored: %s", ev.Text)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for subprocess")
		}
	}
	if !gotOutput {
		t.Errorf("expected at least one output event")
	}

	// Close is idempotent.
	s.Close()
	s.Close()
}

func TestPathSanitise(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool // want error?
	}{
		{"acme/widgets", false},
		{"good-owner/good_repo.name", false},
		{"", true},
		{"only-one-part", true},
		{"too/many/parts", true},
		{"../etc/passwd", true},
		{"good/../bad", true},
		{"./hidden/repo", true},
		{"owner/..", true},
		{"owner/.", true},
		{"owner/with space", true},
		{"owner/with\nnewline", true},
		{"owner/with;semi", true},
	}
	for _, tc := range cases {
		_, _, err := splitRepo(tc.in)
		got := err != nil
		if got != tc.want {
			t.Errorf("splitRepo(%q): got err=%v, want err=%v", tc.in, err, tc.want)
		}
	}
}

func TestGitHubJWT(t *testing.T) {
	t.Parallel()
	// Generate a fresh RSA key for the round trip.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	now := time.Now()
	jwt, err := signGitHubAppJWT(key, 12345, now)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("want 3 JWT parts, got %d", len(parts))
	}

	// Verify the signature with the public key.
	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	sum := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// Decode the payload and sanity-check the claims.
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims struct {
		Iat int64 `json:"iat"`
		Exp int64 `json:"exp"`
		Iss any   `json:"iss"`
	}
	if err := json.Unmarshal(pb, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	// iss is a JSON number; json unmarshals to float64 by default.
	switch v := claims.Iss.(type) {
	case float64:
		if int64(v) != 12345 {
			t.Errorf("iss = %v, want 12345", v)
		}
	case json.Number:
		i, _ := v.Int64()
		if i != 12345 {
			t.Errorf("iss = %v, want 12345", v)
		}
	default:
		t.Errorf("iss has unexpected type %T", claims.Iss)
	}
	if claims.Exp <= claims.Iat {
		t.Errorf("exp %d not after iat %d", claims.Exp, claims.Iat)
	}
	if d := claims.Exp - now.Unix(); d > 600 || d < 60 {
		// GitHub allows up to 10 minutes; we use a 9-minute window.
		t.Errorf("exp window unexpected: %d seconds", d)
	}
}

// TestParseRSAPrivateKey round-trips a generated key through PEM
// encode/decode to make sure our parser accepts both PKCS#1 and PKCS#8
// inputs.
func TestParseRSAPrivateKey(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	pkcs1 := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	got1, err := parseRSAPrivateKey(pkcs1)
	if err != nil {
		t.Fatalf("PKCS1: %v", err)
	}
	if got1.N.Cmp(key.N) != 0 {
		t.Fatal("PKCS1 modulus mismatch")
	}

	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal PKCS8: %v", err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	got8, err := parseRSAPrivateKey(pkcs8)
	if err != nil {
		t.Fatalf("PKCS8: %v", err)
	}
	if got8.N.Cmp(key.N) != 0 {
		t.Fatal("PKCS8 modulus mismatch")
	}
}

// TestHandlerCreateAndStream end-to-end exercises the HTTP routes:
//
//  1. POST /sessions to create a session against a pre-existing clone.
//  2. GET  /sessions/{id}/stream and read until end.
//  3. DELETE the session.
//
// We bypass EnsureClone by using a sentinel repo name whose path the
// handler will find pre-populated under cfg.ClonesDir.
func TestHandlerCreateAndStream(t *testing.T) {
	t.Parallel()
	clones := t.TempDir()
	// Pre-create the "clone" so EnsureClone's "already exists" branch
	// short-circuits without trying to invoke git.
	prepath := filepath.Join(clones, "acme", "widgets", ".git")
	if err := os.MkdirAll(prepath, 0o755); err != nil {
		t.Fatalf("mkdir prepath: %v", err)
	}

	cfg := Config{
		ClonesDir:       clones,
		ClaudeBin:       fakeClaude(t),
		AnthropicAPIKey: "test-key",
		Store:           cloud.NewMemoryStore(),
		Now:             time.Now,
	}
	h, err := NewHandler(cfg, fakeAuth{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	defer h.Shutdown()

	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	user := &cloud.User{ID: "user-1", Login: "alice"}
	srv := httptest.NewServer(withUser(mux, user))
	defer srv.Close()

	// 1. Create session.
	createBody := `{"repo":"acme/widgets","message":"hello\n"}`
	resp, err := http.Post(srv.URL+"/api/cloud/chat/sessions", "application/json", strings.NewReader(createBody))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d", resp.StatusCode)
	}
	var created struct {
		ID   string `json:"id"`
		Repo string `json:"repo"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	resp.Body.Close()
	if created.ID == "" || created.Repo != "acme/widgets" {
		t.Fatalf("unexpected create response: %+v", created)
	}

	// 2. Stream events. We read until we see "end" or 5 s elapse.
	streamReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/cloud/chat/sessions/"+created.ID+"/stream", nil)
	streamResp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer streamResp.Body.Close()
	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("stream status %d", streamResp.StatusCode)
	}

	// Read the SSE stream in a goroutine so we can timeout the test
	// even if the server holds the connection open.
	bodyCh := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var collected strings.Builder
		for {
			n, err := streamResp.Body.Read(buf)
			if n > 0 {
				collected.Write(buf[:n])
				if strings.Contains(collected.String(), `"type":"end"`) {
					bodyCh <- collected.String()
					return
				}
			}
			if err != nil {
				bodyCh <- collected.String()
				return
			}
		}
	}()
	var body string
	select {
	case body = <-bodyCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("stream read timed out")
	}
	if !strings.Contains(body, `"type":"end"`) {
		t.Fatalf("did not see end event; got:\n%s", body)
	}

	// 3. Delete.
	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/cloud/chat/sessions/"+created.ID, nil)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status %d", delResp.StatusCode)
	}
}

// TestBuildSubprocessEnv asserts that ANTHROPIC_* parent vars are
// stripped and only ANTHROPIC_API_KEY is appended.
func TestBuildSubprocessEnv(t *testing.T) {
	t.Parallel()
	parent := []string{
		"PATH=/usr/bin",
		"ANTHROPIC_BASE_URL=https://stale.example.com",
		"ANTHROPIC_MODEL=opus",
		"HOME=/root",
	}
	got := buildSubprocessEnv(parent, "fresh-key")
	want := []string{"PATH=/usr/bin", "HOME=/root", "ANTHROPIC_API_KEY=fresh-key"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %v", len(got), len(want), got)
	}
	for i, kv := range want {
		if got[i] != kv {
			t.Errorf("got[%d]=%q, want %q", i, got[i], kv)
		}
	}

	// With no key configured, no ANTHROPIC_API_KEY entry should be appended.
	got2 := buildSubprocessEnv(parent, "")
	for _, kv := range got2 {
		if strings.HasPrefix(kv, "ANTHROPIC_") {
			t.Errorf("unexpected ANTHROPIC_* entry %q", kv)
		}
	}
}
