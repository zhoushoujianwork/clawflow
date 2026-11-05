package acceptance

import (
	"encoding/json"
	"testing"
	"time"
)

func missingEvidenceReport(t *testing.T, mutate func(*RequirementCheck, *[]Finding, map[string]any)) (Record, string) {
	t.Helper()
	r := structuredRecord()
	r.Checks[0] = RequirementCheck{ID: "R1", Status: Blocked, Missing: "no execution evidence for required behavior",
		Evidence: []RequirementEvidence{{Type: "source", Check: "read code", Reference: "main.go", Commit: r.Contract.HeadSHA, Result: "inspected"}}}
	findings := []Finding{
		{Kind: "requirement", Summary: "no execution evidence", Requirement: "Required behavior", Evidence: "missing execution evidence"},
		{Kind: "requirement", Summary: "still no execution evidence", Requirement: "Required behavior", Evidence: "missing execution evidence 2"},
	}
	m := map[string]any{"status": Failed, "summary": "gap", "evidence": []string{"ran: go test => ok"}, "next_action": "add tests", "next_owner": "implementer"}
	if mutate != nil {
		mutate(&r.Checks[0], &findings, m)
	}
	m["checks"], m["findings"] = r.Checks, findings
	b, _ := json.Marshal(m)
	return r, string(b)
}

func TestMissingEvidenceReportIsNormalizedToBlocked(t *testing.T) {
	r, text := missingEvidenceReport(t, nil)
	rec := Record{Plan: r.Plan}
	if err := applyReviewReport(r.Contract, &rec, text); err != nil {
		t.Fatal(err)
	}
	if rec.Verdict != Blocked || rec.NextOwner != "implementer" {
		t.Fatalf("verdict=%s owner=%s", rec.Verdict, rec.NextOwner)
	}
	for _, f := range rec.Findings {
		if f.Failing() {
			t.Fatalf("finding still failing: %+v", f)
		}
	}
}

func TestMissingEvidenceUnknownOwnerBecomesImplementer(t *testing.T) {
	for _, owner := range []string{"", "somebody"} {
		r, text := missingEvidenceReport(t, func(_ *RequirementCheck, _ *[]Finding, m map[string]any) { m["next_owner"] = owner })
		rec := Record{Plan: r.Plan}
		if err := applyReviewReport(r.Contract, &rec, text); err != nil {
			t.Fatal(err)
		}
		if rec.NextOwner != "implementer" {
			t.Fatalf("owner %q -> %q", owner, rec.NextOwner)
		}
	}
	r, text := missingEvidenceReport(t, func(_ *RequirementCheck, _ *[]Finding, m map[string]any) { m["next_owner"] = "environment" })
	rec := Record{Plan: r.Plan}
	if err := applyReviewReport(r.Contract, &rec, text); err != nil || rec.NextOwner != "environment" {
		t.Fatalf("known owner changed: %v %q", err, rec.NextOwner)
	}
}

func TestAdvisoryWithBlockedCheckUnaffected(t *testing.T) {
	r, text := missingEvidenceReport(t, func(_ *RequirementCheck, f *[]Finding, m map[string]any) {
		*f = []Finding{{Kind: "advisory", Summary: "note"}}
		m["status"] = Blocked
	})
	rec := Record{Plan: r.Plan}
	if err := applyReviewReport(r.Contract, &rec, text); err != nil || rec.Verdict != Blocked {
		t.Fatalf("%v %s", err, rec.Verdict)
	}
}

func TestNormalizationDoesNotRelaxRealOrUnlinkedFailures(t *testing.T) {
	// Linked check passed: still invalid.
	r, text := missingEvidenceReport(t, func(c *RequirementCheck, _ *[]Finding, _ map[string]any) {
		*c = structuredRecord().Checks[0]
	})
	if err := applyReviewReport(r.Contract, &Record{Plan: r.Plan}, text); err == nil {
		t.Fatal("finding against a passed check must stay invalid")
	}
	// No corresponding check: still invalid.
	r, text = missingEvidenceReport(t, func(_ *RequirementCheck, f *[]Finding, _ map[string]any) {
		(*f)[0].Requirement = "Other"
		*f = (*f)[:1]
	})
	if err := applyReviewReport(r.Contract, &Record{Plan: r.Plan}, text); err == nil {
		t.Fatal("finding without a checklist item must stay invalid")
	}
	// Failed observation: untouched, verdict stays failed.
	r = structuredRecord()
	r.Checks[0].Status = Failed
	r.Checks[0].Evidence[0].Result = "failed"
	obs := r.Checks[0].Evidence[0]
	f := []Finding{{Kind: "requirement", Summary: "bad", Requirement: "Required behavior", Evidence: "wrong", Observation: &obs}}
	b, _ := json.Marshal(map[string]any{"status": Failed, "summary": "s", "evidence": []string{"ran: x => y"}, "checks": r.Checks, "findings": f, "next_action": "fix", "next_owner": "implementer"})
	rec := Record{Plan: r.Plan}
	if err := applyReviewReport(r.Contract, &rec, string(b)); err != nil || rec.Verdict != Failed || !rec.Findings[0].Failing() {
		t.Fatalf("%v %s", err, rec.Verdict)
	}
}

func TestNormalizeNeverProducesPassed(t *testing.T) {
	crit := []Criterion{{"R1", "Required behavior", "execution"}}
	checks := []RequirementCheck{{ID: "R1", Status: Blocked, Missing: "gap"}}
	f := []Finding{{Kind: "defect", Requirement: "Required behavior", Summary: "s", Defect: "d", Evidence: "e"}}
	_, status, _, _, changed := normalizeMissingEvidence(crit, checks, f, Failed, "implementer", "a")
	if !changed || status != Blocked {
		t.Fatalf("%v %s", changed, status)
	}
	_, status, _, _, _ = normalizeMissingEvidence(crit, checks, f, Passed, "x", "a")
	if status == Failed {
		t.Fatal("status must not be rewritten from passed")
	}
	// A separate failed check keeps the verdict failed.
	checks2 := append(checks, RequirementCheck{ID: "R2", Status: Failed})
	_, status, _, _, _ = normalizeMissingEvidence(crit, checks2, f, Failed, "x", "a")
	if status != Failed {
		t.Fatalf("status %s", status)
	}
}

func TestTrailingUnstructured(t *testing.T) {
	u := Record{Completed: true, Unstructured: true}
	v := Record{Completed: true}
	if TrailingUnstructured([]Record{v, u, u}) != 2 || TrailingUnstructured([]Record{u, v}) != 0 || TrailingUnstructured(nil) != 0 {
		t.Fatal("trailing count")
	}
	if MaxUnstructuredRetries != 2 {
		t.Fatal("retry bound")
	}
}

func TestUnstructuredAttemptsCountsExistingRecords(t *testing.T) {
	s := openTestStore(t)
	c := contract()
	first := passing()
	if err := s.SaveRecord(first); err != nil {
		t.Fatal(err)
	}
	u1 := laterRecord(first, "u1", 10*time.Second)
	u1.Verdict, u1.Unstructured = Failed, true
	u1.ValidationError, u1.Correction = "bad report", &ReportCorrection{ValidationError: "bad report"}
	u1.Findings = []Finding{{Kind: "unstructured", Summary: "x", Evidence: "x"}}
	u2 := laterRecord(first, "u2", 20*time.Second)
	u2.Verdict, u2.Unstructured = Failed, true
	u2.ValidationError, u2.Correction = "bad report", &ReportCorrection{ValidationError: "bad report"}
	u2.Findings = []Finding{{Kind: "unstructured", Summary: "x", Evidence: "x"}}
	for _, r := range []Record{u1, u2} {
		if err := s.SaveRecord(r); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.UnstructuredAttempts(c)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	// A later verdict resets the count.
	v := laterRecord(first, "v", 30*time.Second)
	v.Verdict = Failed
	if err := s.SaveRecord(v); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.UnstructuredAttempts(c); n != 0 {
		t.Fatalf("n=%d", n)
	}
}
