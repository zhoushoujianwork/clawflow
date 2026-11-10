package commands

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/retry"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

const testHomeMarkerEnv = "CLAWFLOW_COMMANDS_TEST_HOME"

func TestMain(m *testing.M) {
	// Tests that hit a failing fake VCS transport would otherwise sleep through
	// the real 1s/2s/4s write-back backoff. Keep the attempt count so retry
	// behaviour is still exercised; only the waits shrink.
	retry.DefaultConfig.BaseDelay = time.Millisecond
	retry.DefaultConfig.MaxDelay = time.Millisecond
	baseGitDelay = time.Millisecond
	localWorkerPoll = 20 * time.Millisecond
	// Keep tests off the developer's real ~/.clawflow, and let sharded test
	// processes (scripts/verify.sh) run side by side without sharing state.
	// Helper subprocesses re-exec this binary and must keep the HOME their
	// parent test chose, so only the top-level process replaces it.
	home := ""
	if os.Getenv(testHomeMarkerEnv) == "" {
		var err error
		if home, err = os.MkdirTemp("", "clawflow-commands-tests-"); err != nil {
			panic(err)
		}
		if err := os.Setenv("HOME", home); err != nil {
			panic(err)
		}
		if err := os.Setenv(testHomeMarkerEnv, "1"); err != nil {
			panic(err)
		}
	}
	issueAdmissionFn = func(_ vcs.Client, _ string, _ config.Repo, issue vcs.Issue) (*issueAdmission, string) {
		return &issueAdmission{Author: issue.Author, Basis: "test", CheckedAt: time.Now().UTC()}, ""
	}
	// Recovery tests build their own worktrees; do not scan the host's real
	// process table unless a test asks for it.
	scanWorktreeProcesses = func(context.Context, string) ([]worktreeProcess, error) { return nil, nil }
	code := m.Run()
	if home != "" {
		os.RemoveAll(home)
	}
	os.Exit(code)
}
