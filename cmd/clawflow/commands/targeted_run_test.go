package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/acceptance"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// targetedVCS is an in-memory GitHub for runOnce tests: repo o/r with the
// given open issues and no PRs. Requests to any other host fail the test.
func targetedVCS(t *testing.T, issues []map[string]any) {
	t.Helper()
	for _, key := range []string{"GH_TOKEN", "GITLAB_TOKEN"} {
		t.Setenv(key, "targeted-test-only")
	}
	t.Setenv("CLAWFLOW_PILOT_BUDGET_PATH", "")
	original := http.DefaultTransport
	http.DefaultTransport = reopenedLifecycleTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "vcs.invalid" {
			return nil, fmt.Errorf("unexpected external request: %s", r.URL.Host)
		}
		var body any = []any{}
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/o/r":
			body = map[string]int{"id": 1}
		case r.Method == "GET" && r.URL.Path == "/repos/o/r/issues":
			body = issues
		case r.Method == "GET" && (strings.HasSuffix(r.URL.Path, "/sub_issues") || strings.HasSuffix(r.URL.Path, "/events") || r.URL.Path == "/repos/o/r/pulls"):
		default:
			return nil, fmt.Errorf("unexpected fake VCS call: %s %s", r.Method, r.URL.Path)
		}
		data, _ := json.Marshal(body)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data))), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
}

func targetedIssue(n int, labels ...string) map[string]any {
	ls := []map[string]string{}
	for _, l := range labels {
		ls = append(ls, map[string]string{"name": l})
	}
	return map[string]any{"id": n, "number": n, "title": fmt.Sprintf("Issue %d", n), "body": "body", "state": "open", "labels": ls, "html_url": fmt.Sprintf("https://example.invalid/o/r/issues/%d", n)}
}

// recordDispatch stubs the executor and returns the sorted issue numbers it saw.
func recordDispatch(t *testing.T) func() []int {
	t.Helper()
	stubDispatchCoordination(t)
	var mu sync.Mutex
	var got []int
	old := runOneOperatorFn
	runOneOperatorFn = func(_ context.Context, j *runJob, _ time.Duration) (bool, bool, bool) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, j.sub.Number)
		return true, false, false
	}
	t.Cleanup(func() { runOneOperatorFn = old })
	return func() []int {
		mu.Lock()
		defer mu.Unlock()
		out := append([]int(nil), got...)
		sort.Ints(out)
		return out
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	fn()
	os.Stderr = old
	w.Close()
	return <-done
}

func targetedConfig(t *testing.T, mutate func(*config.Repo)) {
	t.Helper()
	// Scheduler housekeeping must never scan the host temp root in tests.
	t.Setenv("TMPDIR", t.TempDir())
	repo := config.Repo{Enabled: false, Platform: "github", BaseURL: "https://vcs.invalid", LocalPath: t.TempDir(), BaseBranch: "main"}
	if mutate != nil {
		mutate(&repo)
	}
	writeTestConfig(t, &config.Config{Repos: map[string]config.Repo{"o/r": repo}})
}

func TestResolveRunRepos(t *testing.T) {
	cfg := &config.Config{Repos: map[string]config.Repo{
		"o/on":  {Enabled: true},
		"o/off": {Enabled: false},
	}}
	cases := []struct {
		name       string
		repo       string
		issue      int
		wantRepos  []string
		wantHidden string
		wantErr    string
	}{
		{name: "untargeted", wantRepos: []string{"o/on"}},
		{name: "repo only enabled", repo: "o/on", wantRepos: []string{"o/on"}},
		{name: "repo only disabled", repo: "o/off", wantErr: "not found or not enabled"},
		{name: "repo only unknown", repo: "o/none", wantErr: "not found or not enabled"},
		{name: "targeted enabled", repo: "o/on", issue: 3, wantRepos: []string{"o/on"}},
		{name: "targeted disabled", repo: "o/off", issue: 3, wantRepos: []string{"o/off", "o/on"}, wantHidden: "o/off"},
		{name: "targeted unknown", repo: "o/none", issue: 3, wantErr: "not found in config"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repos, hidden, err := resolveRunRepos(cfg, tc.repo, tc.issue)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for n := range repos {
				names = append(names, n)
			}
			sort.Strings(names)
			if strings.Join(names, ",") != strings.Join(tc.wantRepos, ",") || hidden != tc.wantHidden {
				t.Fatalf("repos=%v hidden=%q", names, hidden)
			}
		})
	}
	if len(cfg.EnabledRepos()) != 1 {
		t.Fatal("resolveRunRepos must not mutate config")
	}
}

func TestRunOnceTargetedDisabledRepoDispatchesOnlyTarget(t *testing.T) {
	withRunLog(t)
	targetedConfig(t, nil)
	targetedVCS(t, []map[string]any{targetedIssue(5), targetedIssue(7), targetedIssue(9)})
	dispatched := recordDispatch(t)

	if err := runOnce(context.Background(), "o/r", 7, time.Minute); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if got := dispatched(); len(got) != 1 || got[0] != 7 {
		t.Fatalf("dispatched = %v, want [7]", got)
	}
	// The disabled repo stays out of the pending and issues dashboard snapshots.
	for _, name := range []string{"pending.json", "issues.json"} {
		data, err := os.ReadFile(filepath.Join(snapshot.DataDir(), name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.Contains(string(data), "o/r") {
			t.Errorf("%s exposes the disabled repo: %s", name, data)
		}
	}
}

func TestRunOnceEnabledRepoStillSnapshotsIssues(t *testing.T) {
	withRunLog(t)
	targetedConfig(t, func(r *config.Repo) { r.Enabled = true })
	targetedVCS(t, []map[string]any{targetedIssue(5), targetedIssue(7)})
	dispatched := recordDispatch(t)

	if err := runOnce(context.Background(), "o/r", 7, time.Minute); err != nil {
		t.Fatalf("runOnce: %v", err)
	}
	if got := dispatched(); len(got) != 1 || got[0] != 7 {
		t.Fatalf("dispatched = %v, want [7]", got)
	}
	data, err := os.ReadFile(filepath.Join(snapshot.DataDir(), "issues.json"))
	if err != nil || !strings.Contains(string(data), "o/r") {
		t.Fatalf("enabled repo missing from issues snapshot: %v %s", err, data)
	}
}

func TestRunOnceDisabledRepoWithoutIssueErrors(t *testing.T) {
	withRunLog(t)
	targetedConfig(t, nil)
	dispatched := recordDispatch(t)
	err := runOnce(context.Background(), "o/r", 0, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "not found or not enabled") {
		t.Fatalf("err = %v", err)
	}
	if len(dispatched()) != 0 {
		t.Fatal("nothing may be dispatched")
	}
}

func TestRunOnceUnknownRepoErrors(t *testing.T) {
	withRunLog(t)
	targetedConfig(t, nil)
	dispatched := recordDispatch(t)
	for _, issue := range []int{0, 7} {
		if err := runOnce(context.Background(), "o/none", issue, time.Minute); err == nil {
			t.Fatalf("issue=%d: expected error for unknown repo", issue)
		}
	}
	if len(dispatched()) != 0 {
		t.Fatal("nothing may be dispatched")
	}
}

func TestRunOnceTargetedIgnoresRetiredBinding(t *testing.T) {
	host, _ := os.Hostname()
	if host == "" {
		t.Skip("hostname unavailable")
	}
	cases := []struct {
		name    string
		bound   string
		require bool
	}{
		{name: "bound elsewhere", bound: host + "-other"},
		{name: "require_binding unbound", require: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withRunLog(t)
			repo := config.Repo{Platform: "github", BaseURL: "https://vcs.invalid", LocalPath: t.TempDir(), BoundMachine: tc.bound}
			writeTestConfig(t, &config.Config{Settings: config.Settings{RequireBinding: tc.require}, Repos: map[string]config.Repo{"o/r": repo}})
			targetedVCS(t, []map[string]any{targetedIssue(7)})
			dispatched := recordDispatch(t)
			if err := runOnce(context.Background(), "o/r", 7, time.Minute); err != nil {
				t.Fatal(err)
			}
			if len(dispatched()) != 1 {
				t.Fatalf("legacy binding prevented dispatch: %v", dispatched())
			}
		})
	}
}

func TestRunOnceTargetNoMatchReportsReason(t *testing.T) {
	withRunLog(t)
	targetedConfig(t, nil)
	targetedVCS(t, []map[string]any{targetedIssue(7, "agent-skipped", "docs")})
	dispatched := recordDispatch(t)

	var err error
	out := captureStderr(t, func() { err = runOnce(context.Background(), "o/r", 7, time.Minute) })
	if err != nil {
		t.Fatalf("no match must not fail the run: %v", err)
	}
	if len(dispatched()) != 0 {
		t.Fatalf("dispatched %v", dispatched())
	}
	want := "[o/r#7] no operator matches this issue (labels: agent-skipped, docs)"
	if !strings.Contains(out, want) {
		t.Fatalf("stderr lacks %q:\n%s", want, out)
	}
	logData, _ := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".clawflow", "logs", "run.log"))
	if !strings.Contains(string(logData), "run/target_skip") {
		t.Fatalf("run.log lacks run/target_skip:\n%s", logData)
	}
}

func TestHarvestTargetedDisabledRepoOnlyConsidersTargetPR(t *testing.T) {
	f, _, calls := harvestFixture(t)
	setHarvestRepo(t, func(r *config.Repo) { r.Enabled = false })
	other := f.pr
	other.Number = 12
	other.Body = "Fixes #43"
	// The other PR comes first; targeting #42 must skip past it.
	f.openPRs = []vcs.PR{other, f.pr}
	var prs []int
	runAcceptanceFn = func(_ context.Context, c vcs.Client, repo string, pr int) (acceptance.Record, error) {
		prs = append(prs, pr)
		return runAcceptance(context.Background(), c, repo, pr)
	}
	_ = calls
	if failed := harvest(t, "o/r", 42); len(failed) != 0 {
		t.Fatalf("failed: %v", failed)
	}
	if len(prs) != 1 || prs[0] != 11 {
		t.Fatalf("acceptance PRs = %v, want [11]", prs)
	}
	if len(f.expected) != 1 {
		t.Fatalf("conditional merges = %v", f.expected)
	}
}

func TestHarvestTargetedIgnoresOtherPRsAndUnknownRepo(t *testing.T) {
	f, _, calls := harvestFixture(t)
	setHarvestRepo(t, func(r *config.Repo) { r.Enabled = false })
	if failed := harvest(t, "o/r", 99); len(failed) != 0 || *calls != 0 || len(f.expected) != 0 {
		t.Fatalf("PR closing another issue was processed: failed=%v runs=%d", failed, *calls)
	}
	if failed := harvest(t, "o/none", 42); len(failed) != 0 || *calls != 0 {
		t.Fatalf("unknown repo processed: failed=%v runs=%d", failed, *calls)
	}
}

func TestHarvestTargetedStillRequiresEnrollment(t *testing.T) {
	_, _, calls := harvestFixture(t)
	setHarvestRepo(t, func(r *config.Repo) { r.Enabled = false; r.AcceptanceRequired = false })
	if failed := harvest(t, "o/r", 42); len(failed) != 0 || *calls != 0 {
		t.Fatalf("unenrolled repo harvested: failed=%v runs=%d", failed, *calls)
	}
}

func TestHarvestUntargetedSkipsDisabledRepo(t *testing.T) {
	_, _, calls := harvestFixture(t)
	setHarvestRepo(t, func(r *config.Repo) { r.Enabled = false })
	if failed := harvest(t, "", 0); len(failed) != 0 || *calls != 0 {
		t.Fatalf("disabled repo harvested without --issue: failed=%v runs=%d", failed, *calls)
	}
	if failed := harvest(t, "o/r", 0); len(failed) != 0 || *calls != 0 {
		t.Fatalf("disabled repo harvested with --repo only: failed=%v runs=%d", failed, *calls)
	}
}

func TestSweepAutoMergePRsTargetedOnlyMergesTargetPR(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := (&config.Config{Repos: map[string]config.Repo{"o/r": {}}}).Save(); err != nil {
		t.Fatal(err)
	}
	prs := []vcs.PR{
		{Number: 20, Title: "other", Body: "Closes #5", HeadBranch: "fix/issue-5"},
		{Number: 21, Title: "target", Body: "Closes #7", HeadBranch: "fix/issue-7"},
		{Number: 22, Title: "no keyword", Body: "plain"},
	}
	f := &sweepFake{openPRs: prs}
	sweepAutoMergePRs(context.Background(), f, "o/r", config.Repo{AutoMerge: true}, 7)
	if len(f.merged) != 1 || f.merged[0] != 21 {
		t.Fatalf("targeted sweep merged %v, want [21]", f.merged)
	}

	// Untargeted sweep is unchanged: the first closing PR in list order is
	// merged (the per-pass cap is 1).
	f = &sweepFake{openPRs: prs}
	sweepAutoMergePRs(context.Background(), f, "o/r", config.Repo{AutoMerge: true}, 0)
	if len(f.merged) != 1 || f.merged[0] != 20 {
		t.Fatalf("untargeted sweep merged %v, want [20]", f.merged)
	}
}

// Selection and legacy wire-protocol fixtures stub the new execution preflight;
// its real collection, model, effects and DB path have dedicated tests.
func stubDispatchCoordination(t *testing.T) {
	t.Helper()
	old := coordinateDispatch
	coordinateDispatch = func(context.Context, *runJob, coordinationTasks) (bool, error) { return true, nil }
	t.Cleanup(func() { coordinateDispatch = old })
}
