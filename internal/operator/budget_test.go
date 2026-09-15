package operator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWithMaxBudgetUSD_InjectsFlag verifies that a context annotated via
// WithMaxBudgetUSD makes runClaudeWithProvider append --max-budget-usd to the
// claude invocation, and that a zero/negative budget is a no-op (no flag).
// Uses a stub `claude` that just echoes its argv so the test doesn't depend
// on any real subprocess behaviour.
func TestWithMaxBudgetUSD_InjectsFlag(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("shell stub is POSIX-only")
	}
	t.Setenv("HOME", t.TempDir())

	bin := t.TempDir()
	argvFile := filepath.Join(bin, "argv.txt")
	script := "#!/bin/sh\necho \"$@\" > " + argvFile + "\n" +
		`cat <<'STDOUT_EOF'` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":"ok"}` + "\n" +
		`STDOUT_EOF` + "\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write claude stub: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := WithMaxBudgetUSD(context.Background(), 15.5)
	if _, err := RunClaude(ctx, "prompt", t.TempDir(), 30*time.Second, nil, "operator"); err != nil {
		t.Fatalf("RunClaude: %v", err)
	}
	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read captured argv: %v", err)
	}
	if !strings.Contains(string(argv), "--max-budget-usd 15.50") {
		t.Errorf("argv = %q, want it to contain --max-budget-usd 15.50", string(argv))
	}
}

// TestWithMaxBudgetUSD_ZeroIsNoop verifies that a non-positive budget adds no
// flag at all — RunClaude's default (unset) callers must not silently start
// passing --max-budget-usd 0, which claude itself rejects ("must be a
// positive number greater than 0").
func TestWithMaxBudgetUSD_ZeroIsNoop(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("shell stub is POSIX-only")
	}
	t.Setenv("HOME", t.TempDir())

	bin := t.TempDir()
	argvFile := filepath.Join(bin, "argv.txt")
	script := "#!/bin/sh\necho \"$@\" > " + argvFile + "\n" +
		`cat <<'STDOUT_EOF'` + "\n" +
		`{"type":"result","subtype":"success","is_error":false,"result":"ok"}` + "\n" +
		`STDOUT_EOF` + "\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write claude stub: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := WithMaxBudgetUSD(context.Background(), 0)
	if _, err := RunClaude(ctx, "prompt", t.TempDir(), 30*time.Second, nil, "operator"); err != nil {
		t.Fatalf("RunClaude: %v", err)
	}
	argv, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read captured argv: %v", err)
	}
	if strings.Contains(string(argv), "--max-budget-usd") {
		t.Errorf("argv = %q, want no --max-budget-usd flag for a zero budget", string(argv))
	}
}

// TestRunClaudeBudgetExceededSentinel is the end-to-end regression guard for
// the Pilot cost-blowout fix: when the caller opted into a budget cap and
// claude's terminal result carries subtype "error_max_budget_usd", RunClaude
// must return ErrBudgetExceeded (not a generic failure, and not fail over to
// another provider) with whatever partial output claude produced before the
// cap intact.
func TestRunClaudeBudgetExceededSentinel(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("shell stub is POSIX-only")
	}
	t.Setenv("HOME", t.TempDir())

	bin := t.TempDir()
	// Mirrors the real CLI's shape observed live: an assistant turn with text,
	// then a terminal result event with subtype error_max_budget_usd, exit 1.
	script := `#!/bin/sh
cat <<'STDOUT_EOF'
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"PILOT-RESULT: partial work done\n"}]}}
{"type":"result","subtype":"error_max_budget_usd","is_error":true,"num_turns":5,"total_cost_usd":15.5,"errors":["Reached maximum budget ($15.50)"]}
STDOUT_EOF
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write claude stub: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx := WithMaxBudgetUSD(context.Background(), 15.5)
	output, err := RunClaude(ctx, "prompt", t.TempDir(), 30*time.Second, nil, "operator")
	if err == nil {
		t.Fatal("expected an error when the session's own budget cap is hit")
	}
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("want ErrBudgetExceeded, got %v", err)
	}
	if errors.Is(err, ErrCostLimit) || errors.Is(err, ErrRateLimit) || errors.Is(err, ErrAuthError) {
		t.Errorf("budget-exceeded must not also satisfy an unrelated sentinel: %v", err)
	}
	if !strings.Contains(output, "PILOT-RESULT: partial work done") {
		t.Errorf("output = %q, want the partial assistant text preserved", output)
	}
}
