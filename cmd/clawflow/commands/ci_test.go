package commands

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/pilot/budget"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

func TestCIJobPlay(t *testing.T) {
	for _, mode := range []string{"text", "json", "budget exhausted", "API error"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.EscapedPath() != "/api/v4/projects/group%2Fsub%2Frepo/jobs/42/play" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.EscapedPath())
				}
				if mode == "API error" {
					http.Error(w, "not playable", 400)
					return
				}
				json.NewEncoder(w).Encode(gitlab.Job{ID: 42, Name: "deploy", Status: "pending", WebURL: "https://gl/job/42"})
			}))
			defer srv.Close()
			writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{
				"group/sub/repo": {Platform: "gitlab", BaseURL: srv.URL},
			}})
			path := filepath.Join(t.TempDir(), "budget.json")
			if err := budget.Init(path, 1); err != nil {
				t.Fatal(err)
			}
			t.Setenv(budget.EnvPath, path)
			if mode == "budget exhausted" {
				if err := budget.Reserve("prior write"); err != nil {
					t.Fatal(err)
				}
			}
			cmd := NewCICmd()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			args := []string{"job", "play", "--repo", srv.URL + "/group/sub/repo", "--id", "42"}
			if mode == "json" {
				args[1] = "trigger"
				args = append(args, "--json")
			}
			cmd.SetArgs(args)
			err := cmd.Execute()
			if mode == "budget exhausted" {
				if err == nil || !strings.Contains(err.Error(), "budget exhausted") || calls != 0 {
					t.Fatalf("expected budget rejection without request: calls=%d err=%v", calls, err)
				}
				return
			}
			if calls != 1 {
				t.Fatalf("expected exactly one request, got %d", calls)
			}
			if mode == "API error" {
				if err == nil || !strings.Contains(err.Error(), "not playable") {
					t.Fatalf("expected GitLab error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "json" {
				var job gitlab.Job
				if err := json.Unmarshal(out.Bytes(), &job); err != nil || job.ID != 42 || job.Status != "pending" {
					t.Fatalf("unexpected JSON: %s (%v)", out.String(), err)
				}
			} else if !strings.Contains(out.String(), "Triggered job #42 deploy [pending]") || !strings.Contains(out.String(), "https://gl/job/42") {
				t.Fatalf("unexpected output: %s", out.String())
			}
			state, err := budget.Read(path)
			if err != nil || state.Used != 1 {
				t.Fatalf("write budget not consumed: %+v %v", state, err)
			}
		})
	}
}

func TestCIJobPlayInvalidArgs(t *testing.T) {
	for _, args := range [][]string{
		{"job", "play"},
		{"job", "play", "--repo", "group/repo"},
		{"job", "play", "--id", "42"},
		{"job", "play", "--repo", "group/repo", "--id", "0"},
		{"job", "play", "--repo", "group/repo", "--id", "-1"},
		{"job", "play", "--repo", "group/repo", "--id", "42", "extra"},
	} {
		cmd := NewCICmd()
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil {
			t.Errorf("expected argument error for %v", args)
		}
	}
}

func TestStripANSI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b[0Krunning\x1b[0;m", "running"},
		{"\x1b[32;1mok\x1b[0m\n", "ok\n"},
		{"plain text", "plain text"},
		{"", ""},
	}
	for _, c := range cases {
		if got := stripANSI(c.in); got != c.want {
			t.Errorf("stripANSI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStripSections(t *testing.T) {
	// Marker + CR followed by real content on the same physical line.
	in := "section_start:1782893292:prepare_script\rWaiting for pod\nsection_end:1782893298:prepare_script\rDone\n"
	want := "Waiting for pod\nDone\n"
	if got := stripSections(in); got != want {
		t.Errorf("stripSections = %q, want %q", got, want)
	}
	// Collapsed-section option syntax must also be consumed.
	if got := stripSections("section_start:1:step_script[collapsed=true]\rrun\n"); got != "run\n" {
		t.Errorf("stripSections collapsed = %q", got)
	}
	// Non-marker text is untouched.
	if got := stripSections("no markers here\n"); got != "no markers here\n" {
		t.Errorf("stripSections passthrough = %q", got)
	}
}

func TestShortSHA(t *testing.T) {
	if got := shortSHA("3db92cf9abcdef"); got != "3db92cf9" {
		t.Errorf("shortSHA long = %q, want 3db92cf9", got)
	}
	if got := shortSHA("abc"); got != "abc" {
		t.Errorf("shortSHA short = %q, want abc", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("main", 24); got != "main" {
		t.Errorf("truncate no-op = %q", got)
	}
	if got := truncate("release/v0.0.0-smoke-longbranch", 12); got != "release/v0.…" {
		t.Errorf("truncate = %q", got)
	}
}

func TestGitHubCIJobLogRequiresSHAAndRejectsRaw(t *testing.T) {
	writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{"o/r": {Platform: "github"}}})
	for _, extra := range [][]string{nil, {"--sha", "main"}, {"--sha", strings.Repeat("a", 40), "--raw"}} {
		cmd := NewCICmd()
		cmd.SetArgs(append([]string{"job", "log", "--repo", "o/r"}, extra...))
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		if err := cmd.Execute(); err == nil {
			t.Fatal("unsafe query accepted")
		}
	}
}
