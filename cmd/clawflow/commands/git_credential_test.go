package commands

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

type appCredentialTransport struct {
	t     *testing.T
	calls int
}

func (f *appCredentialTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls++
	if r.URL.Host != "api.github.com" {
		f.t.Error("unexpected authentication host")
	}
	body := `{"id":77,"app_slug":"test","repository_selection":"selected"}`
	if r.Method == "POST" {
		body = fmt.Sprintf(`{"token":"short-lived-installation-secret","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}
func TestGitCredentialProtocol(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Settings: config.Settings{GitHubApp: config.GitHubAppSettings{AppID: 1, PrivateKeyPath: keyPath}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	transport := &appCredentialTransport{t: t}
	old := http.DefaultTransport
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = old }()
	run := func(operation, input string) string {
		t.Helper()
		cmd := newGitCredentialCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetIn(strings.NewReader(input))
		cmd.SetArgs([]string{operation})
		if err := cmd.Execute(); err != nil {
			t.Fatal("credential protocol failed")
		}
		return out.String()
	}
	if got := run("get", "protocol=https\nhost=github.com\npath=o/r.git\n\n"); !strings.Contains(got, "username=x-access-token\npassword=short-lived-installation-secret\n") || strings.Contains(got, keyPath) {
		t.Fatal("incorrect credential response")
	}
	calls := transport.calls
	if run("store", "protocol=https\nhost=github.com\npassword=must-not-store\n\n") != "" {
		t.Fatal("helper must not store credentials")
	}
	if run("get", "protocol=https\nhost=attacker.example\npath=o/r.git\n\n") != "" || transport.calls != calls {
		t.Fatal("helper contacted provider for an unrelated host")
	}
}
