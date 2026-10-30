package commands

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/devmode"
)

const testCandidateID = "0123456789ab"

func candidateHome(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".clawflow", "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CLAWFLOW_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv(devmode.EnvID, testCandidateID)
	t.Setenv(devmode.EnvRoot, root)
	t.Setenv(devmode.EnvSchema, devmode.SchemaPrefix+testCandidateID)
	t.Setenv(devmode.EnvPort, "23999")
	t.Setenv("GH_TOKEN", "")
	return root
}

func writeCandidateConfig(t *testing.T, yaml string) {
	t.Helper()
	if err := os.WriteFile(config.ConfigPath(), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A copied production config must not be able to dispatch or write back.
func TestCandidateModeNeutralizesRealRepositories(t *testing.T) {
	root := candidateHome(t)
	clone := filepath.Join(root, "clones", "fixture")
	writeCandidateConfig(t, `repos:
  real/github:
    enabled: true
    auto_merge: true
    self_upgrade: true
    local_path: `+clone+`
  real/gitlab:
    enabled: true
    platform: gitlab
    base_url: https://gitlab.com
    auto_merge: true
  fixture/ok:
    enabled: true
    platform: gitlab
    base_url: http://127.0.0.1:9
    local_path: `+clone+`
    auto_merge: true
  fixture/outside:
    enabled: true
    platform: gitlab
    base_url: http://127.0.0.1:9
    local_path: /Users/someone/github/clawflow
settings:
  github_app: {app_id: 7, private_key_path: /nope}
`)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	for name, r := range cfg.Repos {
		if r.AutoMerge || r.SelfUpgrade {
			t.Errorf("%s keeps auto-merge/self-upgrade", name)
		}
	}
	for _, name := range []string{"real/github", "real/gitlab", "fixture/outside"} {
		if cfg.EnabledRepos()[name].Enabled {
			t.Errorf("%s remains dispatchable in candidate mode", name)
		}
	}
	if !cfg.EnabledRepos()["fixture/ok"].Enabled {
		t.Error("a loopback fixture with a candidate-owned clone must remain usable")
	}
	if cfg.Settings.GitHubApp.Enabled() {
		t.Error("GitHub App identity survived in candidate mode")
	}
}

func TestCandidateModeRefusesVCSClientsForRealForges(t *testing.T) {
	candidateHome(t)
	for _, repo := range []config.Repo{
		{}, // public github.com
		{Platform: "github", BaseURL: "https://api.github.com"},
		{Platform: "gitlab", BaseURL: "https://gitlab.com"},
	} {
		if _, err := newVCSClient(repo); !errors.Is(err, devmode.ErrBlocked) {
			t.Errorf("client for %+v was created: %v", repo, err)
		}
	}
	if _, err := newVCSClient(config.Repo{Platform: "gitlab", BaseURL: "http://127.0.0.1:9"}); err != nil {
		t.Fatalf("fixture client refused: %v", err)
	}
}

// No write reaches a real host even if some code path built a client without
// the explicit check: the process-wide transport refuses every request.
func TestCandidateModeCannotWriteBackToRealRepositories(t *testing.T) {
	candidateHome(t)
	var hits atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); w.WriteHeader(204) }))
	defer fixture.Close()
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	devmode.Install()

	for _, target := range []string{"https://api.github.com/repos/o/r/issues/1/comments", "https://gitlab.com/api/v4/projects/1/merge_requests/2/merge"} {
		for _, method := range []string{"POST", "PUT", "DELETE"} {
			req, _ := http.NewRequest(method, target, strings.NewReader("{}"))
			if _, err := http.DefaultClient.Do(req); !errors.Is(err, devmode.ErrBlocked) {
				t.Fatalf("%s %s was not blocked: %v", method, target, err)
			}
		}
	}
	if resp, err := http.Post(fixture.URL, "application/json", strings.NewReader("{}")); err != nil || hits.Load() != 1 {
		t.Fatalf("loopback fixture write failed: %v hits=%d", err, hits.Load())
	} else {
		resp.Body.Close()
	}
	if os.Getenv("GIT_ALLOW_PROTOCOL") != "file" {
		t.Fatal("git transports are not restricted to local files")
	}
}

func TestCandidateModeRefusesHostMutatingCommands(t *testing.T) {
	candidateHome(t)
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	for _, args := range [][]string{{"update"}, {"sync", "push"}, {"login"}, {"web", "service", "install"}, {"candidate", "up"}, {"controller", "service", "install"}} {
		root := NewRootCmd()
		root.SetArgs(args)
		root.SetOut(os.Stderr)
		root.SilenceErrors, root.SilenceUsage = true, true
		err := root.Execute()
		switch args[0] {
		case "controller":
			// Allowed by the command filter, but the launchd service layer refuses it.
			if err == nil || !errors.Is(err, devmode.ErrBlocked) {
				t.Errorf("%v: want blocked, got %v", args, err)
			}
		default:
			if !errors.Is(err, devmode.ErrBlocked) {
				t.Errorf("%v: want blocked, got %v", args, err)
			}
		}
	}
}

func TestCandidateModeRefusesMalformedIdentity(t *testing.T) {
	candidateHome(t)
	t.Setenv(devmode.EnvID, "not-an-id")
	root := NewRootCmd()
	root.SetArgs([]string{"version"})
	root.SilenceErrors, root.SilenceUsage = true, true
	if err := root.Execute(); !errors.Is(err, devmode.ErrBlocked) {
		t.Fatalf("malformed candidate id accepted: %v", err)
	}
}

func TestCandidateModeRefusesStableLaunchdAndSelfUpgrade(t *testing.T) {
	root := candidateHome(t)
	if _, err := newWebService(); !errors.Is(err, devmode.ErrBlocked) {
		t.Fatalf("stable launchd service reachable: %v", err)
	}
	stableBinary := filepath.Join(t.TempDir(), "clawflow")
	if err := devmode.CheckWritePath(stableBinary); !errors.Is(err, devmode.ErrBlocked) {
		t.Fatalf("binary outside the candidate may be replaced: %v", err)
	}
	if err := devmode.CheckWritePath(filepath.Join(root, "bin", "clawflow")); err != nil {
		t.Fatalf("candidate binary refused: %v", err)
	}
}

// Refused inputs must leave nothing behind.
func TestCandidatePrepareRefusesBeforeMutation(t *testing.T) {
	t.Setenv(devmode.EnvID, "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	base := filepath.Join(t.TempDir(), "cands")
	commit := strings.Repeat("a", 40)
	for _, args := range [][]string{
		{"--fixture", "o/r=https://gitlab.com"},
		{"--fixture", "o/r="},
		{"--fixture", "no-equals"},
		{"--sop-required", "--sop-digest", "abc"},
		{"--capability", "fixture-runtime"},
		{"--port", "8093"},
		{"--port", "80"},
	} {
		root := NewRootCmd()
		root.SetArgs(append([]string{"candidate", "prepare", "--commit", commit, "--base", base}, args...))
		root.SilenceErrors, root.SilenceUsage = true, true
		root.SetOut(os.Stderr)
		if err := root.Execute(); err == nil {
			t.Errorf("%v was accepted", args)
		}
		if _, err := os.Stat(base); err == nil {
			t.Fatalf("%v created %s before refusing", args, base)
		}
	}
}
