package commands

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// TestDeterministicSkip pins down which (operator × repo-config) combinations
// are considered "configuration-level unrunnable" — the answer drives both
// pending-list filtering and firstMatch execution gating, and a regression
// here would resurrect the old bug where an `implement` op with no
// local_path piled up forever in the dashboard's Pending list.
func TestDeterministicSkip(t *testing.T) {
	implement := &operator.Operator{Name: "implement"}
	evalBug := &operator.Operator{Name: "evaluate-custom"}

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
			name: "evaluate-custom never deterministically skipped (no local_path needed)",
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

// TestIsTransientMergeError verifies that only known retryable base-moved
// races are classified as transient (issue #224).
func TestIsTransientMergeError(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want bool
	}{
		{
			name: "405 base branch modified",
			s:    `github merge PR: HTTP 405: {"message":"Base branch was modified. Review and try the merge again.","status":"405"}`,
			want: true,
		},
		{
			name: "message only",
			s:    "Base branch was modified",
			want: true,
		},
		{
			name: "bare 405 status",
			s:    "HTTP 405: method not allowed",
			want: true,
		},
		{
			name: "branch protection is permanent",
			s:    "github merge PR: HTTP 405: required status checks have not passed",
			want: true, // contains 405; acceptable — re-check gate stops bad retries
		},
		{
			name: "auth failure is permanent",
			s:    "github merge PR: HTTP 403: Resource not accessible by integration",
			want: false,
		},
		{
			name: "real conflict is permanent",
			s:    "Pull Request is not mergeable",
			want: false,
		},
		{
			name: "empty",
			s:    "",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientMergeError(tc.s); got != tc.want {
				t.Errorf("isTransientMergeError(%q) = %v, want %v", tc.s, got, tc.want)
			}
		})
	}
}

// TestMergeWithRetry_SuccessFirstTry verifies no retry overhead when the
// first merge attempt succeeds.
func TestMergeWithRetry_SuccessFirstTry(t *testing.T) {
	merges, rechecks, sleeps := 0, 0, 0
	err := mergeWithRetry("test",
		func() error { merges++; return nil },
		func() (vcs.MergeStatus, error) { rechecks++; return vcs.MergeStatusClean, nil },
		func(time.Duration) { sleeps++ },
	)
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if merges != 1 || rechecks != 0 || sleeps != 0 {
		t.Errorf("merges=%d rechecks=%d sleeps=%d, want 1/0/0", merges, rechecks, sleeps)
	}
}

// TestMergeWithRetry_TransientThenSuccess verifies a 405 race is retried
// after a clean re-check and then succeeds.
func TestMergeWithRetry_TransientThenSuccess(t *testing.T) {
	merges := 0
	err := mergeWithRetry("test",
		func() error {
			merges++
			if merges == 1 {
				return errors.New(`HTTP 405: {"message":"Base branch was modified.","status":"405"}`)
			}
			return nil
		},
		func() (vcs.MergeStatus, error) { return vcs.MergeStatusClean, nil },
		func(time.Duration) {},
	)
	if err != nil {
		t.Fatalf("expected nil after retry, got %v", err)
	}
	if merges != 2 {
		t.Errorf("expected 2 merge attempts, got %d", merges)
	}
}

// TestMergeWithRetry_PermanentNoRetry verifies a non-transient failure is
// returned immediately without re-checking or retrying.
func TestMergeWithRetry_PermanentNoRetry(t *testing.T) {
	merges, rechecks := 0, 0
	want := errors.New("HTTP 403: Resource not accessible by integration")
	err := mergeWithRetry("test",
		func() error { merges++; return want },
		func() (vcs.MergeStatus, error) { rechecks++; return vcs.MergeStatusClean, nil },
		func(time.Duration) {},
	)
	if err == nil {
		t.Fatal("expected error for permanent failure")
	}
	if merges != 1 || rechecks != 0 {
		t.Errorf("merges=%d rechecks=%d, want 1/0 (no retry on permanent error)", merges, rechecks)
	}
}

// TestMergeWithRetry_BaseWentDirty verifies that if a real conflict appears
// during the transient window, the retry loop stops and reports it instead
// of merging a no-longer-clean base.
func TestMergeWithRetry_BaseWentDirty(t *testing.T) {
	merges := 0
	err := mergeWithRetry("test",
		func() error {
			merges++
			return errors.New("HTTP 405: Base branch was modified")
		},
		func() (vcs.MergeStatus, error) { return vcs.MergeStatusConflict, nil },
		func(time.Duration) {},
	)
	if err == nil {
		t.Fatal("expected error when base went non-clean")
	}
	if merges != 1 {
		t.Errorf("expected exactly 1 merge attempt (re-check blocked retry), got %d", merges)
	}
}

// TestMergeWithRetry_ExhaustsRetries verifies the loop caps at the initial
// attempt plus mergeRetryAttempts when the race never clears.
func TestMergeWithRetry_ExhaustsRetries(t *testing.T) {
	merges := 0
	err := mergeWithRetry("test",
		func() error {
			merges++
			return errors.New("HTTP 405: Base branch was modified")
		},
		func() (vcs.MergeStatus, error) { return vcs.MergeStatusClean, nil },
		func(time.Duration) {},
	)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if want := 1 + mergeRetryAttempts; merges != want {
		t.Errorf("expected %d merge attempts, got %d", want, merges)
	}
}

// TestExtractOwnerRepo verifies that extractOwnerRepo parses various git
// remote URL formats into "owner/repo", handling .git suffixes, SSH, HTTPS,
// and self-hosted GitLab instances (issue #211).
func TestExtractOwnerRepo(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "HTTPS GitHub with .git",
			url:  "https://github.com/owner/repo.git",
			want: "owner/repo",
		},
		{
			name: "HTTPS GitHub without .git",
			url:  "https://github.com/owner/repo",
			want: "owner/repo",
		},
		{
			name: "SSH GitHub",
			url:  "git@github.com:owner/repo.git",
			want: "owner/repo",
		},
		{
			name: "SSH without .git suffix",
			url:  "git@github.com:owner/repo",
			want: "owner/repo",
		},
		{
			name: "self-hosted GitLab HTTPS",
			url:  "https://gitlab.company.com/owner/repo.git",
			want: "owner/repo",
		},
		{
			name: "self-hosted GitLab SSH",
			url:  "git@gitlab.company.com:owner/repo.git",
			want: "owner/repo",
		},
		{
			name: "HTTPS with nested group (GitLab subgroup, returns first two segments)",
			url:  "https://gitlab.com/owner/repo/subgroup.git",
			want: "owner/repo",
		},
		{
			name: "empty string",
			url:  "",
			want: "",
		},
		{
			name: "unrecognised format",
			url:  "not-a-url",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractOwnerRepo(tc.url)
			if got != tc.want {
				t.Errorf("extractOwnerRepo(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

// TestPruneOrphanedAnalysisWorktrees verifies that pruneOrphanedAnalysisWorktrees
// removes analysis-* directories only for slugs not present in the active
// config, leaving active-repo worktrees untouched (issue #211).
func TestPruneOrphanedAnalysisWorktrees(t *testing.T) {
	// Create a temporary worktrees root to isolate the test from real state.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp) // pruneOrphanedAnalysisWorktrees uses os.UserHomeDir

	wtRoot := tmp + "/.clawflow/worktrees"

	// active repo: owner__active  →  analysis-main should survive
	activeSlugDir := wtRoot + "/owner__active"
	_ = os.MkdirAll(activeSlugDir+"/analysis-main", 0o755)

	// orphaned repo: owner__gone  →  analysis-main should be removed
	orphanSlugDir := wtRoot + "/owner__gone"
	_ = os.MkdirAll(orphanSlugDir+"/analysis-main", 0o755)

	// non-analysis dir inside orphaned slug: must NOT be touched
	_ = os.MkdirAll(orphanSlugDir+"/issue-42-2025-01-01T00-00-00Z", 0o755)

	cfg := &config.Config{
		Repos: map[string]config.Repo{
			"owner/active": {Enabled: true, LocalPath: "/tmp/active"},
		},
	}

	n := pruneOrphanedAnalysisWorktrees(cfg)
	if n != 1 {
		t.Errorf("expected 1 worktree pruned, got %d", n)
	}

	// Active repo worktree must still exist.
	if _, err := os.Stat(activeSlugDir + "/analysis-main"); err != nil {
		t.Errorf("active analysis worktree was incorrectly removed: %v", err)
	}

	// Orphaned analysis dir must be gone.
	if _, err := os.Stat(orphanSlugDir + "/analysis-main"); !os.IsNotExist(err) {
		t.Errorf("orphaned analysis worktree was not removed (stat err: %v)", err)
	}

	// Non-analysis issue dir must be untouched.
	if _, err := os.Stat(orphanSlugDir + "/issue-42-2025-01-01T00-00-00Z"); err != nil {
		t.Errorf("non-analysis dir was unexpectedly removed: %v", err)
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

// stubRunOneOperator swaps the per-job executor for the duration of a test and
// returns a counter of how many jobs were actually dispatched.
func stubRunOneOperator(t *testing.T, fn func(j *runJob) (bool, bool, bool)) *int32 {
	t.Helper()
	var calls int32
	old := runOneOperatorFn
	runOneOperatorFn = func(_ context.Context, j *runJob, _ time.Duration) (bool, bool, bool) {
		atomic.AddInt32(&calls, 1)
		return fn(j)
	}
	t.Cleanup(func() { runOneOperatorFn = old })
	return &calls
}

func testJob(repo string, issue int, op string) *runJob {
	return &runJob{
		op:   &operator.Operator{Name: op},
		sub:  &operator.Subject{Number: issue},
		repo: repo,
	}
}

// TestRunJobsParallel_AggregatesFailures verifies that failed jobs are
// reported separately from fired ones and that a job can be neither (skip).
func TestRunJobsParallel_AggregatesFailures(t *testing.T) {
	stubRunOneOperator(t, func(j *runJob) (bool, bool, bool) {
		switch j.sub.Number {
		case 1:
			return true, false, false // success
		case 2:
			return false, false, true // terminal failure
		default:
			return false, false, false // non-terminal skip
		}
	})
	jobs := []*runJob{testJob("o/r", 1, "implement"), testJob("o/r", 2, "implement"), testJob("o/r", 3, "implement")}

	fired, failed := runJobsParallel(context.Background(), jobs, 3, time.Second)

	if len(fired) != 1 || fired[0].IssueNumber != 1 {
		t.Errorf("fired = %v, want only issue 1", fired)
	}
	if len(failed) != 1 || failed[0] != (firedKey{Repo: "o/r", IssueNumber: 2, Operator: "implement"}) {
		t.Errorf("failed = %v, want only o/r#2 implement", failed)
	}
}

// TestRunJobsParallel_ProviderLimitSkipsNotFailed verifies that only the job
// that hit the account-level limit counts as failed; jobs skipped afterwards
// by the circuit breaker are neither fired nor failed.
func TestRunJobsParallel_ProviderLimitSkipsNotFailed(t *testing.T) {
	withRunLog(t) // the skip path writes to runLog
	calls := stubRunOneOperator(t, func(j *runJob) (bool, bool, bool) {
		return false, true, true // rate-limited / cost-limit
	})
	jobs := []*runJob{testJob("o/r", 1, "implement"), testJob("o/r", 2, "implement"), testJob("o/r", 3, "implement")}

	// One worker keeps dispatch order deterministic.
	fired, failed := runJobsParallel(context.Background(), jobs, 1, time.Second)

	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("dispatched %d jobs, want 1 (rest skipped by circuit breaker)", got)
	}
	if len(fired) != 0 {
		t.Errorf("fired = %v, want none", fired)
	}
	if len(failed) != 1 || failed[0].IssueNumber != 1 {
		t.Errorf("failed = %v, want only the job that hit the limit (issue 1)", failed)
	}
}

func TestRunJobsParallel_NoJobs(t *testing.T) {
	fired, failed := runJobsParallel(context.Background(), nil, 4, time.Second)
	if fired != nil || failed != nil {
		t.Errorf("got (%v, %v), want (nil, nil)", fired, failed)
	}
}

// TestFailedRunsError pins the error message shape returned by runOnce (and
// therefore the RunE exit path) for a failed pass.
func TestFailedRunsError(t *testing.T) {
	if err := failedRunsError(nil); err != nil {
		t.Fatalf("no failures must yield nil, got %v", err)
	}
	err := failedRunsError([]firedKey{
		{Repo: "o/r", IssueNumber: 10, Operator: "implement"},
		{Repo: "a/b", IssueNumber: 7, Operator: "track-progress"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	want := "2 operator run(s) failed: a/b#7 track-progress, o/r#10 implement"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

// commentsFake serves just enough of vcs.Client for runOneOperator to reach
// the base-branch marker check; any other method panics via the nil embed.
type commentsFake struct {
	vcs.Client
	labels []string
}

func (f *commentsFake) GetIssueLabels(string, int) ([]string, error) { return f.labels, nil }
func (f *commentsFake) ListIssueCommentsDetail(string, int) ([]vcs.IssueComment, error) {
	return nil, nil
}

// TestRunOneOperator_InvalidBaseBranchMarkerFails exercises a real terminal
// failure path (no Offload needed): an invalid ClawFlow-Base-Branch marker
// records status=failed and must report failed=true without firing.
func TestRunOneOperator_InvalidBaseBranchMarkerFails(t *testing.T) {
	withRunLog(t)
	op := &operator.Operator{
		Name:    "inspect",
		Trigger: operator.Trigger{Target: "issue", LabelsRequired: []string{"bug"}},
	}
	sub := &operator.Subject{Number: 5, Labels: []string{"bug"}, State: "open", Body: "ClawFlow-Base-Branch: ../evil"}
	job := &runJob{op: op, sub: sub, repo: "o/r", client: &commentsFake{labels: []string{"bug"}}}

	didFire, limited, failed := runOneOperator(context.Background(), job, time.Second)
	if didFire || limited || !failed {
		t.Errorf("runOneOperator() = (%v, %v, %v), want (false, false, true)", didFire, limited, failed)
	}
}
