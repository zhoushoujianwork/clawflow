package acceptance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

func TestRunnerReportCorrection(t *testing.T) {
	for _, mode := range []string{"parse", "passed", "checklist", "observation", "twice-invalid", "unstructured", "malformed-pass", "infrastructure", "execution", "changed-before", "changed-after", "wrong-session", "reused-run", "wrong-profile", "correction-execution"} {
		t.Run(mode, func(t *testing.T) {
			r, scratch, workspace := setupRunner(t)
			fixture := structuredRecord()
			fixture.Verdict = Failed
			fixture.Checks[0].Status = Failed
			fixture.Checks[0].Evidence[0].Result = "failed"
			obs := fixture.Checks[0].Evidence[0]
			fixture.Findings = []Finding{{Kind: "requirement", Summary: "wrong result", Requirement: "Required behavior", Evidence: "observed wrong result", Observation: &obs}}
			report := func(findings []Finding) string {
				b, err := json.Marshal(map[string]any{"status": Failed, "summary": "observed defect", "evidence": fixture.Evidence, "checks": fixture.Checks, "findings": findings, "next_action": fixture.NextAction, "next_owner": fixture.NextOwner})
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			valid := report(fixture.Findings)
			if mode == "passed" {
				fixture = structuredRecord()
				b, _ := json.Marshal(map[string]any{"status": Passed, "summary": "verified", "evidence": fixture.Evidence, "checks": fixture.Checks, "findings": fixture.Findings, "next_action": fixture.NextAction, "next_owner": fixture.NextOwner})
				valid = string(b)
			}
			bad, wantError := "{", "invalid acceptance report:"
			if mode == "checklist" {
				bad = report([]Finding{{Kind: "advisory", Summary: "optional advice"}})
				wantError = "failed checklist item lacks its actionable finding"
			}
			if mode == "observation" {
				f := fixture.Findings[0]
				f.Observation = nil
				bad = report([]Finding{f})
				wantError = "defect finding requires exact-head structured evidence"
			}
			inspections := 0
			inspect := r.Inspect
			r.Inspect = func(ctx context.Context, w string) (Source, error) {
				inspections++
				s, err := inspect(ctx, w)
				if mode == "changed-before" && inspections == 3 || mode == "changed-after" && inspections == 4 {
					s.Clean = false
				}
				return s, err
			}
			calls, corrections := 0, 0
			r.Execute = func(ctx context.Context, prompt string, o execution.Options) (execution.Result, error) {
				calls++
				if o.Workspace == scratch {
					result := successful("planner", "plan-run")
					result.Text = fixture.Plan.Text
					return result, nil
				}
				result := reviewerResult("reviewer", "review-run")
				result.Text = bad
				if !o.Resume {
					if mode == "execution" {
						return result, errors.New("execution failed")
					}
					if mode == "infrastructure" {
						result.Text = `{"status":"infrastructure_error","summary":"offline","evidence":{}}`
					}
					return result, nil
				}
				corrections++
				if o.SessionKey != "reviewer" || o.Profile != result.Profile || o.Workspace != workspace || o.PermissionMode != "bypassPermissions" {
					t.Fatalf("correction options: %+v", o)
				}
				if !strings.Contains(prompt, wantError) || !strings.Contains(strings.ToLower(prompt), "return the corrected full json report only; do not re-review") || !strings.Contains(prompt, "observation") {
					t.Fatalf("correction prompt: %s", prompt)
				}
				result.RunID, result.Text = "correction-run", valid
				result.Usage = json.RawMessage(`{"input_tokens":12,"output_tokens":7}`)
				switch mode {
				case "unstructured":
					result.Text = `{"status":"passed","findings":[{"kind":"defect","summary":"wrong result","defect":"wrong value","evidence":"observed wrong result"}]}`
				case "malformed-pass":
					result.Text = `{"status":"passed","summary":"verified","evidence":[]}`
				case "twice-invalid":
					result.Text = bad
				case "wrong-session":
					result.NativeSessionID = "another-reviewer"
				case "reused-run":
					result.RunID = "review-run"
				case "wrong-profile":
					result.Profile = "other-profile"
				case "correction-execution":
					return result, errors.New("correction failed")
				}
				return result, nil
			}
			record, err := r.Run(context.Background(), fixture.Contract, scratch, workspace)
			success := mode == "parse" || mode == "passed" || mode == "checklist" || mode == "observation" || mode == "unstructured"
			if mode == "unstructured" {
				if !record.Unstructured || record.ValidationError == "" || Evaluate(fixture.Contract, record) == nil {
					t.Fatalf("unsafe salvaged record: %+v", record)
				}
			}
			if success {
				if err != nil || record.Verdict != fixture.Verdict {
					t.Fatalf("verdict=%s err=%v", record.Verdict, err)
				}
				if err := ValidateUpload(fixture.Contract, record); err != nil {
					t.Fatal(err)
				}
			} else if err == nil || record.Verdict != Blocked || !IsInfrastructure(record) {
				t.Fatalf("unsafe verdict=%s err=%v", record.Verdict, err)
			}
			wantCorrections := 1
			if mode == "infrastructure" || mode == "execution" || mode == "changed-before" {
				wantCorrections = 0
			}
			if corrections != wantCorrections || calls != 2+wantCorrections {
				t.Fatalf("calls=%d corrections=%d", calls, corrections)
			}
			if mode == "twice-invalid" && !strings.Contains(err.Error(), record.Correction.ValidationError) {
				t.Fatalf("lost validation error: %v", err)
			}
			stored, loadErr := r.Store.LoadRecord(fixture.Contract, record.AttemptID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if mode != "infrastructure" && mode != "execution" {
				if stored.Correction == nil || !strings.Contains(stored.Correction.ValidationError, wantError) {
					t.Fatalf("missing correction: %+v", stored.Correction)
				}
			} else if stored.Correction != nil {
				t.Fatal("unexpected correction")
			}
			if corrections == 1 {
				if inspections != 4 {
					t.Fatalf("inspections=%d", inspections)
				}
				rows := recordedRows(t)
				found := false
				for _, row := range rows {
					if row.Operator == "acceptance-review-correction" {
						found = true
						if row.InputTokens == nil || *row.InputTokens != 12 {
							t.Fatal("missing correction usage")
						}
					}
				}
				if !found {
					t.Fatal("missing correction run")
				}
			}
		})
	}
}

func TestCorrectionIdentityCannotBypassGate(t *testing.T) {
	for _, mode := range []string{"valid", "session", "run", "planner-run", "development-run", "profile", "policy", "error", "validation-error"} {
		t.Run(mode, func(t *testing.T) {
			r := structuredRecord()
			n := r.Reviewer
			n.RunID = "correction-run"
			r.Correction = &ReportCorrection{ValidationError: "invalid report", Reviewer: n}
			switch mode {
			case "session":
				r.Correction.Reviewer.SessionID = "other"
			case "run":
				r.Correction.Reviewer.RunID = r.Reviewer.RunID
			case "planner-run":
				r.Correction.Reviewer.RunID = r.Plan.Planner.RunID
			case "development-run":
				r.Correction.Reviewer.RunID = r.Contract.Development.RunID
			case "profile":
				r.Correction.Reviewer.Profile = "other"
			case "policy":
				r.Correction.Reviewer.Policy = "default"
			case "error":
				r.Correction.Reviewer.Status = "error"
			case "validation-error":
				r.Correction.ValidationError = ""
			}
			for name, validate := range map[string]func(Contract, Record) error{"gate": Evaluate, "upload": ValidateUpload} {
				if err := validate(r.Contract, r); (err == nil) != (mode == "valid") {
					t.Fatalf("%s: %v", name, err)
				}
			}
		})
	}
}
