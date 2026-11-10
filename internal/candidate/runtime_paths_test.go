package candidate

import (
	"fmt"
	"reflect"
	"testing"
)

// Changes that cannot alter controller, worker, scheduling or storage behaviour
// never trigger a run, so they do not slow acceptance down.
func TestRuntimePathIgnoresDocsSkillsFrontEndAndTests(t *testing.T) {
	for _, p := range []string{
		"README.md", "readme.md", "docs/adr/0001-x.md", "docs/candidate-isolation.md",
		"skills/implement/SKILL.md", "skills/repair-ci/extra.md", "agent-skills/x/SKILL.md",
		"web/src/App.tsx", "web/src/styles/theme.css", "web/package.json", "web-retired/index.html",
		"macos/Sources/App/View.swift", "macos/build-app.sh", ".github/workflows/ci.yml",
		"internal/coordinator/store_test.go", "internal/candidate/testdata/fakecf/main.go",
		"cmd/clawflow/commands/worker_test.go", "cmd/clawflow/commands/issue.go", "cmd/clawflow/commands/pilot.go",
		"cmd/clawflow/commands/web.go", "cmd/clawflow/commands/chat.go", "cmd/clawflow/commands/sub/run.go",
		"Makefile", "install.sh", ".gitignore", "", ".",
	} {
		if RuntimePath(p) {
			t.Errorf("%q must not trigger an isolated run", p)
		}
	}
}

func TestRuntimePathCoversControllerWorkerSchedulingAndStorage(t *testing.T) {
	for _, p := range []string{
		"internal/coordinator/store.go", "internal/store/store.go", "internal/store/migrations.go",
		"internal/queue/queue.go", "internal/execution/library.go", "internal/filelock/filelock.go",
		"internal/snapshot/lock.go", "internal/acceptance/runner.go", "internal/config/config.go",
		"cmd/clawflow/commands/controller.go", "cmd/clawflow/commands/coordinator_jobs.go",
		"cmd/clawflow/commands/worker_run.go", "cmd/clawflow/commands/run.go", "cmd/clawflow/commands/harvest.go",
		"cmd/clawflow/commands/db_migrate.go", "cmd/clawflow/main.go", "go.mod", "go.sum",
		"./internal/store/store.go", "internal\\store\\store.go", "  internal/store/store.go  ",
	} {
		if !RuntimePath(p) {
			t.Errorf("%q must trigger an isolated run", p)
		}
	}
}

func TestRuntimeChangeIsSortedDeduplicatedAndBounded(t *testing.T) {
	if got := RuntimeChange([]string{"docs/a.md", "web/x.tsx"}); len(got) != 0 {
		t.Fatalf("docs and front-end changes triggered: %v", got)
	}
	got := RuntimeChange([]string{"internal/store/b.go", "docs/a.md", "internal/queue/a.go", "internal/store/b.go"})
	want := []string{"internal/queue/a.go", "internal/store/b.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	var many []string
	for i := 0; i < 200; i++ {
		many = append(many, fmt.Sprintf("internal/store/f%03d.go", i))
	}
	if n := len(RuntimeChange(many)); n != MaxRuntimePaths {
		t.Fatalf("recorded %d triggers, want %d", n, MaxRuntimePaths)
	}
	// A runtime file hidden among many documents still triggers.
	files := make([]string, 0, 500)
	for i := 0; i < 499; i++ {
		files = append(files, fmt.Sprintf("docs/page%03d.md", i))
	}
	files = append(files, "internal/coordinator/store.go")
	if len(RuntimeChange(files)) != 1 {
		t.Fatal("a runtime file among many documents did not trigger")
	}
}
