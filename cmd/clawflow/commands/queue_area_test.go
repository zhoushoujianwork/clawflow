package commands

import (
	"context"
	"errors"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func areaInput() repoQueueInput {
	a, b := plannerIssue(1, "2026-01-01"), plannerIssue(2, "2026-01-02")
	a.Body, b.Body = "Edit `internal/a.go`", "Edit `internal/b.go`"
	in := plannerInput("o/r", a, b)
	in.MaxInFlight = 1
	in.OpenPRs = func() ([]vcs.PR, error) {
		return []vcs.PR{{Number: 9, State: "open", HeadBranch: "fix/issue-8", HeadRepo: "o/r"}}, nil
	}
	in.PRFiles = func(int) ([]string, error) { return []string{"internal/a.go"}, nil }
	return in
}

func TestQueueAreaBusyAndIndependentWork(t *testing.T) {
	in := areaInput()
	p := planRepoQueue(in)
	if p.Err != "" || p.Picks[1] || !p.Picks[2] {
		t.Fatalf("unexpected plan: %+v", p)
	}
	e := p.State.Entries[0]
	if e.State != queue.StateParked || e.Reason != "area_busy" || e.BlockingPR != 9 || e.AreaPasses != 1 {
		t.Fatalf("park record: %+v", e)
	}
	rows := queue.BuildList([]queue.RepoState{p.State}, nil, "")
	if !strings.Contains(rows[0].State, "area_busy:PR #9") {
		t.Fatalf("board row: %+v", rows[0])
	}
	in.PRFiles = func(int) ([]string, error) { return []string{"internal/c.go"}, nil }
	in.MaxInFlight = 2
	p = planRepoQueue(in)
	if !p.Picks[1] || !p.Picks[2] {
		t.Fatal("non-overlapping work should run in parallel")
	}
}

func TestQueueAreaNamedFilesWithoutKnownExtension(t *testing.T) {
	for _, name := range []string{"go.mod", "Dockerfile", "api/service.proto", "Buildfile", "scripts/build", ".editorconfig"} {
		t.Run(name, func(t *testing.T) {
			in := areaInput()
			in.Issues[0].Body = "Update `" + name + "`"
			in.PRFiles = func(int) ([]string, error) { return []string{name}, nil }
			p := planRepoQueue(in)
			if p.Err != "" || p.Picks[1] || !p.Picks[2] {
				t.Fatalf("overlapping named file must park while independent work starts: %+v", p)
			}
			e := p.State.Entries[0]
			if e.State != queue.StateParked || e.Reason != "area_busy" || e.BlockingPR != 9 {
				t.Fatalf("missing area hold: %+v", e)
			}
		})
	}
}

func TestQueueAreaStarvationAndReset(t *testing.T) {
	in := areaInput()
	in.Issues = in.Issues[:1]
	for pass := 1; pass <= 4; pass++ {
		p := planRepoQueue(in)
		if p.Picks[1] != (pass == 4) {
			t.Fatalf("pass %d picks %v", pass, p.Picks)
		}
		in.State = p.State
	}
	in.State.Entries[0].BlockingPR, in.State.Entries[0].AreaPasses, in.State.Entries[0].Reason = 10, 3, "area_busy"
	p := planRepoQueue(in)
	if p.Picks[1] || p.State.Entries[0].AreaPasses != 1 {
		t.Fatal("a different PR must reset the streak")
	}
	// Independent work prevents the starvation override, even after three holds.
	in = areaInput()
	in.State = p.State
	in.State.Entries[0].AreaPasses = 3
	p = planRepoQueue(in)
	if p.Picks[1] || !p.Picks[2] {
		t.Fatal("independent work must remain preferred")
	}
}

func TestQueueAreaMissingHintsAndProviderError(t *testing.T) {
	in := areaInput()
	in.PRFiles = func(int) ([]string, error) { return nil, errors.New("unreadable") }
	if p := planRepoQueue(in); p.Err == "" {
		t.Fatal("incomplete PR evidence must fail closed")
	}
	for i := range in.Issues {
		in.Issues[i].Body = "No file estimates"
	}
	if p := planRepoQueue(in); p.Err != "" || !p.Picks[1] {
		t.Fatalf("no hints should skip the check: %+v", p)
	}
}

func TestQueueAreaOnlyManagedSameRepoAndMigrationNamespace(t *testing.T) {
	for _, tc := range []struct {
		branch, repo string
		parked       bool
	}{
		{"fix/issue-8", "o/r", true},
		{"feature/custom", "o/r", false},
		{"fix/issue-8", "fork/r", false},
	} {
		in := areaInput()
		in.Issues[0].Body = "Add `internal/store/migrations/005.sql`"
		in.OpenPRs = func() ([]vcs.PR, error) {
			return []vcs.PR{{Number: 9, State: "open", HeadBranch: tc.branch, HeadRepo: tc.repo, Body: "Fixes #8"}}, nil
		}
		in.PRFiles = func(int) ([]string, error) { return []string{"internal/store/migrations.go"}, nil }
		if p := planRepoQueue(in); p.Picks[1] == tc.parked {
			t.Fatalf("branch=%s repo=%s plan=%+v", tc.branch, tc.repo, p)
		}
	}
}

func TestQueueAreaDoesNotBypassDependenciesOrCapacity(t *testing.T) {
	in := areaInput()
	in.Issues = in.Issues[:1]
	in.State = planRepoQueue(in).State
	in.State.Entries[0].AreaPasses = 3
	in.Issues[0].Body += "\nDepends on: other/r#42"
	if p := planRepoQueue(in); p.Picks[1] || p.State.Entries[0].Reason != "dependency" {
		t.Fatalf("dependency bypassed: %+v", p)
	}
	in.Issues[0].Body = "Edit `internal/a.go`"
	in.Issues = append(in.Issues, plannerIssue(3, "2026-01-03"))
	in.Locked = func(n int) bool { return n == 3 }
	if p := planRepoQueue(in); p.Picks[1] || p.State.Entries[0].Reason != "max_in_flight" {
		t.Fatalf("capacity bypassed: %+v", p)
	}
}

func TestQueueAreaParkVisibleOnBoard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := planRepoQueue(areaInput())
	f := newPipelineFake()
	d := pipelineTestDeps(f, map[string]config.Repo{"o/r": {Enabled: true}})
	d.globalQueue = func() []queue.ListEntry { return queue.BuildList([]queue.RepoState{p.State}, nil, "") }
	snap := buildPipeline(context.Background(), d)
	if len(snap.GlobalQueue) == 0 || snap.GlobalQueue[0].State != "parked:area_busy:PR #9" {
		t.Fatalf("board queue: %+v", snap.GlobalQueue)
	}
	item := itemFor(t, snap.Repos[0], 1)
	if !strings.Contains(item.Why, "area_busy:PR #9") {
		t.Fatalf("board issue: %+v", item)
	}
}
