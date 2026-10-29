package gitexec

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// This executes a real HTTPS push against git-http-backend. The server rejects
// unauthenticated requests, verifies the installation credential, and stores a ref.
func TestGitHubAppHTTPSPush(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	bare := filepath.Join(root, "o", "r.git")
	if err := os.MkdirAll(filepath.Dir(bare), 0700); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %s", args[0], out)
		}
		return strings.TrimSpace(string(out))
	}
	git("", "init", "--bare", bare)
	git(bare, "config", "http.receivepack", "true")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	var authenticated atomic.Int32
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/installation":
			fmt.Fprint(w, `{"id":17,"permissions":{"contents":"write"},"repository_selection":"selected"}`)
			return
		case "/app/installations/17/access_tokens":
			json.NewEncoder(w).Encode(map[string]any{"token": "app-installation-test-secret", "expires_at": time.Now().Add(time.Hour)})
			return
		}
		user, password, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			w.WriteHeader(401)
			return
		}
		if user != "x-access-token" || password != "app-installation-test-secret" {
			t.Error("push did not use installation identity")
			w.WriteHeader(403)
			return
		}
		authenticated.Add(1)
		backend.ServeHTTP(w, r)
	}))
	defer server.Close()
	// Only the test API client needs to trust the ephemeral test certificate.
	// Git is separately configured with GIT_SSL_NO_VERIFY below.
	// Resolve's provider uses the default transport; replace it for this test.
	old := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = old }()
	cfg := &config.Config{Settings: config.Settings{GitHubApp: config.GitHubAppSettings{AppID: 8, PrivateKeyPath: keyPath}}, Repos: map[string]config.Repo{"o/r": {Platform: "github", BaseURL: server.URL}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	git(work, "init")
	git(work, "config", "user.email", "test@example.com")
	git(work, "config", "user.name", "test")
	git(work, "commit", "--allow-empty", "-m", "test")
	remote := server.URL + "/o/r.git"
	git(work, "remote", "add", "origin", remote)
	c := exec.Command("git", "push", "origin", "HEAD:refs/heads/main")
	c.Dir = work
	c.Env = append(os.Environ(), "GIT_SSL_NO_VERIFY=true")
	out, err := CombinedOutput(context.Background(), c)
	if err != nil {
		t.Fatalf("push failed: %v %s", err, out)
	}
	if authenticated.Load() == 0 {
		t.Fatal("no authenticated push")
	}
	if git(work, "rev-parse", "HEAD") != git(bare, "rev-parse", "refs/heads/main") {
		t.Fatal("push did not update ref")
	}
	data, err := os.ReadFile(filepath.Join(work, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "app-installation-test-secret") || strings.Contains(string(out), "app-installation-test-secret") {
		t.Fatal("installation token leaked")
	}
	if git(work, "remote", "get-url", "origin") != remote {
		t.Fatal("remote was rewritten")
	}
	// A helper must never return the token for another repository or host.
	args, env, err := githubCredentials(context.Background(), c, []string{"credential", "fill"}, c.Env)
	if err != nil {
		t.Fatal(err)
	}
	probe := exec.Command("git", args...)
	probe.Dir = work
	probe.Env = env
	probe.Stdin = strings.NewReader("protocol=https\nhost=attacker.example\npath=o/r.git\n\n")
	denied, _ := probe.CombinedOutput()
	if strings.Contains(string(denied), "app-installation-test-secret") {
		t.Fatal("helper leaked credential to another host")
	}
}
