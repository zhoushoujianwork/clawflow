package acceptance

import "testing"

func TestTextAcceptsStringOrArray(t *testing.T) {
	var check RequirementCheck
	if err := decodeStrict([]byte(`{"id":"AC-1","status":"blocked","evidence":[],"missing":["base unavailable","no remote"]}`), &check); err != nil {
		t.Fatalf("array missing rejected: %v", err)
	}
	if check.Missing != "base unavailable\nno remote" {
		t.Fatalf("missing = %q", check.Missing)
	}
	var f Finding
	if err := decodeStrict([]byte(`{"kind":"requirement","summary":"s","defect":null,"evidence":["a","b"]}`), &f); err != nil {
		t.Fatalf("array evidence rejected: %v", err)
	}
	if f.Evidence != "a\nb" || f.Defect != "" {
		t.Fatalf("finding = %+v", f)
	}
	check = RequirementCheck{}
	if err := decodeStrict([]byte(`{"id":"AC-1","status":"passed","evidence":[],"missing":""}`), &check); err != nil || check.Missing != "" {
		t.Fatalf("string missing: %v %q", err, check.Missing)
	}
	if err := decodeStrict([]byte(`{"id":"AC-1","status":"passed","evidence":[],"missing":{"x":1}}`), &check); err == nil {
		t.Fatal("object missing accepted")
	}
}
