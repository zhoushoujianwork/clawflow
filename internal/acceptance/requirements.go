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
	Environment *EnvironmentEvidence `json:"environment,omitempty"`
	Type        string               `json:"type"` // source, unit, integration, live
	Check       string               `json:"check"`
	Reference   string               `json:"reference"`
	Commit      string               `json:"commit"`
	Result      string               `json:"result"` // inspected, exists, passed, failed, unverified
}

type RequirementCheck struct {
	ID       string                `json:"id"`
	Status   Verdict               `json:"status"`
	Evidence []RequirementEvidence `json:"evidence"`
	Missing  Text                  `json:"missing"`
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
		if processOnlyRequirement(item.Requirement) {
			return errors.New("criterion requires historical implementer process evidence, not an exact-head deliverable")
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
			if e.Type == "live" && e.Result == "passed" {
				if e.Environment == nil {
					return errors.New("live evidence requires runtime identity and five evidence layers")
				}
				if missing := e.Environment.missingText(c.HeadSHA); missing != "" {
					return errors.New("live evidence missing: " + missing)
				}
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
			if !nonempty(string(check.Missing)) {
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
			if f.Evidence != "" && !strings.Contains(string(out[i].Evidence), string(f.Evidence)) {
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

const criteriaInstruction = " Return raw JSON only: a nonempty array of {id, requirement, proof}. Derive criteria only from deliverable statements (outcomes, acceptance checks and required behavior) that the exact head can satisfy or violate. Implementer process instructions, baselines and authorization boundaries are not criteria and require no historical evidence. For example, exclude \"Inspect current main before editing; baseline reviewed: <sha>\", \"No deployment is authorized\", \"no deployment or coordinator enablement is authorized\" and \"dependencies must land first\". A boundary is eligible only in its explicit diff-checkable form, for example \"no deployment files changed\" or \"the PR changes no deployment or config files\"; do not invent that requirement from an authorization instruction. Cover every approved deliverable acceptance requirement; requirement must be an exact quote from the supplied requirements. Use stable unique IDs. proof is source, execution, or live, choosing the minimum evidence justified by the requirement. Do not add reviewer preferences or new requirements. Recorded decisions remain authoritative within their accepted scope. Live/visual validation is required only where approved criteria justify it. "
const checksInstruction = " Return checks (one per saved criterion): {id, status: passed|failed|blocked, evidence: [{type: source|unit|integration|live, check, reference, commit, result: inspected|exists|passed|failed|unverified}], missing: string}. Reference an observed execution result, command output or CI job/test result, not just a test file. Reuse relevant successful existing tests and trustworthy CI on the exact submitted commit when coverage is established; do not rerun or add tests without a meaningful gap. Unrelated successful tests cannot cover a requirement. Source-only conclusions and test existence are not execution proof. Checks and verdict must agree. Failed requires a demonstrated code defect, never merely missing evidence. A finding stating no demonstrated defect and citing only missing historical or implementer process evidence is an advisory informational note; the remaining deliverable criteria decide the verdict. Blocked identifies human-only validation or an unresolved scope decision, with missing proof and a specific next action. Infrastructure/tool failures are execution errors, never code defects. When a local check cannot run only because of the review environment (sandbox limits, missing generated build assets, caches) and trustworthy CI on the exact submitted commit covers that criterion, use that CI result as the execution proof and continue. Report status infrastructure_error only when no trustworthy evidence can establish a criterion, so existing retries handle it. Each failed checklist item must have a finding naming its exact requirement. Each failing finding also includes observation with the same typed evidence fields and result failed on the exact head. Include next_action and next_owner. Do not perform live/visual validation without an explicitly authorized testing window. Do not turn reviewer advice into requirements. "

const environmentInstruction = " For live evidence, include environment with candidate (submitted head), loaded_candidate (observed build provenance), cli_match (actual executed CLI, PATH-resolved CLI and host-selected CLI all agree), daemon_version, connector_version, logged_in, target_count, current and binding ({target_id: fresh opaque alias, generation: connection nonce}), and layers {environment-ready, candidate-loaded, offline, live-write, save-reload}, each {status: passed|failed|blocked|not-run, evidence: sanitized observation, binding: {target_id, generation} for live-write and save-reload}. Obtain fresh observations from the authorized environment probe; never infer matches from a build or offline pass. A reconnect invalidates all earlier window/document bindings. Retain failed and not-run layers. Missing identity or any required layer prevents live-verified; report blocked and identify missing evidence. Keep actual CLI paths in private machine evidence only; public reports use the match result and opaque references, never paths, application identities or secrets. This evidence does not authorize live actions or replace resource leases. "
