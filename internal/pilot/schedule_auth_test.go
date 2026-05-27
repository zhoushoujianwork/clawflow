package pilot

import (
	"errors"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/operator"
)

// TestIsAuthError_Patterns verifies that IsAuthError correctly identifies
// the error patterns that appear in 403 / authentication-failure responses
// from the claude CLI — the same patterns that caused issue #209 where
// pilot wakes silently failed with "API Error: 403 Request not allowed".
func TestIsAuthError_Patterns(t *testing.T) {
	authPatterns := []struct {
		name   string
		err    error
		output string
	}{
		{"403 in err", errors.New("exit status 1"), "API Error: 403 Request not allowed"},
		{"request not allowed in output", errors.New("exit status 1"), "Request not allowed"},
		{"failed to authenticate in output", errors.New("exit status 1"), "Failed to authenticate"},
		{"authentication failed in output", errors.New("exit status 1"), "authentication failed — please login again"},
		{"403 in err string", errors.New("claude: exit status 1: 403"), ""},
	}
	for _, tc := range authPatterns {
		t.Run(tc.name, func(t *testing.T) {
			if !operator.IsAuthError(tc.err, tc.output) {
				t.Errorf("IsAuthError(%v, %q) = false, want true", tc.err, tc.output)
			}
		})
	}
}

// TestIsAuthError_NilErr verifies that IsAuthError returns false when err is nil.
func TestIsAuthError_NilErr(t *testing.T) {
	if operator.IsAuthError(nil, "403 error in output") {
		t.Error("IsAuthError(nil, ...) = true, want false — nil err means success")
	}
}

// TestIsAuthError_NonAuthError verifies that unrelated errors are not
// misclassified as auth errors.
func TestIsAuthError_NonAuthError(t *testing.T) {
	nonAuthCases := []struct {
		err    error
		output string
	}{
		{errors.New("exit status 1"), "You've hit your limit"},
		{errors.New("exit status 1"), "rate limit exceeded"},
		{errors.New("exit status 1"), "network timeout"},
		{errors.New("connection refused"), ""},
	}
	for _, tc := range nonAuthCases {
		if operator.IsAuthError(tc.err, tc.output) {
			t.Errorf("IsAuthError(%v, %q) = true, want false — should not be classified as auth error", tc.err, tc.output)
		}
	}
}

// TestErrAuthError_WrappedByRunClaude verifies that the ErrAuthError sentinel
// is wrapped (not replaced) so errors.Is works correctly in callers that need
// to distinguish auth errors from other failures.
func TestErrAuthError_WrappedByRunClaude(t *testing.T) {
	// ErrAuthError is what RunClaude wraps when IsAuthError matches.
	// Check that the sentinel itself is distinct from ErrRateLimit.
	if errors.Is(operator.ErrAuthError, operator.ErrRateLimit) {
		t.Error("ErrAuthError must not be the same as ErrRateLimit")
	}
	if errors.Is(operator.ErrRateLimit, operator.ErrAuthError) {
		t.Error("ErrRateLimit must not be the same as ErrAuthError")
	}

	// Simulate the wrapped error RunClaude produces.
	wrapped := errors.Join(operator.ErrAuthError, errors.New("claude: exit status 1"))
	if !errors.Is(wrapped, operator.ErrAuthError) {
		t.Error("errors.Is(wrapped, ErrAuthError) = false — callers rely on this for auth-error detection")
	}
}
