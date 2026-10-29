package pilot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

func TestStewardCharterSeedAndPreserve(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"one", "two"} {
		if _, err := project.Create(name); err != nil {
			t.Fatal(err)
		}
		if err := project.WriteContext(name, "implementation "+name); err != nil {
			t.Fatal(err)
		}
	}
	body, err := EnsureCharter()
	if err != nil || !strings.Contains(body, "implementation one") || !strings.Contains(body, "implementation two") {
		t.Fatalf("%s %v", body, err)
	}
	if err := os.WriteFile(CharterPath(), []byte("owner charter"), 0600); err != nil {
		t.Fatal(err)
	}
	body, err = EnsureCharter()
	if err != nil || body != "owner charter" {
		t.Fatalf("%q %v", body, err)
	}
}

func TestStewardPlatformAskAndPolicyGates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/repos/b/two/issues/3/comments":
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "global decision") {
				t.Error("missing proposed comment")
			}
			posts.Add(1)
			w.WriteHeader(201)
			fmt.Fprint(w, `{}`)
		case r.Method == "GET" && r.URL.Path == "/repos/b/two/issues/3":
			fmt.Fprint(w, `{"number":3,"state":"open","labels":[]}`)
		case r.Method == "GET" && r.URL.Path == "/repos/b/two/issues/3/comments":
			fmt.Fprint(w, `[]`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg := &config.Config{Repos: map[string]config.Repo{"a/one": {Enabled: true}, "b/two": {Enabled: true, BaseURL: server.URL}, "disabled/repo": {}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.WritePipeline(pipeline.Snapshot{GeneratedAt: "2026-10-29T00:00:00Z", Repos: []pipeline.RepoBoard{{Repo: "b/two", Items: []pipeline.Item{{Issue: 3, Acceptance: &pipeline.AcceptanceInfo{Verdict: "failed", Summary: "recorded finding"}}}}, {Repo: "disabled/repo"}}}); err != nil {
		t.Fatal(err)
	}
	old := runWakeOffload
	defer func() { runWakeOffload = old }()
	runWakeOffload = func(ctx context.Context, prompt, workdir string, timeout time.Duration, events io.Writer, role string, _ ...string) (string, error) {
		for _, want := range []string{"a/one", "b/two", "recorded finding", "owner question", "recent_runs_and_usage", "Ops budget: 1"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("missing %s", want)
			}
		}
		if strings.Contains(prompt, "disabled/repo") {
			t.Fatal("disabled repo in scope")
		}
		if role != config.RolePilot || workdir != filepath.Dir(CharterPath()) {
			t.Fatalf("%s %s", role, workdir)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("missing deadline")
		}
		return "answer\n```pilot-actions\n" + `{"run_id":"proposal","actions":[{"id":"comment","type":"comment","repo":"b/two","issue":3,"body":"global decision"},{"id":"extra","type":"note","repo":"a/one","body":"over budget"},{"id":"skip","type":"label_remove","repo":"a/one","issue":1,"label":"clawflow-skip"},{"id":"merge","type":"override_merge","repo":"b/two","pr":2,"head":"` + strings.Repeat("a", 40) + `"}]}` + "\n```", nil
	}
	result, err := Ask(context.Background(), "owner question", 1, time.Second)
	if err == nil || len(result.Actions) != 4 {
		t.Fatalf("%+v %v", result, err)
	}
	if result.Actions[0].Outcome != "applied" || result.Actions[1].Outcome != "wake_limit" || result.Actions[2].Outcome == "applied" || result.Actions[3].Outcome != "override_merge_disabled" {
		t.Fatalf("%+v", result.Actions)
	}
	if posts.Load() != 1 {
		t.Fatalf("comment effects = %d", posts.Load())
	}
	records, err := ReadActionRecords(filepath.Join(snapshot.DataDir(), "pilot-actions.jsonl"))
	if err != nil || len(records) != 5 {
		t.Fatalf("audit %d %v", len(records), err)
	}
}

func TestStewardPlainAnswerAndDeadline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := (&config.Config{Repos: map[string]config.Repo{}}).Save(); err != nil {
		t.Fatal(err)
	}
	old := runWakeOffload
	defer func() { runWakeOffload = old }()
	for _, expired := range []bool{false, true} {
		runWakeOffload = func(ctx context.Context, _, _ string, _ time.Duration, _ io.Writer, _ string, _ ...string) (string, error) {
			if expired {
				<-ctx.Done()
			}
			return "plain answer", nil
		}
		timeout := time.Second
		if expired {
			timeout = 10 * time.Millisecond
		}
		result, err := Ask(context.Background(), "hello", 5, timeout)
		if result.Answer != "plain answer" || (err != nil) != expired {
			t.Fatal(fmt.Sprint(result, err))
		}
	}
}

func TestStewardAuditContentionRespectsDeadline(t *testing.T) {
	e, _ := testExecutor(t)
	lock, err := os.OpenFile(e.Path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	e.Context = ctx
	_, err = e.Apply(actionSet("contended", `{"id":"note","type":"note","body":"must not run"}`), false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := os.Stat(e.Path); !os.IsNotExist(err) {
		t.Fatal("contention wrote audit", err)
	}
}
