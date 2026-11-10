package candidate

import (
	"path"
	"sort"
	"strings"
)

// runtimeDirs are source directories whose non-test Go files execute inside the
// controller, a worker, the scheduler, the storage layer or the write-back path.
// A change below any of them can only be fully observed by running the program,
// so acceptance of such a change needs an isolated real run (see RunTask).
var runtimeDirs = []string{
	"internal/acceptance/", "internal/actions/", "internal/agentcapacity/", "internal/branch/",
	"internal/candidate/", "internal/clone/", "internal/config/", "internal/coordination/",
	"internal/coordinator/", "internal/cron/", "internal/devmode/", "internal/execution/",
	"internal/filelock/", "internal/gitexec/", "internal/gitsync/", "internal/housekeeping/",
	"internal/joblog/", "internal/operator/", "internal/pipeline/",
	"internal/preflight/", "internal/project/", "internal/queue/", "internal/repairledger/",
	"internal/resourcelease/", "internal/retry/", "internal/snapshot/",
	"internal/store/", "internal/upgrade/",
}

// runtimeCommandPrefixes name the files of cmd/clawflow/commands that implement
// the controller, workers, scheduling, harvest, write-back and storage entry
// points. Presentation-only commands (web, dashboard, chat, sync, pilot, install
// helpers) are deliberately absent.
var runtimeCommandPrefixes = []string{
	"acceptance", "automerge", "basebranch", "branch", "ci", "controller", "coordinator", "db",
	"dispatch", "effect_spool", "execution", "git", "harvest", "housekeeping", "issue_admission",
	"job_hooks", "pipeline", "pr_actions", "pr_check", "pr_feedback", "preflight", "queue", "reopen",
	"reset", "respawn", "run", "selfupgrade", "session_resume", "upgrade", "vcs_client", "worker",
	"worktree", "writeback",
}

const commandsDir = "cmd/clawflow/commands/"

// RuntimePath reports whether one changed repository path touches a runtime
// path. Tests, documentation, operator skill text, web and native front ends
// never do, even when they live below a runtime directory.
func RuntimePath(p string) bool {
	p = path.Clean(strings.TrimSpace(strings.ReplaceAll(p, "\\", "/")))
	switch {
	case p == "go.mod" || p == "go.sum":
		return true
	case !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go"):
		return false
	case strings.Contains(p, "/testdata/"):
		return false
	case p == "cmd/clawflow/main.go":
		return true
	case strings.HasPrefix(p, commandsDir):
		name := strings.TrimPrefix(p, commandsDir)
		if strings.Contains(name, "/") {
			return false
		}
		for _, prefix := range runtimeCommandPrefixes {
			if strings.HasPrefix(name, prefix) {
				return true
			}
		}
		return false
	}
	for _, dir := range runtimeDirs {
		if strings.HasPrefix(p, dir) {
			return true
		}
	}
	return false
}

// MaxRuntimePaths bounds the triggering paths recorded as evidence.
const MaxRuntimePaths = 20

// RuntimeChange returns the changed paths that require an isolated run, sorted,
// deduplicated and bounded, so the same change always yields the same list. An
// empty result means the change does not trigger.
func RuntimeChange(files []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		f = path.Clean(strings.TrimSpace(strings.ReplaceAll(f, "\\", "/")))
		if RuntimePath(f) && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	sort.Strings(out)
	if len(out) > MaxRuntimePaths {
		out = out[:MaxRuntimePaths]
	}
	return out
}
