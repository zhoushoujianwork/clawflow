package runverdict

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/operator"
)

func TestDecide(t *testing.T) {
	wb := errors.New("label write failed")
	tests := []struct {
		name                          string
		in                            Input
		status, errText, label, wbErr string
		reason                        string
	}{
		{"success", Input{Output: "ok", Outcome: "agent-implemented"}, "success", "", "agent-implemented", "", ""},
		{"failure", Input{RunErr: errors.New("boom"), Outcome: "agent-failed"}, "failed", "boom", "agent-failed", "", ""},
		{"skipped empty output", Input{Output: ""}, "skipped-empty", "", "", "", ""},
		{"no marker", Input{RunErr: fmt.Errorf("x: %w", operator.ErrNoOutcomeMarker), Output: "b"}, "no-marker", "x: " + operator.ErrNoOutcomeMarker.Error(), "", "", ""},
		{"disallowed outcome", Input{RunErr: operator.ErrDisallowedOutcome, Outcome: "bogus"}, "disallowed-outcome", operator.ErrDisallowedOutcome.Error(), "", "", ""},
		{"writeback failure", Input{Output: "ok", Outcome: "agent-implemented", WritebackErr: wb}, "success", "", "", wb.Error(), ""},
		{"rate limit", Input{RunErr: operator.ErrRateLimit}, "rate-limited", operator.ErrRateLimit.Error(), "", "", ""},
		{"auth", Input{RunErr: operator.ErrAuthError}, "auth-error", operator.ErrAuthError.Error(), "", "", ""},
		{"output limit", Input{RunErr: operator.ErrOutputLimit}, "output-limit", operator.ErrOutputLimit.Error(), "", "", ""},
		{"cost limit", Input{RunErr: operator.ErrCostLimit}, "cost-limit", operator.ErrCostLimit.Error(), "", "", ""},
		{"lease lost wins", Input{CtxErr: errors.New("canceled"), RunErr: errors.New("x")}, "lease-lost", "canceled", "", "", ""},
		{"resource busy", Input{ResourceBusy: true, RunErr: errors.New("busy")}, "blocked", "busy", "", "", ""},
		{"no-change", Input{Output: "same", Outcome: "no-change"}, "success", "", "", "", "same"},
		{"repair agent-failed", Input{IsRepair: true, RepairLabel: "CI", Output: "o", Outcome: "agent-failed"}, "failed", "CI repair could not update existing PR: o", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := Decide(tc.in)
			if v.Status != tc.status || v.Error != tc.errText || v.AppliedLabel != tc.label ||
				v.WritebackError != tc.wbErr || v.OutcomeReason != tc.reason {
				t.Fatalf("got %+v", v)
			}
		})
	}
}

func TestDecideUnmanagedBranch(t *testing.T) {
	v := Decide(Input{Output: "ok", Outcome: "agent-implemented", UnmanagedBranch: "fix/issue-639-job-logs", IssueNumber: 639})
	if v.Status != "blocked" || !strings.Contains(v.Error, "fix/issue-639-job-logs") || !strings.Contains(v.Error, "fix/issue-639") {
		t.Fatalf("got %+v", v)
	}
	if v := Decide(Input{Output: "ok", Outcome: "agent-implemented", IssueNumber: 639}); v.Status != "success" {
		t.Fatalf("managed branch changed: %+v", v)
	}
}
