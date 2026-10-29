package acceptance

import (
	"encoding/json"
	"errors"
	"strings"
)

// Criterion is frozen before source access. Requirement quotes the approved
// contract; proof specifies the minimum observation justified by that requirement.
type Criterion struct {
	ID          string `json:"id"`
	Requirement string `json:"requirement"`
	Proof       string `json:"proof"` // source, execution, live
}

type RequirementEvidence struct {
	Type      string `json:"type"` // source, unit, integration, live
	Check     string `json:"check"`
	Reference string `json:"reference"`
	Commit    string `json:"commit"`
	Result    string `json:"result"` // inspected, exists, passed, failed, unverified
}

type RequirementCheck struct {
	ID       string                `json:"id"`
	Status   Verdict               `json:"status"`
	Evidence []RequirementEvidence `json:"evidence"`
	Missing  string                `json:"missing"`
}

func validateCriteria(c Contract, p Plan) error {
	var criteria []Criterion
	if err := decodeStrict([]byte(unfenceReport(p.Text)), &criteria); err != nil {
		return errors.New("planner must return a requirement criteria array")
	}
	a, _ := json.Marshal(criteria)
	b, _ := json.Marshal(p.Criteria)
	if string(a) != string(b) || len(criteria) == 0 {
		return errors.New("criteria differ from the saved plan")
	}
	seen := map[string]bool{}
	for _, item := range criteria {
		if !nonempty(item.ID, item.Requirement) || seen[item.ID] || !strings.Contains(c.Requirements, item.Requirement) {
			return errors.New("criterion must uniquely identify an approved requirement")
		}
		if item.Proof != "source" && item.Proof != "execution" && item.Proof != "live" {
			return errors.New("unknown criterion proof")
		}
		seen[item.ID] = true
	}
	return nil
}

// ValidateChecks binds each observation to a planned requirement and exact head.
// Existence of a test is distinct from observing its execution. CI coverage is
// reviewed explicitly; a green aggregate alone is not requirement evidence.
func ValidateChecks(c Contract, r Record) error {
	if c.EvidenceSchema != 1 {
		return nil
	}
	if err := validateCriteria(c, r.Plan); err != nil {
		return err
	}
	if !nonempty(r.NextAction, r.NextOwner) {
		return errors.New("review requires next action and owner")
	}
	criteria := map[string]Criterion{}
	for _, item := range r.Plan.Criteria {
		criteria[item.ID] = item
	}
	seen := map[string]bool{}
	failed, blocked := false, false
	for _, check := range r.Checks {
		item, ok := criteria[check.ID]
		if !ok || seen[check.ID] {
			return errors.New("unknown or duplicate requirement check")
		}
		seen[check.ID] = true
		proof, defect := false, false
		for _, e := range check.Evidence {
			if !nonempty(e.Check, e.Reference, e.Commit) {
				return errors.New("evidence requires check, reference and commit")
			}
			if e.Type != "source" && e.Type != "unit" && e.Type != "integration" && e.Type != "live" {
				return errors.New("unknown evidence type")
			}
			switch e.Result {
			case "inspected", "exists", "passed", "failed", "unverified":
			default:
				return errors.New("unknown evidence result")
			}
			if e.Commit != c.HeadSHA {
				return errors.New("evidence commit differs from submitted head")
			}
			if e.Result == "failed" {
				defect = true
			}
			if item.Proof == "source" && e.Type == "source" && e.Result == "inspected" {
				proof = true
			}
			if e.Result == "passed" && (e.Type == "unit" || e.Type == "integration" || e.Type == "live") && (item.Proof != "live" || e.Type == "live") {
				proof = true
			}
		}
		switch check.Status {
		case Passed:
			if !proof || defect || check.Missing != "" {
				return errors.New("passed requirement lacks compatible proof or contains a gap")
			}
		case Failed:
			if !defect {
				return errors.New("failed requirement requires observed defect evidence")
			}
			linked := false
			for _, f := range r.Findings {
				if f.Failing() && f.Requirement == item.Requirement {
					linked = true
				}
			}
			if !linked {
				return errors.New("failed checklist item lacks its actionable finding")
			}
			failed = true
		case Blocked:
			if !nonempty(check.Missing) {
				return errors.New("blocked requirement must identify missing proof")
			}
			blocked = true
		default:
			return errors.New("unknown requirement status")
		}
	}
	if len(seen) != len(criteria) {
		return errors.New("review omits planned requirements")
	}
	for _, f := range r.Findings {
		if !f.Failing() {
			continue
		}
		e := f.Observation
		if e == nil || !nonempty(e.Check, e.Reference) || e.Commit != c.HeadSHA || e.Result != "failed" || (e.Type != "source" && e.Type != "unit" && e.Type != "integration" && e.Type != "live") {
			return errors.New("defect finding requires exact-head structured evidence")
		}
		if f.Kind == "requirement" && !strings.Contains(c.Requirements, f.Requirement) {
			return errors.New("finding expands approved requirements")
		}
		// Requirement-linked findings must agree with every corresponding
		// checklist item, not only the overall verdict. Standalone defects
		// outside the planned requirements remain valid findings.
		if f.Kind == "requirement" || nonempty(f.Requirement) {
			linked := false
			for _, check := range r.Checks {
				if criteria[check.ID].Requirement != f.Requirement {
					continue
				}
				linked = true
				if check.Status != Failed {
					return errors.New("failing requirement finding contradicts checklist")
				}
			}
			if !linked {
				return errors.New("failing requirement finding lacks its checklist item")
			}
		}
	}
	failing, err := validateFindings(r.Findings)
	if err != nil {
		return err
	}
	switch r.Verdict {
	case Passed:
		if failed || blocked || failing {
			return errors.New("passed verdict contradicts checklist")
		}
	case Failed:
		if !failing {
			return errors.New("failed verdict requires actionable defect findings")
		}
	case Blocked:
		if !blocked || failed || failing {
			return errors.New("blocked verdict contradicts checklist")
		}
	default:
		return errors.New("unknown verdict")
	}
	return nil
}

// DeduplicateFindings consolidates repeated observations of one concern while
// retaining distinct locations. Audit records retain each completed review.
func DeduplicateFindings(in []Finding) []Finding {
	var out []Finding
	seen := map[string]int{}
	for _, f := range in {
		key := strings.Join([]string{f.Kind, strings.TrimSpace(f.Requirement), strings.TrimSpace(f.Defect)}, "\x00")
		if f.Kind == "advisory" || f.Defect == "" {
			key += strings.TrimSpace(f.Summary)
		}
		if i, ok := seen[key]; ok {
			if f.Evidence != "" && !strings.Contains(out[i].Evidence, f.Evidence) {
				out[i].Evidence += "\n" + f.Evidence
			}
			for _, loc := range f.Locations {
				found := false
				for _, old := range out[i].Locations {
					if loc == old {
						found = true
					}
				}
				if !found {
					out[i].Locations = append(out[i].Locations, loc)
				}
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, f)
	}
	return out
}

const criteriaInstruction = " Return raw JSON only: a nonempty array of {id, requirement, proof}. Cover every approved acceptance requirement; requirement must be an exact quote from the supplied requirements. Use stable unique IDs. proof is source, execution, or live, choosing the minimum evidence justified by the requirement. Do not add reviewer preferences or new requirements. Recorded decisions remain authoritative within their accepted scope. Live/visual validation is required only where approved criteria justify it. "
const checksInstruction = " Return checks (one per saved criterion): {id, status: passed|failed|blocked, evidence: [{type: source|unit|integration|live, check, reference, commit, result: inspected|exists|passed|failed|unverified}], missing}. Reference an observed execution result, command output or CI job/test result, not just a test file. Reuse relevant successful existing tests and trustworthy CI on the exact submitted commit when coverage is established; do not rerun or add tests without a meaningful gap. Unrelated successful tests cannot cover a requirement. Source-only conclusions and test existence are not execution proof. Checks and verdict must agree. Failed requires a demonstrated code defect, never merely missing evidence. Blocked identifies human-only validation or an unresolved scope decision, with missing proof and a specific next action. Infrastructure/tool failures are execution errors, never code defects; report status infrastructure_error so existing retries handle them. Each failed checklist item must have a finding naming its exact requirement. Each failing finding also includes observation with the same typed evidence fields and result failed on the exact head. Include next_action and next_owner. Do not perform live/visual validation without an explicitly authorized testing window. Do not turn reviewer advice into requirements. "
