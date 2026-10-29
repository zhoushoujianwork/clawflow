package commands

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"gopkg.in/yaml.v3"
)

type platformStore struct{ coordinator.WorkerStore }

func (*platformStore) VerifyToken(_ context.Context, secret string) (*coordinator.Token, error) {
	if secret == "admin-secret" {
		return &coordinator.Token{Name: "admin", Scope: coordinator.ScopeAdmin}, nil
	}
	if secret == "worker-secret" {
		return &coordinator.Token{Name: "worker", Scope: coordinator.ScopeWorker}, nil
	}
	return nil, coordinator.ErrToken
}
func (*platformStore) AppendAudit(context.Context, string, string, string) error { return nil }

// A separate process gives the platform its own real HOME/config, independent of the client.
func TestPlatformServerProcess(t *testing.T) {
	if os.Getenv("CLAWFLOW_PLATFORM_TEST_SERVER") != "1" {
		return
	}
	admin := &platformAdmin{board: func(context.Context) (coordinator.Board, error) {
		return coordinator.Board{Tasks: []coordinator.BoardTask{{State: "pending"}}, Workers: []coordinator.BoardWorker{{Name: "online", Live: true}, {Name: "offline"}}}, nil
	}}
	server := httptest.NewTLSServer(coordinator.NewPlatformHandler(&platformStore{}, admin))
	defer server.Close()
	sum := sha256.Sum256(server.Certificate().Raw)
	fmt.Printf("%s %s\n", server.URL, hex.EncodeToString(sum[:]))
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func TestPlatformClientEnrollment(t *testing.T) {
	hostHome := t.TempDir()
	clientHome := t.TempDir()
	t.Setenv("HOME", clientHome)
	child := exec.Command(os.Args[0], "-test.run=^TestPlatformServerProcess$")
	child.Env = append(os.Environ(), "HOME="+hostHome, "CLAWFLOW_PLATFORM_TEST_SERVER=1")
	out, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = in.Close()
		if err := child.Wait(); err != nil {
			t.Error(err)
		}
	})
	reader := bufio.NewReader(out)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Fields(line)
	if len(parts) != 2 {
		t.Fatalf("server startup: %q", line)
	}
	tokenFile := filepath.Join(clientHome, "admin-token")
	if err := os.WriteFile(tokenFile, []byte("admin-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Repos: map[string]config.Repo{"client/local": {Enabled: false}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	login := NewPlatformCmd()
	login.SetOut(&output)
	login.SetArgs([]string{"login", "--url", parts[0], "--token-file", tokenFile, "--fingerprint", parts[1]})
	if err := login.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "版本: "+Version) {
		t.Fatal(output.String())
	}
	before, err := os.ReadFile(config.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		cmd := NewRepoCmd()
		cmd.SetOut(&output)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		return cmd.Execute()
	}
	for _, args := range [][]string{
		{"add", "owner/remote", "--disabled"},
		{"enable", "owner/remote"},
		{"set", "owner/remote", "--ci-required", "on", "--auto-merge", "on", "--test-command", "go test ./...", "--max-in-flight", "3"},
		{"disable", "owner/remote"},
		{"list"},
	} {
		if err := run(args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	hostConfig := func() config.Config {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(hostHome, ".clawflow/config/config.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var cfg config.Config
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	remote := hostConfig().Repos["owner/remote"]
	if remote.Enabled || !remote.CIRequired || !remote.AutoMerge || remote.TestCommand != "go test ./..." || remote.MaxInFlight != 3 || remote.LocalPath != "" {
		t.Fatalf("platform config: %+v", remote)
	}
	if err := run("enable", "owner/remote"); err != nil {
		t.Fatal(err)
	}
	if !hostConfig().Repos["owner/remote"].Enabled {
		t.Fatal("enable did not change platform")
	}
	after, _ := os.ReadFile(config.ConfigPath())
	if !bytes.Equal(before, after) {
		t.Fatal("client config was changed by remote repo operations")
	}
	if strings.Contains(string(after), "admin-secret") || strings.Contains(output.String(), "admin-secret") {
		t.Fatal("secret leaked")
	}
	status := NewPlatformCmd()
	status.SetOut(&output)
	status.SetArgs([]string{"status"})
	if err := status.Execute(); err != nil {
		t.Fatal(err)
	}
	statusAlias := NewStatusCmd()
	statusAlias.SetOut(&output)
	statusAlias.SetArgs([]string{})
	if err := statusAlias.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), parts[0]) || !strings.Contains(output.String(), `"pending":1`) || !strings.Contains(output.String(), "online") || strings.Contains(output.String(), "offline") {
		t.Fatal(output.String())
	}
	if err := run("set", "owner/remote", "--ci-required", "invalid"); err == nil {
		t.Fatal("invalid option accepted")
	}
	if err := run("add", "owner/another", "--local-path", "/tmp"); err == nil {
		t.Fatal("local checkout accepted")
	}
}

func TestPlatformTokenDestination(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(tokenFile, []byte("admin-secret"), 0600)
	var received int
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received++
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer destination.Close()
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	sum := sha256.Sum256(source.Certificate().Raw)
	s := config.PlatformSettings{URL: source.URL, TokenFile: tokenFile, Fingerprint: hex.EncodeToString(sum[:])}
	var out any
	if err := platformCall(context.Background(), s, "GET", "/admin/v1/status", nil, &out); err == nil {
		t.Fatal("redirect followed")
	}
	if received != 0 {
		t.Fatal("token sent to redirect destination")
	}
	s.Fingerprint = strings.Repeat("0", 64)
	if err := platformCall(context.Background(), s, "GET", "/admin/v1/status", nil, &out); err == nil {
		t.Fatal("wrong TLS pin accepted")
	}
	for _, url := range []string{"http://example.com", "https://user:password@example.com", "https://example.com/other", "https://example.com?token=secret"} {
		s.URL = url
		if err := platformCall(context.Background(), s, "GET", "/admin/v1/status", nil, &out); err == nil {
			t.Fatalf("accepted %s", url)
		}
	}
}

func TestPlatformAdminPreservesRepoPolicy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg := &config.Config{Repos: map[string]config.Repo{"o/r": {Priority: 1, Labels: map[string]string{"custom": "kept"}, LocalPath: "/host/checkout"}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	a := &platformAdmin{}
	if _, err := a.Repos(context.Background(), coordinator.RepoRequest{Action: "set", Repo: "o/r", Options: map[string]string{"auto-merge": "on", "acceptance-required": "on", "acceptance-overlap-ci": "off", "self-upgrade": "on", "bound-machine": "runner"}}); err != nil {
		t.Fatal(err)
	}
	after, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	r := after.Repos["o/r"]
	if r.Priority != 1 || r.Labels["custom"] != "kept" || r.LocalPath != "/host/checkout" || r.UpdatedAt.IsZero() || !r.AutoMerge || !r.AcceptanceRequired || r.AcceptanceOverlapsCI() || !r.SelfUpgrade || len(r.WorkerTags) != 1 {
		t.Fatalf("policy lost: %+v", r)
	}
	out, err := a.Repos(context.Background(), coordinator.RepoRequest{Action: "list"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]config.Repo)["o/r"].LocalPath != "" {
		t.Fatal("checkout path disclosed")
	}
	for _, input := range []coordinator.RepoRequest{
		{Action: "add", Repo: "."}, {Action: "add", Repo: "git@invalid"},
		{Action: "set", Repo: "o/r", Options: map[string]string{"local-path": "/tmp"}},
		{Action: "set", Repo: "o/r", Options: map[string]string{"labels": "bad"}},
		{Action: "set", Repo: "o/r", Options: map[string]string{"priority": "0"}},
	} {
		if _, err := a.Repos(context.Background(), input); err == nil {
			t.Fatalf("accepted %+v", input)
		}
	}
}
