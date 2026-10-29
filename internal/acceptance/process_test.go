package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

func TestPlannerExcludesHistoricalProcessCriteria(t *testing.T) {
	process := []string{
		"Inspect current main before editing; baseline reviewed: <sha>",
		"No deployment is authorized",
		"no deployment or coordinator enablement is authorized",
		"dependencies must land first",
	}
	c := contract()
	c.EvidenceSchema = 1
	c.Requirements = strings.Join(process, "\n") + "\nRequired behavior\nno deployment files changed"
	for _, requirement := range append([]string{"Required behavior", "no deployment files changed"}, process...) {
		t.Run(requirement, func(t *testing.T) {
			runner, scratch, _ := setupRunner(t)
			runner.Execute = func(_ context.Context, prompt string, _ execution.Options) (execution.Result, error) {
				for _, rule := range []string{"exact head can satisfy or violate", "not criteria and require no historical evidence", "no deployment files changed", "do not invent that requirement"} {
					if !strings.Contains(prompt, rule) {
						t.Fatalf("missing planning rule: %s", rule)
					}
				}
				result := successful("planner", "plan-run")
				b, _ := json.Marshal([]Criterion{{"R1", requirement, "source"}})
				result.Text = string(b)
				return result, nil
			}
			plan, err := runner.RequirementsPlan(context.Background(), c, scratch)
			if processOnlyRequirement(requirement) {
				if err == nil {
					t.Fatal("planner accepted a historical evidence criterion")
				}
			} else if err != nil || len(plan.Criteria) != 1 || plan.Criteria[0].Requirement != requirement {
				t.Fatalf("deliverable rejected: %v", err)
			}
		})
	}
}

func TestPlannerAcceptsDeliverablesFromMixedIssueBody(t *testing.T) {
	c := contract()
	c.EvidenceSchema = 1
	c.Requirements = "Inspect current main before editing; baseline reviewed: <sha>\nNo deployment is authorized\nRequired behavior: preserve supported configuration actions\nno deployment files changed"
	want := []Criterion{
		{"AC-001", "preserve supported configuration actions", "execution"},
		{"AC-002", "no deployment files changed", "source"},
	}
	runner, scratch, _ := setupRunner(t)
	runner.Execute = func(_ context.Context, prompt string, _ execution.Options) (execution.Result, error) {
		if !strings.Contains(prompt, c.Requirements) || !strings.Contains(prompt, criteriaInstruction) {
			t.Fatal("planner did not receive the mixed issue body and deliverable-only rules")
		}
		result := successful("planner", "mixed-plan-run")
		b, _ := json.Marshal(want)
		result.Text = string(b)
		return result, nil
	}
	plan, err := runner.RequirementsPlan(context.Background(), c, scratch)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Criteria) != len(want) {
		t.Fatalf("unexpected criteria: %+v", plan.Criteria)
	}
	for i, criterion := range plan.Criteria {
		if criterion != want[i] || processOnlyRequirement(criterion.Requirement) {
			t.Fatalf("unexpected criterion: %+v", criterion)
		}
	}
}

func processGap() Finding {
	return Finding{Kind: "requirement", Summary: "No demonstrated defect", Requirement: "Inspect current main before editing", Evidence: "Missing historical evidence that current main was inspected before editing"}
}

func TestProcessOnlyFindingFollowsRemainingCriteria(t *testing.T) {
	for _, status := range []Verdict{Passed, Blocked, Failed} {
		t.Run(string(status), func(t *testing.T) {
			r := structuredRecord()
			r.Checks[0].Status = status
			r.Findings = []Finding{processGap()}
			if status == Blocked {
				r.Checks[0].Evidence = nil
				r.Checks[0].Missing = "Human validation window required"
			}
			if status == Failed {
				r.Checks[0].Evidence[0].Result = "failed"
				obs := r.Checks[0].Evidence[0]
				r.Findings = append(r.Findings, Finding{Kind: "requirement", Summary: "wrong result", Requirement: r.Plan.Criteria[0].Requirement, Evidence: "TestRequiredBehavior failed", Observation: &obs})
			}
			b, _ := json.Marshal(map[string]any{"status": Failed, "summary": "review complete", "evidence": r.Evidence, "findings": r.Findings, "checks": r.Checks, "next_action": "Repair process evidence", "next_owner": "implementation"})
			if err := applyReviewReport(r.Contract, &r, string(b)); err != nil {
				t.Fatal(err)
			}
			if r.Verdict != status || r.Findings[0].Kind != "advisory" || r.Unstructured {
				t.Fatalf("incorrect normalization: %+v", r)
			}
			if err := ValidateUpload(r.Contract, r); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLegacyProcessFindingStillRequiresExecutionEvidence(t *testing.T) {
	for _, executed := range []bool{false, true} {
		r := passing()
		evidence := []string{"Missing historical evidence of baseline inspection"}
		if executed {
			evidence = append(evidence, EvidenceCommandPrefix+" go test ./... => passed")
		}
		b, _ := json.Marshal(map[string]any{
			"status": Failed, "summary": "review complete", "evidence": evidence,
			"findings": []Finding{processGap()},
		})
		err := applyReviewReport(r.Contract, &r, string(b))
		if !executed {
			if err == nil {
				t.Fatal("informational process note bypassed execution evidence")
			}
			continue
		}
		if err != nil || r.Verdict != Passed || r.Findings[0].Kind != "advisory" {
			t.Fatalf("legacy process note was not normalized: verdict=%s err=%v", r.Verdict, err)
		}
	}
}

func TestProcessNormalizationPreservesDefectsAndValidation(t *testing.T) {
	for _, mode := range []string{"observation", "location", "defect", "mixed evidence", "mixed summary", "missing checks", "stale proof"} {
		t.Run(mode, func(t *testing.T) {
			r := structuredRecord()
			f := processGap()
			switch mode {
			case "observation":
				e := r.Checks[0].Evidence[0]
				e.Result = "failed"
				f.Observation = &e
			case "location":
				f.Locations = []FindingLocation{{"a.go", 1, "head"}}
			case "defect":
				f.Defect = "lost update"
			case "mixed evidence":
				f.Evidence += "; TestRequiredBehavior fails with wrong result"
			case "mixed summary":
				f.Summary += "; TestRequiredBehavior fails"
			case "missing checks":
				r.Checks = nil
			case "stale proof":
				r.Checks[0].Evidence[0].Commit = "old"
			}
			b, _ := json.Marshal(map[string]any{"status": Failed, "summary": "review complete", "evidence": r.Evidence, "findings": []Finding{f}, "checks": r.Checks, "next_action": "repair", "next_owner": "implementation"})
			if err := applyReviewReport(r.Contract, &r, string(b)); err == nil {
				t.Fatal("normalization bypassed defect or proof validation")
			}
			if mode != "missing checks" && mode != "stale proof" && processOnlyFinding(f) {
				t.Fatal("concrete or mixed defect downgraded")
			}
		})
	}
}

func TestUnstructuredProcessGapCannotTriggerRepair(t *testing.T) {
	r := structuredRecord()
	b, _ := json.Marshal(map[string]any{"status": Failed, "findings": []Finding{processGap()}})
	if applyUnstructuredReport(&r, string(b), errors.New("missing checks")) {
		t.Fatal("process-only gap salvaged as a defect")
	}
	b, _ = json.Marshal(map[string]any{"status": Failed, "findings": []Finding{processGap(), {Kind: "defect", Summary: "lost update"}}})
	if !applyUnstructuredReport(&r, string(b), errors.New("missing checks")) || len(r.Findings) != 1 || !strings.Contains(r.Findings[0].Summary, "lost update") {
		t.Fatal("real unstructured defect lost")
	}
}

func TestProcessFindingRequiresExplicitNoDefectAndOnlyProcessEvidence(t *testing.T) {
	for _, evidence := range []string{
		"Missing historical evidence that current main was inspected before editing",
		"Cannot verify deployment authorization from the exact head",
		"No evidence that dependencies landed first",
	} {
		f := processGap()
		f.Evidence = Text(evidence)
		if !processOnlyFinding(f) {
			t.Fatalf("process-only evidence was not recognized: %s", evidence)
		}
	}
	for _, field := range []string{"summary", "defect", "evidence"} {
		f := processGap()
		switch field {
		case "defect":
			f.Kind, f.Summary, f.Defect = "defect", "Missing historical process evidence", "No demonstrated defect"
		case "evidence":
			f.Summary = "Missing historical process evidence"
			f.Evidence += "; No demonstrated defect"
		}
		if !processOnlyFinding(f) {
			t.Fatalf("process-only note not recognized with disclaimer in %s", field)
		}
	}
	f := processGap()
	f.Summary = "Required behavior not implemented"
	if processOnlyFinding(f) {
		t.Fatal("missing explicit no-defect statement was ignored")
	}
	f = processGap()
	f.Evidence = "The PR changes deployment files"
	if processOnlyFinding(f) {
		t.Fatal("diff-checkable violation was downgraded")
	}
}
