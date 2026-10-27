package queue

// Candidate scopes. Scope decides which open issues of a repo may enter the
// queue; in-flight items always count regardless of scope.
const (
	ScopeAll     = "all"     // every eligible open leaf issue
	ScopeLabeled = "labeled" // only issues labeled ScopeLabel
	ScopeRanked  = "ranked"  // only issues in the rank file, or labeled ScopeLabel (enrolled on sight)
)

// ScopeLabel opts an issue into the labeled and ranked scopes.
const ScopeLabel = "clawflow"

// ValidScope reports whether s is a known scope name.
func ValidScope(s string) bool {
	return s == ScopeAll || s == ScopeLabeled || s == ScopeRanked
}

// EffectiveScope resolves a repo's scope: the repo setting, else the global
// default, else "all". An unknown value fails closed for repos that ever had
// a queue_issue ("ranked", so unlisted work is never picked up) and falls
// back to "all" otherwise. fellBack reports that an unknown value was seen.
func EffectiveScope(repoScope, defaultScope string, hadQueueIssue bool) (scope string, fellBack bool) {
	raw := repoScope
	if raw == "" {
		raw = defaultScope
	}
	switch {
	case raw == "":
		return ScopeAll, false
	case ValidScope(raw):
		return raw, false
	case hadQueueIssue:
		return ScopeRanked, true
	}
	return ScopeAll, true
}

// Remove returns order without k.
func Remove(order []Key, k Key) []Key {
	out := make([]Key, 0, len(order))
	for _, o := range order {
		if o != k {
			out = append(out, o)
		}
	}
	return out
}
