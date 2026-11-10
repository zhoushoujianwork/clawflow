package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/pipeline"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func stoppedDeps(t *testing.T, repos map[string]config.Repo, runs []snapshot.RunIndexEntry, issues []snapshot.IssueEntry) pipelineDeps {
	t.Helper()
	d := decisionsTestDeps(&decisionsFake{}, repos)
	d.newClient = func(config.Repo) (vcs.Client, error) {
		t.Fatal("a stopped repository must not create a provider client")
		return nil, nil
	}
	d.runs = func() []snapshot.RunIndexEntry { return runs }
	d.issues = func() []snapshot.IssueEntry { return issues }
	d.globalQueue = func() []queue.ListEntry {
		return []queue.ListEntry{{Position: 1, Key: "grp/smoke#1", Repo: "grp/smoke", Number: 1, Title: "stale queue row", State: "eligible"}}
	}
	return d
}

func failedImplementRun(repo string, issue int) snapshot.RunIndexEntry {
	end := decisionsT0.Add(-time.Hour).Add(18 * time.Second)
	code := 1
	var m snapshot.RunIndexEntry
	m.Repo, m.IssueNumber, m.IssueTitle, m.Operator, m.Status = repo, issue, "greeting name argument", "implement", "failed"
	m.StartedAt, m.EndedAt = decisionsT0.Add(-time.Hour), &end
	m.Error = "Invalid prompt: flagged"
	m.ExitCode, m.ErrorKind, m.ExecutionStatus = &code, "execution", "error"
	m.OffloadRunID, m.Machine = "off-run-1", "exec-host"
	m.Path = "./data/runs/grp__smoke/issue-1/2026-10-27T11-00-00Z/"
	return m
}

// A disabled repository keeps its issues and failed attempts on the board,
// marked stopped, without any provider request or queue entry.
func TestStoppedRepositoryKeepsIssuesAndFailedAttempts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	repos := map[string]config.Repo{
		"grp/smoke": {Platform: "gitlab", BaseURL: "https://gitlab.example.test", CIRequired: true},
		"o/live":    {Enabled: true},
	}
	issues := []snapshot.IssueEntry{{Repo: "grp/smoke", IssueNumber: 1, IssueTitle: "greeting name argument", State: "open", UpdatedAt: "2026-10-27T10:00:00Z"}}
	d := stoppedDeps(t, repos, []snapshot.RunIndexEntry{failedImplementRun("grp/smoke", 1)}, issues)
	var created []string
	d.newClient = func(rc config.Repo) (vcs.Client, error) {
		created = append(created, rc.BaseURL)
		return &decisionsFake{}, nil
	}

	snap := buildPipeline(context.Background(), d)

	var stopped, live *pipeline.RepoBoard
	for i := range snap.Repos {
		switch snap.Repos[i].Repo {
		case "grp/smoke":
			stopped = &snap.Repos[i]
		case "o/live":
			live = &snap.Repos[i]
		}
	}
	if stopped == nil || live == nil {
		t.Fatalf("both registered repositories need a board: %+v", snap.Repos)
	}
	if !stopped.Stopped || stopped.Enabled || live.Stopped {
		t.Fatalf("stopped flag: stopped=%+v live=%+v", stopped.Stopped, live.Stopped)
	}
	if len(created) != 1 || created[0] != "" {
		t.Fatalf("only the enabled repository may reach a provider, got clients for %v", created)
	}
	if len(stopped.Items) != 1 || stopped.Items[0].Issue != 1 {
		t.Fatalf("issue must stay visible: %+v", stopped.Items)
	}
	item := stopped.Items[0]
	if item.Stage != pipeline.StageFailed || item.Run == nil || item.Run.Status != "failed" {
		t.Fatalf("failure must stay visible: stage=%s run=%+v", item.Stage, item.Run)
	}
	if len(item.Executions) != 1 || item.Executions[0].RunPath == "" || item.Executions[0].Status != "error" {
		t.Fatalf("attempt must be traceable: %+v", item.Executions)
	}
	if !strings.Contains(strings.Join(item.Evidence, " "), "automation is off") {
		t.Fatalf("evidence must say automation is off: %v", item.Evidence)
	}
	if stopped.URL != "https://gitlab.example.test/grp/smoke" {
		t.Fatalf("repo URL: %q", stopped.URL)
	}
	for _, q := range snap.GlobalQueue {
		if q.Repo == "grp/smoke" {
			t.Fatalf("a stopped repository must not appear in the dispatch queue: %+v", q)
		}
	}
}

// An idle issue of a stopped repository is "not processed", not "waiting for the next run".
func TestStoppedRepositoryIdleIssueIsNotQueued(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	issues := []snapshot.IssueEntry{{Repo: "grp/smoke", IssueNumber: 2, IssueTitle: "idle", State: "open"}}
	d := stoppedDeps(t, map[string]config.Repo{"grp/smoke": {}}, nil, issues)
	snap := buildPipeline(context.Background(), d)
	got := snap.Repos[0].Items
	if len(got) != 1 || !strings.Contains(got[0].Why, "automation is off") || got[0].InQueue {
		t.Fatalf("idle issue: %+v", got)
	}
}

// Merged history and closed issues with no record behave as on a live board.
func TestStoppedRepositoryReusesPreviousBoardAndDropsEmptyClosedIssues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	issues := []snapshot.IssueEntry{
		{Repo: "grp/smoke", IssueNumber: 3, IssueTitle: "shipped", State: "closed"},
		{Repo: "grp/smoke", IssueNumber: 4, IssueTitle: "closed untouched", State: "closed"},
	}
	d := stoppedDeps(t, map[string]config.Repo{"grp/smoke": {}}, nil, issues)
	d.previous = map[string]pipeline.RepoBoard{"grp/smoke": {Repo: "grp/smoke", Items: []pipeline.Item{{
		Issue: 3, Title: "shipped", State: "closed", PR: &pipeline.PRInfo{Number: 9, State: "merged"},
		Delivery: &pipeline.Delivery{PR: 9, MergedAt: "2026-10-20T00:00:00Z"},
	}}}}
	snap := buildPipeline(context.Background(), d)
	items := snap.Repos[0].Items
	if len(items) != 1 || items[0].Issue != 3 || items[0].Stage != pipeline.StageMerged || items[0].Delivery == nil {
		t.Fatalf("merged history must be kept and the untouched closed issue dropped: %+v", items)
	}
}

// Building a stopped board changes no configuration and writes nothing.
func TestStoppedBoardDoesNotWriteOrReenable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	state := t.TempDir()
	t.Setenv("CLAWFLOW_STATE_DIR", state)
	cfg := map[string]config.Repo{"grp/smoke": {}}
	d := stoppedDeps(t, cfg, []snapshot.RunIndexEntry{failedImplementRun("grp/smoke", 1)}, nil)
	_ = buildPipeline(context.Background(), d)
	if cfg["grp/smoke"].Enabled || cfg["grp/smoke"].AutoMerge {
		t.Fatal("building the board changed the repository configuration")
	}
	entries, _ := os.ReadDir(state)
	if len(entries) != 0 {
		t.Fatalf("building the board wrote state: %v", entries)
	}
}

// A pass keeps the recorded issues of a disabled repository and of one another
// pass owns, and drops those of a repository that is no longer registered.
func TestIssueSnapshotKeepsDisabledRepositoryEntries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	if err := os.MkdirAll(snapshot.DataDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	old := []snapshot.IssueEntry{
		{Repo: "grp/smoke", IssueNumber: 1, State: "open", IssueTitle: "disabled, kept"},
		{Repo: "o/other", IssueNumber: 2, State: "open", IssueTitle: "owned by another pass"},
		{Repo: "o/gone", IssueNumber: 3, State: "open", IssueTitle: "unregistered"},
	}
	b, _ := json.Marshal(old)
	if err := os.WriteFile(filepath.Join(snapshot.DataDir(), "issues.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Repos: map[string]config.Repo{"grp/smoke": {}, "o/other": {Enabled: true}, "o/live": {Enabled: true}}}
	keep := issueSnapshotKeep(cfg, map[string]bool{"o/other": true})
	if !keep["grp/smoke"] || !keep["o/other"] || keep["o/live"] || keep["o/gone"] {
		t.Fatalf("keep set: %v", keep)
	}
	fresh := []snapshot.IssueEntry{{Repo: "o/live", IssueNumber: 5, State: "open"}}
	if err := snapshot.WriteIssuesMerged(fresh, keep); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range readIssueSnapshot() {
		got[e.Repo] = true
	}
	if !got["grp/smoke"] || !got["o/other"] || !got["o/live"] || got["o/gone"] {
		t.Fatalf("snapshot repos after a pass: %v", got)
	}
}
