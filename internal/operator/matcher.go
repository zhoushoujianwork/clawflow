package operator

import "slices"

// Subject is the operator runtime view of an issue or PR. The CLI layer
// converts vcs.Issue / vcs.PR into this unified shape so matchers don't care
// about platform specifics.
type Subject struct {
	Number     int
	Title      string
	Body       string
	Labels     []string
	IsPR       bool
	HeadBranch string // PR only
	URL        string // HTML URL, if the VCS exposes one
}

// HasLabel reports whether the subject carries the given label.
func (s *Subject) HasLabel(label string) bool {
	return slices.Contains(s.Labels, label)
}

// Matches reports whether `op` should fire on `sub` based on the trigger
// rules. The lock label is NOT considered here — the runner handles that.
func Matches(sub *Subject, op *Operator) bool {
	if op.Trigger.Target == "issue" && sub.IsPR {
		return false
	}
	if op.Trigger.Target == "pr" && !sub.IsPR {
		return false
	}

	labelSet := make(map[string]struct{}, len(sub.Labels))
	for _, l := range sub.Labels {
		labelSet[l] = struct{}{}
	}
	for _, req := range op.Trigger.LabelsRequired {
		if _, ok := labelSet[req]; !ok {
			return false
		}
	}
	for _, ex := range op.Trigger.LabelsExcluded {
		if _, ok := labelSet[ex]; ok {
			return false
		}
	}
	return true
}
