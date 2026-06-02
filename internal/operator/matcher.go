package operator

import (
	"fmt"
	"slices"
)

// Subject is the operator runtime view of an issue or PR. The CLI layer
// converts vcs.Issue / vcs.PR into this unified shape so matchers don't care
// about platform specifics.
type Subject struct {
	Number     int
	Title      string
	Body       string
	Labels     []string
	State      string // "open" | "closed"
	IsPR       bool
	HeadBranch string // PR only
	URL        string // HTML URL, if the VCS exposes one
	// SubTotal is the number of native sub-issues linked to this subject
	// (GitHub sub_issues_summary.total). 0 = leaf (or a platform without
	// native sub-issues, e.g. GitLab). Drives the AppliesTo structural gate.
	SubTotal int
}

// HasLabel reports whether the subject carries the given label.
func (s *Subject) HasLabel(label string) bool {
	return slices.Contains(s.Labels, label)
}

// Matches reports whether `op` should fire on `sub` based on the trigger
// rules. The lock label is NOT considered here — the runner handles that.
func Matches(sub *Subject, op *Operator) bool {
	ok, _ := MatchesWithReason(sub, op)
	return ok
}

// MatchesWithReason is Matches plus a human-readable reason string explaining
// the decision. On match the reason is "match"; on miss it names the rule
// that rejected the subject (e.g. `missing required label "feat"`). Used by
// the CLI's --debug trace; production matching uses the cheaper Matches.
func MatchesWithReason(sub *Subject, op *Operator) (bool, string) {
	if op.Trigger.Target == "issue" && sub.IsPR {
		return false, "target=issue but subject is PR"
	}
	if op.Trigger.Target == "pr" && !sub.IsPR {
		return false, "target=pr but subject is issue"
	}

	labelSet := make(map[string]struct{}, len(sub.Labels))
	for _, l := range sub.Labels {
		labelSet[l] = struct{}{}
	}
	for _, req := range op.Trigger.LabelsRequired {
		if _, ok := labelSet[req]; !ok {
			return false, fmt.Sprintf("missing required label %q", req)
		}
	}
	if len(op.Trigger.LabelsRequiredAny) > 0 {
		found := false
		for _, any := range op.Trigger.LabelsRequiredAny {
			if _, ok := labelSet[any]; ok {
				found = true
				break
			}
		}
		if !found {
			return false, fmt.Sprintf("none of required_any labels %v present", op.Trigger.LabelsRequiredAny)
		}
	}
	for _, ex := range op.Trigger.LabelsExcluded {
		if _, ok := labelSet[ex]; ok {
			return false, fmt.Sprintf("excluded label %q present", ex)
		}
	}
	// Structure-aware gate. "" and "any" impose no constraint (back-compat).
	switch op.Trigger.AppliesTo {
	case AppliesParent:
		if sub.SubTotal <= 0 {
			return false, "applies_to=parent but subject has no sub-issues (leaf)"
		}
	case AppliesLeaf:
		if sub.SubTotal > 0 {
			return false, fmt.Sprintf("applies_to=leaf but subject has %d sub-issue(s) (parent)", sub.SubTotal)
		}
	}
	return true, "match"
}
