// Package runverdict decides the recorded status of a finished operator run.
// It is pure: no VCS, filesystem or cmd dependencies.
package runverdict

import (
	"errors"

	"github.com/zhoushoujianwork/clawflow/internal/operator"
)

// Input describes a finished run.
type Input struct {
	CtxErr       error  // non-nil when the run context ended (lease lost)
	RunErr       error  // error returned by the operator run
	ResourceBusy bool   // RunErr is a resource-lease block
	Output       string // operator output body
	Outcome      string // outcome label requested by the operator
	IsRepair     bool   // the job is a CI/acceptance repair
	RepairLabel  string // human label of the repair kind
	WritebackErr error  // error from comment/label write-back
}

// Verdict is the decision derived from Input.
type Verdict struct {
	Status         string // run status ("success", "failed", ...)
	Error          string // error text recorded with the status
	AppliedLabel   string // label that actually landed; empty when none
	OutcomeReason  string // set for the "no-change" outcome
	WritebackError string // write-back failure text, if any
}

// Decide maps a finished run to its status, applied label and write-back result.
func Decide(in Input) Verdict {
	v := Verdict{}
	errText := ""
	if in.RunErr != nil {
		errText = in.RunErr.Error()
	}
	switch {
	case in.CtxErr != nil:
		v.Status, v.Error = "lease-lost", in.CtxErr.Error()
	case in.ResourceBusy:
		v.Status, v.Error = "blocked", errText
	case is(in.RunErr, operator.ErrCostLimit):
		v.Status, v.Error = "cost-limit", errText
	case is(in.RunErr, operator.ErrRateLimit):
		v.Status, v.Error = "rate-limited", errText
	case is(in.RunErr, operator.ErrAuthError):
		v.Status, v.Error = "auth-error", errText
	case is(in.RunErr, operator.ErrOutputLimit):
		v.Status, v.Error = "output-limit", errText
	case is(in.RunErr, operator.ErrNoOutcomeMarker):
		v.Status, v.Error = "no-marker", errText
	case is(in.RunErr, operator.ErrDisallowedOutcome):
		v.Status, v.Error = "disallowed-outcome", errText
	case in.RunErr != nil:
		v.Status, v.Error = "failed", errText
	case in.Output == "":
		v.Status = "skipped-empty"
	case in.IsRepair && in.Outcome == "agent-failed":
		v.Status = "failed"
		v.Error = in.RepairLabel + " repair could not update existing PR"
	default:
		v.Status = "success"
	}
	if in.Outcome == "no-change" {
		v.OutcomeReason = in.Output
	}
	if in.WritebackErr != nil {
		v.WritebackError = in.WritebackErr.Error()
	}
	v.AppliedLabel = in.Outcome
	if is(in.RunErr, operator.ErrDisallowedOutcome) || in.WritebackErr != nil ||
		in.Outcome == "no-change" || (in.IsRepair && in.Outcome == "agent-failed") {
		v.AppliedLabel = ""
	}
	return v
}

func is(err, target error) bool { return err != nil && errors.Is(err, target) }
