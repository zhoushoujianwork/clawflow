package acceptance

import (
	"encoding/json"
	"errors"
	"strings"
)

// applyUnstructuredReport salvages only explicit failing content, never a pass.
// Source and native identity checks must already have succeeded.
func applyUnstructuredReport(r *Record, text string, validationErr error) bool {
	if len(text) > maxFileBytes {
		return false
	}
	var report map[string]json.RawMessage
	if json.Unmarshal([]byte(unfenceReport(text)), &report) != nil {
		return false
	}
	read := func(raw json.RawMessage) string {
		var value Text
		_ = json.Unmarshal(raw, &value)
		return strings.TrimSpace(string(value))
	}
	var findings []Finding
	for _, field := range []string{"findings", "checks"} {
		var items []json.RawMessage
		if json.Unmarshal(report[field], &items) != nil {
			continue
		}
		for _, raw := range items {
			var item map[string]json.RawMessage
			if json.Unmarshal(raw, &item) != nil {
				continue
			}
			kind, status := read(item["kind"]), read(item["status"])
			if field == "findings" && kind != "requirement" && kind != "defect" && status != "failed" || field == "checks" && status != "failed" {
				continue
			}
			var parts []string
			for _, key := range []string{"summary", "requirement", "defect", "evidence", "missing", "text"} {
				if value := read(item[key]); value != "" {
					parts = append(parts, key+": "+value)
				}
			}
			// Typed checklist evidence is retained as text, without trusting its shape.
			if field == "checks" {
				var evidence []map[string]json.RawMessage
				_ = json.Unmarshal(item["evidence"], &evidence)
				for _, e := range evidence {
					for _, key := range []string{"check", "reference"} {
						if value := read(e[key]); value != "" {
							parts = append(parts, key+": "+value)
						}
					}
				}
			}
			if len(parts) > 0 {
				content := strings.Join(parts, "\n")
				findings = append(findings, Finding{Kind: "unstructured", Summary: content, Evidence: Text(content)})
			}
		}
	}
	if len(findings) == 0 {
		return false
	}
	r.Unstructured, r.ValidationError = true, validationErr.Error()
	r.Verdict, r.Findings = Failed, DeduplicateFindings(findings)
	r.Report = "failed (unstructured findings)"
	r.Evidence = []string{"Untrusted reviewer findings; report validation failed: " + validationErr.Error()}
	r.NextOwner, r.NextAction = "implementation", "Repair the reported findings, then run fresh acceptance."
	return true
}

func validateUnstructured(r Record) error {
	has := false
	for _, f := range r.Findings {
		has = has || f.Kind == "unstructured"
	}
	if !r.Unstructured && (has || r.ValidationError != "") {
		return errors.New("unstructured findings require an explicit marker")
	}
	if r.Unstructured {
		if r.Verdict != Failed || !has || !nonempty(r.ValidationError) || r.Correction == nil {
			return errors.New("unstructured report requires failed findings and correction history")
		}
		for _, f := range r.Findings {
			if f.Kind != "unstructured" {
				return errors.New("unstructured report contains structured findings")
			}
		}
	}
	return nil
}
