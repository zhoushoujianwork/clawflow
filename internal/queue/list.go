package queue

import "strconv"

// ListEntry is one row of the global queue view.
type ListEntry struct {
	Position int    `json:"position"` // 1-based place in the global order
	Key      string `json:"key"`
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	Title    string `json:"title,omitempty"`
	Scope    string `json:"scope,omitempty"` // effective candidate scope of the repo
	Tier     string `json:"tier"`            // P0..P3
	Rank     int    `json:"rank,omitempty"`  // 1-based position in queue.json, 0 = unranked
	State    string `json:"state"`           // in_flight | eligible | blocked:<r> | parked:<r> | waiting:dependency
}

// BuildList merges the per-repo states with the current rank file into the
// global order (priority, rank, created_at, key). repo filters to one repo
// when non-empty. Rank is taken from the live rank file, not from the state
// recorded at the last pass, so `queue move` shows up immediately.
//
// scopeOf returns a repo's effective scope (nil = use the scope recorded by
// the last pass). Entries that the scope would no longer admit are dropped:
// they are not candidates.
func BuildList(states []RepoState, order []Key, repo string, scopeOf func(RepoState) string) []ListEntry {
	pos := Positions(order)
	var items []Item
	meta := map[Key]EntryRecord{}
	scopes := map[string]string{}
	for _, st := range states {
		if repo != "" && st.Repo != repo {
			continue
		}
		scope := st.Scope
		if scopeOf != nil {
			scope = scopeOf(st)
		}
		scopes[st.Repo] = scope
		for _, e := range st.Entries {
			k := Key{Repo: st.Repo, Number: e.Number}
			if e.State != StateInFlight && e.State != StateParked {
				_, ranked := pos[k]
				if (scope == ScopeLabeled && !e.Labeled) || (scope == ScopeRanked && !ranked && !e.Labeled) {
					continue
				}
			}
			prio, _ := PriorityFromLabels([]string{e.Priority})
			items = append(items, Item{Key: k, Priority: prio, CreatedAt: e.Created})
			meta[k] = e
		}
	}
	var out []ListEntry
	for i, it := range Order(items, pos) {
		e := meta[it.Key]
		row := ListEntry{
			Position: i + 1, Key: it.Key.String(), Repo: it.Key.Repo, Number: it.Key.Number,
			Title: e.Title, Tier: it.Priority.String(), State: e.State, Scope: scopes[it.Key.Repo],
		}
		if e.Reason != "" {
			row.State += ":" + e.Reason
		}
		if p, ok := pos[it.Key]; ok {
			row.Rank = p + 1
		}
		out = append(out, row)
	}
	return out
}

// RankLabel renders a rank for tables: "-" when unranked.
func RankLabel(rank int) string {
	if rank <= 0 {
		return "-"
	}
	return strconv.Itoa(rank)
}
