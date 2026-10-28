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

// EffectiveScope resolves the repo setting, then the global setting, then
// labeled. Unknown values also fall back to labeled.
func EffectiveScope(repoScope, defaultScope string, _ bool) (scope string, fellBack bool) {
	raw := repoScope
	if raw == "" {
		raw = defaultScope
	}
	if raw == "" {
		return ScopeLabeled, false
	}
	if ValidScope(raw) {
		return raw, false
	}
	return ScopeLabeled, true
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
