package acceptance

import (
	"regexp"
	"strings"
)

// Keep the fallback deliberately narrow. General interpretation belongs to the
// isolated planner; this guard rejects known process-only criteria, not clauses
// that also specify delivered behavior or a diff-checkable restriction.
var processRequirement = regexp.MustCompile(`(?i)^(inspect current main before editing(?:; baseline reviewed: [^;\n]+)?|baseline reviewed: [^;\n]+|no deployment(?: or coordinator enablement)? is authorized(?: by this issue)?|dependencies must land (?:first|before dependent implementation))[.!]?$`)

func processOnlyRequirement(text string) bool {
	return processRequirement.MatchString(strings.TrimSpace(text))
}

func processOnlyFinding(f Finding) bool {
	if !f.Failing() || len(f.Locations) != 0 {
		return false
	}
	noDefect := func(s string) bool {
		s = strings.Trim(strings.ToLower(strings.TrimSpace(s)), ". ")
		return s == "no demonstrated defect"
	}
	content := f.Summary + ";" + f.Defect + ";" + string(f.Evidence)
	if e := f.Observation; e != nil {
		// An unverified observation can describe the same process-only gap.
		// Inspect its text too, so mixed or concrete evidence is never hidden.
		if e.Result != "unverified" {
			return false
		}
		if !processOnlyRequirement(e.Check) {
			content += ";" + e.Check
		}
		content += ";" + e.Reference
	}
	if !strings.Contains(strings.ToLower(content), "no demonstrated defect") {
		return false
	}
	// Every evidence clause must describe a process-only gap. A code observation,
	// test failure or diff violation alongside the gap must keep its failure path.
	hasGap := false
	for _, clause := range strings.FieldsFunc(content, func(r rune) bool { return r == '.' || r == ';' || r == '\n' }) {
		clause = strings.ToLower(strings.TrimSpace(clause))
		if clause == "" || noDefect(clause) {
			continue
		}
		process := containsAny(clause, "historical", "before editing", "baseline", "deployment authorization", "deployment was authorized", "dependencies landed first", "dependency ordering", "process evidence", "implementer process")
		missing := containsAny(clause, "missing", "cannot verify", "cannot prove", "not available", "no evidence", "not evidenced", "unavailable", "cannot be verified", "cannot be proven", "not recorded", "not demonstrated", "not verifiable")
		if !process || !missing {
			return false
		}
		hasGap = true
	}
	return hasGap
}

func containsAny(s string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(s, value) {
			return true
		}
	}
	return false
}

// normalizeProcessFindings retains informational notes without repair authority.
func normalizeProcessFindings(findings []Finding) bool {
	changed := false
	for i, f := range findings {
		if processOnlyFinding(f) {
			findings[i].Kind = "advisory"
			changed = true
		}
	}
	return changed
}
