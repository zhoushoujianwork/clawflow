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

func TestMain(m *testing.M) {
	// Tests that hit a failing fake VCS transport would otherwise sleep through
	// the real 1s/2s/4s write-back backoff. Keep the attempt count so retry
	// behaviour is still exercised; only the waits shrink.
	retry.DefaultConfig.BaseDelay = time.Millisecond
	retry.DefaultConfig.MaxDelay = time.Millisecond
	issueAdmissionFn = func(_ vcs.Client, _ string, _ config.Repo, issue vcs.Issue) (*issueAdmission, string) {
		return &issueAdmission{Author: issue.Author, Basis: "test", CheckedAt: time.Now().UTC()}, ""
	}
	// Recovery tests build their own worktrees; do not scan the host's real
	// process table unless a test asks for it.
	scanWorktreeProcesses = func(context.Context, string) ([]worktreeProcess, error) { return nil, nil }
	os.Exit(m.Run())
}
