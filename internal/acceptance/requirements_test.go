package acceptance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

func structuredRecord() Record {
	r := passing()
	r.Contract.EvidenceSchema = 1
	r.ContractDigest, _ = r.Contract.Digest()
	r.Plan.ContractDigest = r.ContractDigest
	r.Plan.Criteria = []Criterion{{"R1", "Required behavior", "execution"}}
	b, _ := json.Marshal(r.Plan.Criteria)
	r.Plan.Text, r.Plan.Digest = string(b), digest(string(b))
	r.Checks = []RequirementCheck{{ID: "R1", Status: Passed, Evidence: []RequirementEvidence{{Type: "unit", Check: "TestRequiredBehavior covers required behavior", Reference: "CI run 7/job 9/TestRequiredBehavior: PASS", Commit: r.Contract.HeadSHA, Result: "passed"}}}}
	r.NextOwner, r.NextAction = "runner", "Apply CI and merge gates"
	r.Evidence = []string{"Reused existing test result; no duplicate execution"}
	return r
}

func TestRequirementEvidenceReuseAndGaps(t *testing.T) {
	r := structuredRecord()
	if err := Evaluate(r.Contract, r); err != nil {
		t.Fatal(err)
	}
	if err := ValidateUpload(r.Contract, r); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Record){
		"test file exists":      func(r *Record) { r.Checks[0].Evidence[0].Result = "exists" },
		"source only":           func(r *Record) { r.Checks[0].Evidence[0].Type = "source"; r.Checks[0].Evidence[0].Result = "inspected" },
		"wrong commit":          func(r *Record) { r.Checks[0].Evidence[0].Commit = "old" },
		"unrelated requirement": func(r *Record) { r.Checks[0].ID = "R2" },
		"missing requirement":   func(r *Record) { r.Checks = nil },
		"duplicate":             func(r *Record) { r.Checks = append(r.Checks, r.Checks[0]) },
		"unverified":            func(r *Record) { r.Checks[0].Evidence[0].Result = "unverified" },
		"missing proof":         func(r *Record) { r.Checks[0].Missing = "visual result" },
		"missing action":        func(r *Record) { r.NextAction = "" },
		"failed checklist":      func(r *Record) { r.Checks[0].Status = Failed; r.Checks[0].Evidence[0].Result = "failed" },
		"expanded scope": func(r *Record) {
			r.Plan.Criteria[0].Requirement = "new preference"
			b, _ := json.Marshal(r.Plan.Criteria)
			r.Plan.Text = string(b)
			r.Plan.Digest = digest(string(b))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r := structuredRecord()
			change(&r)
			if Evaluate(r.Contract, r) == nil {
				t.Fatal("accepted invalid proof")
			}
		})
	}
}

func TestRequirementOutcomeRouting(t *testing.T) {
	for _, status := range []Verdict{Passed, Failed, Blocked, "infrastructure_error"} {
		t.Run(string(status), func(t *testing.T) {
			fixture := structuredRecord()
			fixture.Verdict = status
			fixture.Checks[0].Status = status
			if status == Failed {
				fixture.Checks[0].Evidence[0].Result = "failed"
				obs := fixture.Checks[0].Evidence[0]
				fixture.Findings = []Finding{{Kind: "requirement", Summary: "wrong result", Requirement: "Required behavior", Evidence: "TestRequiredBehavior fails", Observation: &obs}}
			}
			if status == Blocked {
				fixture.Checks[0].Evidence[0].Result = "unverified"
				fixture.Checks[0].Missing = "Owner must authorize the required live validation window"
				fixture.NextOwner = "owner"
				fixture.NextAction = fixture.Checks[0].Missing
			}
			runner, scratch, workspace := setupRunner(t)
			runner.Execute = func(ctx context.Context, prompt string, opts execution.Options) (execution.Result, error) {
				result := reviewerResult("reviewer", "review-run")
				var err error
				if opts.ToolsDisabled {
					result = successful("planner", "plan-run")
				}
				if opts.ToolsDisabled {
					result.Text = fixture.Plan.Text
				} else {
					if !strings.Contains(prompt, "Reuse relevant successful existing tests") || strings.Contains(prompt, "You must run") {
						t.Fatal("review still mandates duplicate tests")
					}
					report := map[string]any{"status": status, "summary": "review complete", "evidence": fixture.Evidence, "findings": fixture.Findings, "checks": fixture.Checks, "next_action": fixture.NextAction, "next_owner": fixture.NextOwner}
					b, _ := json.Marshal(report)
					result.Text = string(b)
				}
				return result, err
			}
			record, err := runner.Run(context.Background(), fixture.Contract, scratch, workspace)
			if status == "infrastructure_error" {
				if err == nil || !IsInfrastructure(record) {
					t.Fatalf("not routed to retry: %v", err)
				}
				return
			}
			if err != nil || record.Verdict != status || IsInfrastructure(record) {
				t.Fatalf("verdict=%s err=%v", record.Verdict, err)
			}
			if err := ValidateUpload(record.Contract, record); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeduplicateRequirementConcerns(t *testing.T) {
	f := Finding{Kind: "defect", Defect: "wrong return value", Summary: "bad", Evidence: "test A", Locations: []FindingLocation{{"a.go", 1, "head"}}}
	other := f
	other.Evidence = "test B"
	other.Locations = []FindingLocation{{"a.go", 2, "head"}}
	result := DeduplicateFindings([]Finding{f, other, f})
	if len(result) != 1 || len(result[0].Locations) != 2 || result[0].Evidence != "test A\ntest B" {
		t.Fatalf("%+v", result)
	}
}

func TestCriterionMinimumProofAndVerdictAgreement(t *testing.T) {
	for _, proof := range []string{"source", "live"} {
		t.Run(proof, func(t *testing.T) {
			r := structuredRecord()
			r.Plan.Criteria[0].Proof = proof
			b, _ := json.Marshal(r.Plan.Criteria)
			r.Plan.Text = string(b)
			r.Plan.Digest = digest(string(b))
			if proof == "source" {
				r.Checks[0].Evidence[0].Type = "source"
				r.Checks[0].Evidence[0].Result = "inspected"
			} else {
				if Evaluate(r.Contract, r) == nil {
					t.Fatal("unit test substituted for required live proof")
				}
				r.Checks[0].Evidence[0].Type = "live"
			}
			if err := Evaluate(r.Contract, r); err != nil {
				t.Fatal(err)
			}
		})
	}
	r := structuredRecord()
	r.Verdict = Failed
	r.Findings = []Finding{{Kind: "advisory", Summary: "optional extra test"}}
	if ValidateUpload(r.Contract, r) == nil {
		t.Fatal("advice became repair authority")
	}
	r = structuredRecord()
	r.Verdict = Blocked
	if ValidateUpload(r.Contract, r) == nil {
		t.Fatal("blocked verdict with passing checklist")
	}
	r = structuredRecord()
	r.Verdict = Failed
	r.Findings = []Finding{{Kind: "defect", Summary: "wrong result", Defect: "wrong return value", Evidence: "test failed"}}
	if ValidateUpload(r.Contract, r) == nil {
		t.Fatal("unbound defect became repair authority")
	}
}

func TestFailingRequirementFindingMustMatchChecklist(t *testing.T) {
	for _, status := range []Verdict{Passed, Blocked, Failed} {
		t.Run(string(status), func(t *testing.T) {
			r := structuredRecord()
			r.Verdict = Failed
			r.Checks[0].Status = status
			observation := r.Checks[0].Evidence[0]
			observation.Result = "failed"
			r.Findings = []Finding{{Kind: "requirement", Summary: "wrong behavior", Requirement: r.Plan.Criteria[0].Requirement, Evidence: "TestRequiredBehavior fails", Observation: &observation}}
			if status == Failed {
				r.Checks[0].Evidence[0] = observation
			} else if status == Blocked {
				r.Checks[0].Evidence = nil
				r.Checks[0].Missing = "Human validation pending"
			}
			for name, validate := range map[string]func(Contract, Record) error{"checks": ValidateChecks, "upload": ValidateUpload} {
				err := validate(r.Contract, r)
				if status == Failed && err != nil {
					t.Fatalf("%s rejected consistent failure: %v", name, err)
				}
				if status != Failed && err == nil {
					t.Errorf("%s accepted failed requirement with %s checklist", name, status)
				}
			}
		})
	}
}

func TestFailingFindingRequirementLink(t *testing.T) {
	for _, kind := range []string{"requirement", "defect"} {
		for _, requirement := range []string{"Required behavior", "behavior", ""} {
			t.Run(kind+"/"+requirement, func(t *testing.T) {
				r := structuredRecord()
				r.Verdict = Failed
				observation := r.Checks[0].Evidence[0]
				observation.Result = "failed"
				r.Findings = []Finding{{Kind: kind, Summary: "wrong result", Requirement: requirement, Defect: "wrong return value", Evidence: "TestResult fails", Observation: &observation}}
				err := ValidateUpload(r.Contract, r)
				if kind == "defect" && requirement == "" {
					if err != nil {
						t.Fatalf("standalone defect rejected: %v", err)
					}
				} else if err == nil {
					t.Fatal("unmatched or contradictory requirement finding accepted")
				}
			})
		}
	}
}
