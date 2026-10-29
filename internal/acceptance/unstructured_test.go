package acceptance

import (
	"errors"
	"strings"
	"testing"
)

func TestUnstructuredFailureExtraction(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{`{"status":"passed","findings":[{"kind":"defect","summary":"lost update"}]}`, true},
		{`{"checks":[{"status":"failed","missing":"wrong value"}]}`, true},
		{`{"findings":[42,{"kind":"defect","summary":"lost update"}]}`, true},
		{`{"checks":[{"status":"failed","evidence":[{"check":"TestValue failed"}]}]}`, true},
		{`{"status":"failed","summary":"bad"}`, false},
		{`{"status":"passed","findings":[{"kind":"advisory","summary":"optional test"}]}`, false},
		{`{"checks":[{"status":"failed","id":"R1"}]}`, false},
		{`{"findings":[{"kind":"defect","summary":"  "}]}`, false},
		{`{"status":"passed","summary":"ok","evidence":[]}`, false},
		{`{`, false},
	} {
		r := structuredRecord()
		if got := applyUnstructuredReport(&r, tc.text, errors.New("invalid schema")); got != tc.want {
			t.Fatalf("%s: got %v", tc.text, got)
		}
		if tc.want && (r.Verdict != Failed || !r.Unstructured || r.ValidationError != "invalid schema" || !strings.Contains(r.Findings[0].Summary, ":")) {
			t.Fatalf("record=%+v", r)
		}
	}
}

func TestUnstructuredUploadCannotAuthorizePass(t *testing.T) {
	r := structuredRecord()
	r.Correction = &ReportCorrection{ValidationError: "initial error", Reviewer: r.Reviewer}
	r.Correction.Reviewer.RunID = "correction-run"
	applyUnstructuredReport(&r, `{"findings":[{"kind":"defect","summary":"lost update"}]}`, errors.New("invalid schema"))
	if err := ValidateUpload(r.Contract, r); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"pass", "missing-marker", "missing-error", "missing-correction", "changed-source", "wrong-session"} {
		t.Run(mode, func(t *testing.T) {
			candidate := r
			switch mode {
			case "pass":
				candidate.Verdict = Passed
			case "missing-marker":
				candidate.Unstructured = false
			case "missing-error":
				candidate.ValidationError = ""
			case "missing-correction":
				candidate.Correction = nil
			case "changed-source":
				candidate.After.Clean = false
			case "wrong-session":
				c := *candidate.Correction
				c.Reviewer.SessionID = "other"
				candidate.Correction = &c
			}
			if ValidateUpload(r.Contract, candidate) == nil || Evaluate(r.Contract, candidate) == nil {
				t.Fatal("accepted unsafe report")
			}
		})
	}
}
