package commands

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
)

// TestDeterministicSkip pins down which (operator × repo-config) combinations
// are considered "configuration-level unrunnable" — the answer drives both
// pending-list filtering and firstMatch execution gating, and a regression
// here would resurrect the old bug where an `implement` op with no
// local_path piled up forever in the dashboard's Pending list.
func TestDeterministicSkip(t *testing.T) {
	implement := &operator.Operator{Name: "implement"}
	evalBug := &operator.Operator{Name: "evaluate-bug"}

	cases := []struct {
		name string
		op   *operator.Operator
		repo config.Repo
		want bool
	}{
		{
			name: "implement with empty local_path is skipped",
			op:   implement,
			repo: config.Repo{LocalPath: ""},
			want: true,
		},
		{
			name: "implement with local_path runs",
			op:   implement,
			repo: config.Repo{LocalPath: "/tmp/clone"},
			want: false,
		},
		{
			name: "evaluate-bug never deterministically skipped (no local_path needed)",
			op:   evalBug,
			repo: config.Repo{LocalPath: ""},
			want: false,
		},
		{
			name: "nil operator is safely false (defensive)",
			op:   nil,
			repo: config.Repo{},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deterministicSkip(tc.op, tc.repo); got != tc.want {
				t.Errorf("deterministicSkip = %v, want %v", got, tc.want)
			}
			// Reason must be non-empty exactly when skip is true. This
			// keeps the debug log readable and prevents future contributors
			// from forgetting to add a reason when they add a new skip.
			reason := deterministicSkipReason(tc.op, tc.repo)
			if tc.want && reason == "" {
				t.Errorf("expected non-empty reason when skip=true")
			}
			if !tc.want && reason != "" {
				t.Errorf("expected empty reason when skip=false, got %q", reason)
			}
		})
	}
}

// TestIsGitLockError verifies that isGitLockError correctly classifies
// transient git lock messages vs. permanent errors.
func TestIsGitLockError(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "index.lock File exists",
			output: "fatal: Unable to create '/some/repo/.git/index.lock': File exists",
			want:   true,
		},
		{
			name:   "cannot lock ref",
			output: "error: cannot lock ref 'refs/heads/main': ref refs/heads/main is at...",
			want:   true,
		},
		{
			name:   "Another git process",
			output: "Another git process seems to be running in this repository",
			want:   true,
		},
		{
			name:   ".lock File exists generic",
			output: "error: .lock: File exists",
			want:   true,
		},
		{
			name:   "permanent error: branch not found",
			output: "error: pathspec 'origin/nonexistent' did not match any file(s) known to git",
			want:   false,
		},
		{
			name:   "permanent error: disk full",
			output: "error: write error: No space left on device",
			want:   false,
		},
		{
			name:   "empty output",
			output: "",
			want:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isGitLockError(tc.output)
			if got != tc.want {
				t.Errorf("isGitLockError(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}

// TestRunGitWithRetry_Success verifies that a command that succeeds on the
// first attempt returns nil immediately without any retry overhead.
func TestRunGitWithRetry_Success(t *testing.T) {
	calls := 0
	err := runGitWithRetry(func() *exec.Cmd {
		calls++
		return exec.Command("true")
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call, got %d", calls)
	}
}

// TestRunGitWithRetry_PermanentError verifies that a non-lock error causes
// an immediate failure with no retries.
func TestRunGitWithRetry_PermanentError(t *testing.T) {
	calls := 0
	err := runGitWithRetry(func() *exec.Cmd {
		calls++
		// exit 1 with output that is NOT a lock error.
		return exec.Command("sh", "-c", "echo 'fatal: bad object'; exit 1")
	})
	if err == nil {
		t.Fatal("expected non-nil error for permanent failure")
	}
	if calls != 1 {
		t.Errorf("expected exactly 1 call for permanent error, got %d (should not retry)", calls)
	}
}

// TestRunGitWithRetry_LockErrorSucceedsOnRetry verifies that a lock-error
// failure on the first attempt is retried and succeeds on the second attempt.
func TestRunGitWithRetry_LockErrorSucceedsOnRetry(t *testing.T) {
	calls := 0
	err := runGitWithRetry(func() *exec.Cmd {
		calls++
		if calls == 1 {
			// Simulate a git lock error on the first attempt.
			return exec.Command("sh", "-c",
				"echo 'Another git process seems to be running in this repository'; exit 1")
		}
		// Succeed on subsequent attempts.
		return exec.Command("true")
	})
	if err != nil {
		t.Fatalf("expected nil after retry, got %v", err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (1 lock failure + 1 success), got %d", calls)
	}
}

// TestRunGitWithRetry_ExhaustsRetries verifies that a command that always
// fails with a lock error is retried exactly maxGitRetries times and then
// returns an error that wraps the original with a retry-count annotation.
func TestRunGitWithRetry_ExhaustsRetries(t *testing.T) {
	calls := 0
	err := runGitWithRetry(func() *exec.Cmd {
		calls++
		return exec.Command("sh", "-c",
			"echo 'fatal: Unable to create .git/index.lock: File exists'; exit 1")
	})
	if err == nil {
		t.Fatal("expected non-nil error after exhausting retries")
	}
	// Should have been called maxGitRetries+1 times (initial + 5 retries).
	const maxGitRetries = 5
	if calls != maxGitRetries+1 {
		t.Errorf("expected %d calls (1 initial + %d retries), got %d",
			maxGitRetries+1, maxGitRetries, calls)
	}
	// Error message should mention the retry count.
	if !strings.Contains(err.Error(), "retried") {
		t.Errorf("error should mention retry count, got: %v", err)
	}
}
