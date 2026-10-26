package commands

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func TestDisabledRepoAddNeverInitializesLabels(t *testing.T) {
	for _, flags := range [][]string{{}, {"--disabled"}, {"--skip-label-init"}, {"--disabled", "--skip-label-init"}} {
		t.Run(fmt.Sprint(flags), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("GITLAB_TOKEN", "test-token")
			var requests atomic.Int32
			old := http.DefaultTransport
			http.DefaultTransport = lifecycleTransport(func(r *http.Request) (*http.Response, error) {
				requests.Add(1)
				return nil, fmt.Errorf("unexpected HTTP request")
			})
			t.Cleanup(func() { http.DefaultTransport = old })
			cmd := newRepoAddCmd()
			cmd.SetArgs(append([]string{"https://gitlab.example.com/ns/offload"}, flags...))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if len(flags) > 0 && requests.Load() != 0 {
				t.Fatalf("made %d HTTP requests", requests.Load())
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(flags) == 0 && requests.Load() == 0 {
				t.Fatal("default add did not initialize labels")
			}
			wantEnabled := len(flags) == 0 || flags[0] != "--disabled"
			if cfg.Repos["ns/offload"].Enabled != wantEnabled {
				t.Fatalf("%+v", cfg.Repos)
			}
		})
	}
}

func TestRepoAddPreservesMalformedConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := config.ConfigPath()
	resetTestWrite(t, path, "repos: [")
	cmd := newRepoAddCmd()
	cmd.SetArgs([]string{"ns/repo", "--disabled"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected config error")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "repos: [" {
		t.Fatal("config overwritten")
	}
}

func TestRepoAddValidatesCheckoutOrigin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init")
	git("remote", "add", "origin", "https://gitlab.example.com/ns/offload.git")
	for _, tc := range []struct {
		url  string
		fail bool
	}{{"https://gitlab.example.com/ns/other", true}, {"https://other.example.com/ns/offload", true}, {"https://gitlab.example.com/ns/offload", false}} {
		cmd := newRepoAddCmd()
		cmd.SetArgs([]string{tc.url, "--local-path", dir, "--disabled"})
		err := cmd.Execute()
		if (err != nil) != tc.fail {
			t.Fatalf("%s: %v", tc.url, err)
		}
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repos["ns/offload"].LocalPath != filepath.Clean(dir) {
		t.Fatalf("%+v", cfg.Repos)
	}
	if _, err := validateRepoCheckout(t.TempDir(), config.RepoInfo{OwnerRepo: "ns/offload", Platform: "gitlab", BaseURL: "https://gitlab.example.com"}); err == nil {
		t.Fatal("accepted non-checkout")
	}
}

type lifecycleTransport func(*http.Request) (*http.Response, error)

func (f lifecycleTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
