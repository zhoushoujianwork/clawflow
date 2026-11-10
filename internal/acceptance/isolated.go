package acceptance

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Isolated-run statuses. There is no "blocked": a run that could not be
// attempted for environmental reasons is an infrastructure error of the review
// attempt, handled by the existing retry policy, not a verdict about the code.
const (
	IsolatedPassed = "passed"
	IsolatedFailed = "failed"
)

// IsolatedStep is one observed stage of the real task.
type IsolatedStep struct {
	Name     string `json:"name"`
	Passed   bool   `json:"passed"`
	Evidence string `json:"evidence"`
}

// IsolatedRun is the runner-observed result of running one real task through an
// isolated candidate build of the exact head. It is produced by the runner from
// process, database and forge observations, never from model output.
type IsolatedRun struct {
	Commit            string         `json:"commit"`
	RuntimeID         string         `json:"runtime_id"`
	Status            string         `json:"status"`
	Triggers          []string       `json:"triggers"`
	Steps             []IsolatedStep `json:"steps"`
	ResidualProcesses []string       `json:"residual_processes"`
	ResidualLocks     []string       `json:"residual_locks"`
	Failure           string         `json:"failure,omitempty"`
	Summary           string         `json:"summary"`
	// Digest is the SHA-256 of the candidate evidence file the run produced.
	Digest      string    `json:"digest"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
}

// isolatedStepNames is the required order of a complete run.
var isolatedStepNames = []string{"claimed", "executed", "written-back", "finished"}

// Validate checks that the run belongs to the contract head and that its status
// follows from its recorded facts, so a record cannot say "passed" over a failed
// step, a leftover process or a leftover lock.
func (r IsolatedRun) Validate(c Contract) error {
	switch {
	case r.Commit != c.HeadSHA:
		return errors.New("isolated run is for another commit")
	case !nonempty(r.RuntimeID, r.Summary, r.Digest):
		return errors.New("isolated run lacks identity, summary or evidence digest")
	case r.Status != IsolatedPassed && r.Status != IsolatedFailed:
		return errors.New("isolated run has an unknown status")
	case r.StartedAt.IsZero() || r.CompletedAt.Before(r.StartedAt):
		return errors.New("isolated run has incoherent timestamps")
	case len(r.Triggers) == 0 || len(r.Triggers) > 20 || len(r.Steps) > len(isolatedStepNames) || len(r.Summary) > 4000 ||
		len(r.ResidualProcesses) > 21 || len(r.ResidualLocks) > 21:
		return errors.New("isolated run is empty of triggers or exceeds its bounds")
	}
	clean := r.Failure == "" && len(r.ResidualProcesses) == 0 && len(r.ResidualLocks) == 0 && len(r.Steps) == len(isolatedStepNames)
	for i, step := range r.Steps {
		clean = clean && i < len(isolatedStepNames) && step.Name == isolatedStepNames[i] && step.Passed
	}
	if clean != (r.Status == IsolatedPassed) {
		return fmt.Errorf("isolated run status %q contradicts its recorded steps", r.Status)
	}
	return nil
}

// validateIsolated applies the rules that hold for every non-infrastructure
// record: a contract that requires an isolated run needs a coherent one, and a
// contract that does not require one cannot carry one.
func validateIsolated(c Contract, r Record) error {
	if !c.IsolatedRun {
		if r.IsolatedRun != nil {
			return errors.New("record carries an isolated run the contract did not require")
		}
		return nil
	}
	if r.IsolatedRun == nil {
		return errors.New("acceptance requires an isolated run of the exact head, and the record has none")
	}
	return r.IsolatedRun.Validate(c)
}

// isolatedEvidenceLine is the entry that makes a verdict cite the run.
func isolatedEvidenceLine(r IsolatedRun) string {
	return "isolated-run: " + strings.TrimSpace(r.Summary) + " [evidence sha256 " + r.Digest + "]"
}

// citeIsolatedRun adds the run to the evidence of a reviewer verdict. An
// infrastructure record (no evidence) stays one.
func (r *Record) citeIsolatedRun() {
	if r.IsolatedRun == nil || len(r.Evidence) == 0 {
		return
	}
	line := isolatedEvidenceLine(*r.IsolatedRun)
	for _, e := range r.Evidence {
		if e == line {
			return
		}
	}
	r.Evidence = append(r.Evidence, line)
}

// isolatedFailureFinding is the runner-authored defect that a failed run adds
// to the verdict. It carries an exact-head failed observation, as every failing
// finding must.
func isolatedFailureFinding(c Contract, r IsolatedRun) Finding {
	return Finding{
		Kind:     "defect",
		Summary:  "The exact head did not complete a real task in an isolated run",
		Defect:   "an isolated candidate build of this head failed the claim, execute, write-back and finish lifecycle or left a process or lock behind",
		Evidence: Text(r.Summary),
		Observation: &RequirementEvidence{
			Type: "integration", Check: "isolated candidate run: claim, execute, write back, finish, no residue",
			Reference: "isolated run " + r.RuntimeID + " evidence sha256 " + r.Digest, Commit: c.HeadSHA, Result: "failed",
		},
	}
}

// enforceIsolatedRun makes a failed run decide the verdict. The reviewer sees the
// run, but its judgement cannot turn a failed run into a pass, and a blocked or
// passed verdict becomes failed with a runner-authored defect finding so the
// existing repair flow receives exact-head evidence.
func enforceIsolatedRun(c Contract, record *Record) {
	run := record.IsolatedRun
	if run == nil || run.Status != IsolatedFailed {
		return
	}
	already := false
	for _, f := range record.Findings {
		already = already || (f.Kind == "defect" && f.Observation != nil && strings.HasPrefix(f.Observation.Reference, "isolated run "))
	}
	if !already {
		record.Findings = append(record.Findings, isolatedFailureFinding(c, *run))
	}
	record.Verdict = Failed
	record.NextOwner = "implementer"
	record.NextAction = "Fix the defect shown by the isolated run of this head, then run fresh acceptance"
	record.Report = strings.TrimSpace(record.Report) + " The isolated run of this head failed: " + run.Failure + "."
}

// isolatedPrompt hands the reviewer the run result as authoritative data. It is
// runner-observed, not model-authored, and still bounded text.
func isolatedPrompt(run *IsolatedRun) string {
	if run == nil {
		return ""
	}
	return " Isolated real-run evidence for the exact head (runner-observed, authoritative; status " + run.Status + "): " + run.Summary +
		". A real task was claimed, executed, written back and finished in an isolated candidate build of this head, and the run was checked for leftover processes and locks. Cite it as integration execution evidence for criteria it covers. A failed run is a demonstrated defect of this head; do not call the head passed. "
}
