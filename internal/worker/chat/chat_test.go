package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// silenceLogs swaps the package-level stderr sink for the duration of
// the test so noisy "post events" lines from negative paths don't
// pollute test output. Restored via t.Cleanup.
func silenceLogs(t *testing.T) {
	t.Helper()
	prev := stderrSink
	stderrSink = io.Discard
	t.Cleanup(func() { stderrSink = prev })
}

func TestValidateRepo(t *testing.T) {
	cases := []struct {
		in    string
		wantE bool
	}{
		{"owner/name", false},
		{"foo-bar/baz_qux", false},
		{"a/b/c", false}, // GitLab nested path is allowed
		{"", true},
		{"../etc/passwd", true},
		{"foo/bar/../baz", true},
		{"/abs/path", true},
		{".hidden/repo", true},
		{"owner/.git", true},
		{"owner/name\nrm", true},
		{"./relative", true},
	}
	for _, tc := range cases {
		err := validateRepo(tc.in)
		if (err != nil) != tc.wantE {
			t.Errorf("validateRepo(%q) err=%v wantErr=%v", tc.in, err, tc.wantE)
		}
	}
}

func TestPickProvider(t *testing.T) {
	silenceLogs(t)

	t.Run("nil credentials", func(t *testing.T) {
		if _, _, err := pickProvider(nil); err == nil {
			t.Fatal("expected error for nil credentials")
		}
	})

	t.Run("first enabled wins", func(t *testing.T) {
		c := &config.Credentials{
			ClaudeProviders: []config.ClaudeProvider{
				{Name: "disabled1", APIKey: "k1", Enabled: false},
				{Name: "disabled2", APIKey: "k2", Enabled: false},
				{Name: "first-enabled", APIKey: "want", BaseURL: "https://relay/", Enabled: true},
				{Name: "second-enabled", APIKey: "skip", Enabled: true},
			},
		}
		apiKey, baseURL, err := pickProvider(c)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if apiKey != "want" || baseURL != "https://relay/" {
			t.Fatalf("got apiKey=%q baseURL=%q", apiKey, baseURL)
		}
	})

	t.Run("no enabled provider", func(t *testing.T) {
		c := &config.Credentials{
			ClaudeProviders: []config.ClaudeProvider{
				{Name: "a", APIKey: "k", Enabled: false},
			},
		}
		if _, _, err := pickProvider(c); err == nil {
			t.Fatal("expected error when no provider enabled")
		}
	})

	t.Run("legacy fallback", func(t *testing.T) {
		c := &config.Credentials{ClaudeAPIKey: "legacy-k"}
		apiKey, _, err := pickProvider(c)
		if err != nil || apiKey != "legacy-k" {
			t.Fatalf("legacy fallback: apiKey=%q err=%v", apiKey, err)
		}
	})

	t.Run("oauth entry returns empty", func(t *testing.T) {
		c := &config.Credentials{
			ClaudeProviders: []config.ClaudeProvider{
				{Name: "oauth", Enabled: true}, // no APIKey, no BaseURL
			},
		}
		apiKey, baseURL, err := pickProvider(c)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if apiKey != "" || baseURL != "" {
			t.Fatalf("expected empty pair for oauth, got %q %q", apiKey, baseURL)
		}
	})
}

func TestBuildCloneURL(t *testing.T) {
	cases := []struct {
		name      string
		platform  string
		repo      string
		baseURL   string
		ghToken   string
		glToken   string
		want      string
		wantError bool
	}{
		{
			name:    "github with token",
			platform: "github", repo: "foo/bar", ghToken: "ghs_abc",
			want: "https://x-access-token:ghs_abc@github.com/foo/bar.git",
		},
		{
			name:    "github default platform",
			platform: "", repo: "foo/bar", ghToken: "tok",
			want: "https://x-access-token:tok@github.com/foo/bar.git",
		},
		{
			name:    "github no token falls back to public",
			platform: "github", repo: "foo/bar",
			want: "https://github.com/foo/bar.git",
		},
		{
			name:    "gitlab.com with token",
			platform: "gitlab", repo: "ns/proj", glToken: "glpat_xyz",
			want: "https://oauth2:glpat_xyz@gitlab.com/ns/proj.git",
		},
		{
			name:    "gitlab self-hosted https",
			platform: "gitlab", repo: "ns/proj", baseURL: "https://gitlab.example.com",
			glToken: "tk",
			want:    "https://oauth2:tk@gitlab.example.com/ns/proj.git",
		},
		{
			name:    "gitlab self-hosted http",
			platform: "gitlab", repo: "ns/proj", baseURL: "http://git.local:8080",
			glToken: "tk",
			want:    "http://oauth2:tk@git.local:8080/ns/proj.git",
		},
		{
			name:     "unknown platform errors",
			platform: "bitbucket", repo: "foo/bar", wantError: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &cloud.ChatAssignment{Platform: tc.platform, Repo: tc.repo, BaseURL: tc.baseURL}
			c := &config.Credentials{GHToken: tc.ghToken, GitLabToken: tc.glToken}
			got, err := buildCloneURL(a, c)
			if (err != nil) != tc.wantError {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantError)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// fakeClaudeBin writes a tiny script to dir that emits a marker to
// stdout, a marker to stderr, and exits 0. Returns the script path.
// Skipped on Windows because we use a bash shebang.
func fakeClaudeBin(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("posix-only test")
	}
	p := filepath.Join(dir, "fake-claude.sh")
	script := "#!/bin/sh\n" +
		"echo hello-from-claude\n" +
		"echo warn-line >&2\n" +
		"exit 0\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// initBareRepo creates a minimal .git directory at dir so the worker's
// "is this a clone?" check passes without us actually running git.
// We do it via plumbing rather than `git init` to keep the test fast
// and dependency-free.
func initLocalClone(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// fakeCloud is a minimal httptest.Server that records WorkerEvents
// posts. Use start() to spin one up and stop via Close.
type fakeCloud struct {
	server *httptest.Server

	mu     sync.Mutex
	events []cloud.ChatEvent
}

func newFakeCloud(t *testing.T) *fakeCloud {
	t.Helper()
	fc := &fakeCloud{}
	fc.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/events") {
			http.NotFound(w, r)
			return
		}
		var req cloud.WorkerEventsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fc.mu.Lock()
		fc.events = append(fc.events, req.Events...)
		fc.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(fc.server.Close)
	return fc
}

func (fc *fakeCloud) snapshot() []cloud.ChatEvent {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	out := make([]cloud.ChatEvent, len(fc.events))
	copy(out, fc.events)
	return out
}

func TestRunSession_HappyPath(t *testing.T) {
	silenceLogs(t)

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	tmp := t.TempDir()
	clonesDir := filepath.Join(tmp, "clones")

	// Pre-populate the workdir so the worker skips clone but still
	// hits the fetch+reset path — which we test separately. To bypass
	// the network-touching fetch entirely, point HOME to a fresh dir
	// so config.Load() can't find a LocalPath override, and instead
	// pre-create a fake clone where the worker expects it. Then
	// shim git to a no-op script.
	a := &cloud.ChatAssignment{
		SessionID: "sess-1",
		Repo:      "happy/path",
		Platform:  "github",
		Message:   "hi",
	}
	workdir := filepath.Join(clonesDir, "happy", "path")
	initLocalClone(t, workdir)

	// Shim git so `git -C dir fetch / reset` succeed without network.
	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitShim := filepath.Join(binDir, "git")
	if err := os.WriteFile(gitShim, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Isolate HOME so config.Load() finds nothing (forces the
	// ClonesDir fallback) and PATH so our fake git is picked up.
	fakeHome := filepath.Join(tmp, "home")
	if err := os.MkdirAll(fakeHome, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", fakeHome)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	claudeBin := fakeClaudeBin(t, binDir)

	fc := newFakeCloud(t)
	client, err := cloud.NewClient(cloud.Config{BaseURL: fc.server.URL, WorkerToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}

	creds := &config.Credentials{
		ClaudeProviders: []config.ClaudeProvider{
			{Name: "test", APIKey: "sk-test", Enabled: true},
		},
	}

	loop := NewLoop(Config{
		Client:    client,
		Creds:     creds,
		ClonesDir: clonesDir,
		ClaudeBin: claudeBin,
	})
	loop.initClients()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := runSession(ctx, loop, a); err != nil {
		t.Fatalf("runSession: %v", err)
	}

	evts := fc.snapshot()
	if len(evts) == 0 {
		t.Fatal("no events received")
	}

	var sawOutput, sawEnd, sawStderr bool
	var combined strings.Builder
	for _, e := range evts {
		combined.WriteString(string(e.Type))
		combined.WriteString(":")
		combined.WriteString(e.Text)
		combined.WriteString("\n")
		switch e.Type {
		case cloud.ChatEventOutput:
			sawOutput = true
		case cloud.ChatEventEnd:
			sawEnd = true
		case cloud.ChatEventStderr:
			sawStderr = true
		}
	}
	if !sawOutput {
		t.Errorf("missing output event:\n%s", combined.String())
	}
	if !sawStderr {
		t.Errorf("missing stderr event:\n%s", combined.String())
	}
	if !sawEnd {
		t.Errorf("missing end event:\n%s", combined.String())
	}
	if !strings.Contains(combined.String(), "hello-from-claude") {
		t.Errorf("expected stdout to contain marker; got:\n%s", combined.String())
	}
}

func TestRunSession_PathTraversalRejected(t *testing.T) {
	silenceLogs(t)
	fc := newFakeCloud(t)
	client, err := cloud.NewClient(cloud.Config{BaseURL: fc.server.URL, WorkerToken: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	loop := NewLoop(Config{
		Client:    client,
		Creds:     &config.Credentials{ClaudeProviders: []config.ClaudeProvider{{Name: "x", APIKey: "k", Enabled: true}}},
		ClonesDir: t.TempDir(),
		ClaudeBin: "/bin/true",
	})
	loop.initClients()

	a := &cloud.ChatAssignment{SessionID: "ss", Repo: "../escape", Platform: "github", Message: "hi"}
	if err := runSession(context.Background(), loop, a); err == nil {
		t.Fatal("expected error for path traversal repo")
	}
	evts := fc.snapshot()
	if len(evts) == 0 || evts[0].Type != cloud.ChatEventError {
		t.Fatalf("expected ChatEventError, got %#v", evts)
	}
}

func TestPoll_NoContentReturnsNil(t *testing.T) {
	silenceLogs(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/worker/chat/poll" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer wt" {
			t.Errorf("auth header = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	client, err := cloud.NewClient(cloud.Config{BaseURL: srv.URL, WorkerToken: "wt"})
	if err != nil {
		t.Fatal(err)
	}
	loop := NewLoop(Config{Client: client, Creds: &config.Credentials{}})
	loop.initClients()
	a, err := loop.poll(context.Background(), "m", "w")
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if a != nil {
		t.Fatalf("expected nil assignment, got %+v", a)
	}
}

func TestPoll_DecodesAssignment(t *testing.T) {
	silenceLogs(t)
	want := cloud.ChatAssignment{SessionID: "s-1", Repo: "owner/name", Platform: "github", Message: "hi"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cloud.ChatPollResponse{Assignment: &want})
	}))
	defer srv.Close()
	client, err := cloud.NewClient(cloud.Config{BaseURL: srv.URL, WorkerToken: "wt"})
	if err != nil {
		t.Fatal(err)
	}
	loop := NewLoop(Config{Client: client, Creds: &config.Credentials{}})
	loop.initClients()
	got, err := loop.poll(context.Background(), "m", "w")
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if got == nil || got.SessionID != want.SessionID {
		t.Fatalf("got %+v want %+v", got, want)
	}
}
