package githubauth

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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
)

func testSettings(t *testing.T) (config.GitHubAppSettings, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "secret-private-key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	return config.GitHubAppSettings{AppID: 42, PrivateKeyPath: path}, key
}
func TestJWT(t *testing.T) {
	s, key := testSettings(t)
	now := time.Unix(1700000000, 0)
	jwt, err := signJWT(s, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatal("invalid JWT")
	}
	signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hash[:], signature); err != nil {
		t.Fatal(err)
	}
	data, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]int64
	if err := json.Unmarshal(data, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["iat"] != now.Unix()-60 || claims["exp"] > now.Unix()+600 || claims["iss"] != 42 {
		t.Fatal("incorrect claims")
	}
}
func TestConcurrentRefreshAndFallback(t *testing.T) {
	s, _ := testSettings(t)
	now := time.Now()
	var mints atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ey") {
			t.Error("missing JWT")
		}
		switch r.URL.Path {
		case "/repos/o/r/installation":
			fmt.Fprint(w, `{"id":7,"permissions":{"contents":"write"},"repository_selection":"selected"}`)
		case "/repos/o/outside/installation":
			w.WriteHeader(404)
		case "/app/installations/7/access_tokens":
			mints.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"token": "installation-secret", "expires_at": now.Add(time.Hour)})
		default:
			t.Error("unexpected endpoint")
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	p := New(s, server.URL)
	p.now = func() time.Time { return now }
	callers := func() {
		t.Helper()
		var wg sync.WaitGroup
		for i := 0; i < 24; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id, err := p.Resolve(context.Background(), "o/r", "personal-secret")
				if err != nil || id.Token != "installation-secret" || id.Name != "app:42:installation:7" {
					t.Error("incorrect installation resolution", err)
				}
			}()
		}
		wg.Wait()
	}
	callers()
	if mints.Load() != 1 {
		t.Fatal("refresh not shared")
	}
	callers()
	if mints.Load() != 1 {
		t.Fatal("token not cached")
	}
	now = now.Add(56 * time.Minute)
	callers()
	if mints.Load() != 2 {
		t.Fatal("token not refreshed before expiry")
	}
	id, err := p.Resolve(context.Background(), "o/outside", "personal-secret")
	if err != nil || id.Name != "personal" || id.Token != "personal-secret" {
		t.Fatal("fallback failed", err)
	}
	s.InstallationID = 8
	p = New(s, server.URL)
	id, err = p.Resolve(context.Background(), "o/r", "personal-secret")
	if err != nil || id.Name != "personal" {
		t.Fatal("explicit installation mismatch did not fall back")
	}
}
func TestAuthenticationErrorsDoNotExposeSecrets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, _ := testSettings(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, s.PrivateKeyPath+" installation-secret")
	}))
	defer server.Close()
	p := New(s, server.URL)
	_, err := p.Resolve(context.Background(), "o/r", "personal-secret")
	if err == nil {
		t.Fatal("expected error")
	}
	lg, openErr := clog.Open("auth-test")
	if openErr != nil {
		t.Fatal(openErr)
	}
	lg.Warn("authentication", "error", err)
	lg.Close()
	captured, readErr := os.ReadFile(filepath.Join(clog.LogsDir(), "auth-test.log"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, secret := range []string{s.PrivateKeyPath, "installation-secret", "personal-secret"} {
		if strings.Contains(err.Error(), secret) || strings.Contains(string(captured), secret) {
			t.Fatal("secret in diagnostic")
		}
	}
	if err := os.Chmod(s.PrivateKeyPath, 0644); err != nil {
		t.Fatal(err)
	}
	_, err = signJWT(s, time.Now())
	if err == nil || strings.Contains(err.Error(), s.PrivateKeyPath) {
		t.Fatal("unsafe key accepted or path exposed")
	}
}
