package operator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// stubClaude puts a fake `claude` executable at the front of PATH that prints
// stdoutLine to stdout, stderrText to stderr, and exits non-zero — mimicking
// how the real CLI reports an API refusal.
func stubClaude(t *testing.T, stdoutLine, stderrText string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub is POSIX-only")
	}
	// Isolate credential loading so the test always sees the single default
	// (OAuth) provider rather than the developer's real config.
	t.Setenv("HOME", t.TempDir())

	bin := t.TempDir()
	// Quoted heredoc delimiters keep "$500" from being shell-expanded.
	script := "#!/bin/sh\ncat <<'STDOUT_EOF'\n" + stdoutLine +
		"\nSTDOUT_EOF\ncat <<'STDERR_EOF' >&2\n" + stderrText +
		"\nSTDERR_EOF\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write claude stub: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestRunClaudeCostLimitSentinel is the end-to-end regression guard for issue
// #308. A 402 billing cap must reach the caller as ErrCostLimit so run.go
// records status="cost-limit" (kept out of the circuit breaker) and aborts the
// pass. Before the fix nothing matched the Chinese 402 text, so RunClaude
// returned a bare error, the run was recorded as "failed", and three capped
// passes were enough to mislabel a healthy issue agent-failed.
func TestRunClaudeCostLimitSentinel(t *testing.T) {
	// Verbatim terminal result event and stderr line from the incident:
	// ~/.clawflow/data/runs/zhoushoujianwork__clawflow/issue-306/2026-09-11T09-22-34Z
	const resultEvent = `{"type":"result","subtype":"success","is_error":true,"api_error_status":402,"num_turns":1,"total_cost_usd":0,"result":"API Error: 402 已达到每日费用限制 ($500)"}`
	const stderrLine = `API Error: 402 已达到每日费用限制 ($500)`

	stubClaude(t, resultEvent, stderrLine)

	_, err := RunClaude(context.Background(), "prompt", t.TempDir(), 30*time.Second, nil, "operator")
	if err == nil {
		t.Fatal("expected an error when every provider is cost-capped")
	}
	if !errors.Is(err, ErrCostLimit) {
		t.Errorf("want ErrCostLimit, got %v", err)
	}
	// The two must stay distinct: a rate limit clears within minutes while a
	// billing cap holds until the provider's window resets, and both the
	// dashboard and the operator's next action hinge on telling them apart.
	if errors.Is(err, ErrRateLimit) {
		t.Errorf("cost limit must not also satisfy ErrRateLimit: %v", err)
	}
	if errors.Is(err, ErrAuthError) || errors.Is(err, ErrOutputLimit) {
		t.Errorf("cost limit must not satisfy auth/output-limit sentinels: %v", err)
	}
}

// TestRunClaudeRateLimitStillSentinel guards the pre-existing behaviour the
// new 402 branch sits in front of: an ordinary rate limit must keep reporting
// ErrRateLimit and must not be reclassified as a cost limit.
func TestRunClaudeRateLimitStillSentinel(t *testing.T) {
	const resultEvent = `{"type":"result","subtype":"success","is_error":true,"result":"You've hit your limit"}`
	stubClaude(t, resultEvent, "You've hit your limit · resets 3:20am")

	_, err := RunClaude(context.Background(), "prompt", t.TempDir(), 30*time.Second, nil, "operator")
	if err == nil {
		t.Fatal("expected an error when the provider is rate limited")
	}
	if !errors.Is(err, ErrRateLimit) {
		t.Errorf("want ErrRateLimit, got %v", err)
	}
	if errors.Is(err, ErrCostLimit) {
		t.Errorf("rate limit must not be reclassified as a cost limit: %v", err)
	}
}
