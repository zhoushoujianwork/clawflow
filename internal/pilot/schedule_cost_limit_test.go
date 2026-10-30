package pilot

import (
	"errors"
	"fmt"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"testing"
)

// TestClassifyWakeStatus_CostLimit is the core regression guard for issue #320:
// a 402 billing cap reaching pilot must be recorded as "cost-limit", not as a
// generic "failed". 18 wakes in one day were misfiled as "failed" — which
// counted them toward the consecutive-failure alert ("check credentials" on
// perfectly good credentials) and, through the pre-wake MarkWoken stamp, burned
// a full cooldown per 4-second failure.
func TestClassifyWakeStatus_CostLimit(t *testing.T) {
	// The sentinel path is what production actually hits: RunOffload's
	// provider-exhaustion branch returns ("" , ErrCostLimit-wrapped), so the
	// 402 text is nowhere in output.
	sentinel := fmt.Errorf("%w: all 1 provider(s) failed", operator.ErrCostLimit)
	if got := classifyWakeStatus(sentinel, ""); got != "cost-limit" {
		t.Errorf("classifyWakeStatus(ErrCostLimit, empty output) = %q, want cost-limit — "+
			"the exhaustion path discards output, so only errors.Is can catch it", got)
	}

	// The text path covers the pre-#308 shape still present in historical
	// meta.json: a bare "claude: exit status 1" plus the 402 line in output.
	textCases := []struct {
		name   string
		output string
	}{
		{"chinese 402 from proxy", "API Error: 402 已达到每日费用限制 ($500)"},
		{"chinese 402 higher cap", "API Error: 402 已达到每日费用限制 ($700)"},
		{"english 402", "API Error: 402 Payment Required"},
	}
	for _, tc := range textCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyWakeStatus(errors.New("claude: exit status 1"), tc.output); got != "failed" {
				t.Errorf("classifyWakeStatus(exit status 1, %q) = %q, want failed", tc.output, got)
			}
		})
	}
}

// TestClassifyWakeStatus_OtherStatuses verifies cost-limit detection did not
// swallow the statuses that were already working: auth errors keep their own
// bucket (issue #204) and everything else stays "failed".
func TestClassifyWakeStatus_OtherStatuses(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		output string
		want   string
	}{
		{"nil err", nil, "", ""},
		{"auth protocol", operator.ErrAuthError, "API Error: 403 Request not allowed", "auth-error"},
		{"timeout", errors.New("signal: killed"), "", "failed"},
		{"generic", errors.New("claude: exit status 1"), "", "failed"},
		{"rate limit is not a cost limit", errors.New("claude: exit status 1"), "You've hit your limit", "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyWakeStatus(tc.err, tc.output); got != tc.want {
				t.Errorf("classifyWakeStatus(%v, %q) = %q, want %q", tc.err, tc.output, got, tc.want)
			}
		})
	}
}

// TestClassifyWakeStatus_BudgetCapped guards the wake-side half of the
// --max-budget-usd fix: a session that hit its own budget ceiling must be
// recorded as "budget-capped", distinct from both "cost-limit" (an
// account-level provider refusal) and "failed" (a genuine error) — it must
// never count toward the consecutive-failure alert, since the run did real,
// paid-for work and stopped exactly as configured.
func TestClassifyWakeStatus_BudgetCapped(t *testing.T) {
	sentinel := fmt.Errorf("%w: Reached maximum budget ($15.00)", operator.ErrBudgetExceeded)
	if got := classifyWakeStatus(sentinel, "PILOT-RESULT: partial work done"); got != "budget-capped" {
		t.Errorf("classifyWakeStatus(ErrBudgetExceeded, ...) = %q, want budget-capped", got)
	}
	if got := classifyWakeStatus(sentinel, ""); got == "cost-limit" {
		t.Error("ErrBudgetExceeded must not be classified as cost-limit — they have different causes and different dashboard semantics")
	}
}
