package acceptance

import (
	"encoding/json"
	"testing"
)

func TestValidateUpload(t *testing.T) {
	for _, verdict := range []Verdict{Passed, Failed, Blocked} {
		r := passing()
		r.Verdict = verdict
		if err := ValidateUpload(r.Contract, r); err != nil {
			t.Fatalf("%s: %v", verdict, err)
		}
	}
	for name, mutate := range map[string]func(*Record){
		"digest":     func(r *Record) { r.Plan.Digest = "tampered" },
		"stale":      func(r *Record) { r.Contract.HeadSHA = "different" },
		"self":       func(r *Record) { r.Reviewer.SessionID = r.Contract.Development.SessionID },
		"planner":    func(r *Record) { r.Reviewer.SessionID = r.Plan.Planner.SessionID },
		"command":    func(r *Record) { r.Evidence = []string{"looks good"} },
		"incomplete": func(r *Record) { r.Completed = false },
	} {
		t.Run(name, func(t *testing.T) {
			r := passing()
			mutate(&r)
			if ValidateUpload(contract(), r) == nil {
				t.Fatal("accepted invalid upload")
			}
		})
	}
	r := passing()
	raw, _ := json.Marshal(r)
	var decoded Record
	if err := DecodeUpload(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := DecodeUpload(append(raw[:len(raw)-1], []byte(`,"verdict":"passed"}`)...), &decoded); err == nil {
		t.Fatal("accepted duplicate field")
	}
}
