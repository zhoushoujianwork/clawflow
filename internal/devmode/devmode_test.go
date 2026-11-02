package devmode

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func candidateEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(EnvID, "0123456789ab")
	t.Setenv(EnvRoot, root)
	t.Setenv(EnvSchema, SchemaPrefix+"0123456789ab")
	t.Setenv(EnvPort, "23456")
	return root
}

func TestInactiveByDefault(t *testing.T) {
	t.Setenv(EnvID, "")
	if Active() {
		t.Fatal("candidate mode must be opt-in")
	}
	if err := CheckCommand("update"); err != nil {
		t.Fatalf("stable mode must not restrict commands: %v", err)
	}
	before := http.DefaultTransport
	Install()
	if http.DefaultTransport != before {
		t.Fatal("Install changed the transport outside candidate mode")
	}
}

func TestMalformedIDFailsClosed(t *testing.T) {
	t.Setenv(EnvID, "../../etc")
	if !Active() || ID() != "" {
		t.Fatal("a malformed id must stay active without a usable identity")
	}
	if err := CheckLaunchTarget(LabelBase+"x.controller", ""); !errors.Is(err, ErrBlocked) {
		t.Fatalf("launch target accepted with malformed id: %v", err)
	}
}

func TestTransportRefusesNonLoopback(t *testing.T) {
	candidateEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	client := &http.Client{Transport: Transport(nil)}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("loopback fixture must stay reachable: %v", err)
	}
	resp.Body.Close()
	for _, target := range []string{"https://api.github.com/repos/o/r/issues", "https://gitlab.example.com/api/v4", "http://10.0.0.5/x", "http://[2001:db8::1]/"} {
		if _, err := client.Get(target); !errors.Is(err, ErrBlocked) {
			t.Fatalf("%s was not blocked: %v", target, err)
		}
	}
}

func TestRedirectToExternalHostIsBlocked(t *testing.T) {
	candidateEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://api.github.com/user", http.StatusFound)
	}))
	defer srv.Close()
	client := &http.Client{Transport: Transport(nil)}
	if _, err := client.Get(srv.URL); !errors.Is(err, ErrBlocked) {
		t.Fatalf("redirect hop escaped the guard: %v", err)
	}
}

func TestBaseURLRules(t *testing.T) {
	for raw, ok := range map[string]bool{"": false, "https://api.github.com": false, "http://127.0.0.1:9": true, "http://localhost:9/api": true, "http://[::1]:9": true, "http://127.0.0.1.evil.test": false} {
		if got := CheckBaseURL(raw) == nil; got != ok {
			t.Errorf("CheckBaseURL(%q) accepted=%v want %v", raw, got, ok)
		}
	}
}

func TestListenRules(t *testing.T) {
	candidateEnv(t)
	for addr, ok := range map[string]bool{"127.0.0.1:23456": true, "[::1]:23456": true, "localhost:23456": true, ":23456": false, "0.0.0.0:23456": false, "127.0.0.1:8093": false, "127.0.0.1:0": false, "bad": false} {
		if got := CheckListen(addr) == nil; got != ok {
			t.Errorf("CheckListen(%q) accepted=%v want %v", addr, got, ok)
		}
	}
}

func TestDSNMustPinCandidateSchema(t *testing.T) {
	candidateEnv(t)
	schema := os.Getenv(EnvSchema)
	for dsn, ok := range map[string]bool{
		"postgres://u:p@h/db?search_path=" + schema: true,
		"host=h dbname=d search_path=" + schema:     true,
		"postgres://u:p@h/db":                       false,
		"postgres://u:p@h/db?search_path=public":    false,
		"host=h dbname=d":                           false,
	} {
		if got := CheckDSN(dsn) == nil; got != ok {
			t.Errorf("CheckDSN accepted=%v want %v", got, ok)
		}
	}
	t.Setenv(EnvSchema, "public")
	if CheckDSN("postgres://u:p@h/db?search_path=public") == nil {
		t.Fatal("a non-candidate schema namespace was accepted")
	}
}

func TestLaunchTargetAndPathOwnership(t *testing.T) {
	root := candidateEnv(t)
	own := ServiceLabel("0123456789ab", "controller")
	if err := CheckLaunchTarget(own, filepath.Join(root, "home", "Library", "LaunchAgents", own+".plist")); err != nil {
		t.Fatalf("own service refused: %v", err)
	}
	for _, label := range []string{"com.zhoushoujianwork.clawflow.controller", "com.zhoushoujianwork.clawflow.worker-main", LabelBase + "ffffffffffff.controller"} {
		if err := CheckLaunchTarget(label, ""); !errors.Is(err, ErrBlocked) {
			t.Errorf("foreign label %s accepted", label)
		}
	}
	if CheckLaunchTarget(own, "/Users/x/Library/LaunchAgents/"+own+".plist") == nil {
		t.Fatal("plist outside the candidate root accepted")
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	if Within(root, filepath.Join(link, "x")) {
		t.Fatal("symlink escaped the candidate root")
	}
	if !Within(root, filepath.Join(root, "new", "deep", "file")) {
		t.Fatal("not-yet-created path below the root rejected")
	}
}

func TestHostMutatingCommandsRefused(t *testing.T) {
	candidateEnv(t)
	for _, name := range []string{"update", "sync", "login", "web", "__respawn"} {
		if err := CheckCommand(name); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s was not refused", name)
		}
	}
	if err := CheckCommand("controller"); err != nil {
		t.Fatalf("controller must stay available: %v", err)
	}
}
