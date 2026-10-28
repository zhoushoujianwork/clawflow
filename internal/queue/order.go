package queue

import (
	"math"
	"sort"
	"time"
)

// Item is the ordering view of one candidate.
type Item struct {
	Key       Key
	Priority  Priority
	CreatedAt string // RFC3339; unparseable sorts last within its rank
	Deps      []Key
}

func createdTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Unix(math.MaxInt32, 0)
	}
	return t
}

// Order returns the items sorted by (priority, rank, created_at, key). rank
// maps a key to its position in the rank file; unranked items sort after all
// ranked ones of the same priority. The input is not modified.
func Order(items []Item, rank map[Key]int) []Item {
	out := append([]Item(nil), items...)
	pos := func(k Key) int {
		if p, ok := rank[k]; ok {
			return p
		}
		return math.MaxInt
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if pa, pb := pos(a.Key), pos(b.Key); pa != pb {
			return pa < pb
		}
		if ta, tb := createdTime(a.CreatedAt), createdTime(b.CreatedAt); !ta.Equal(tb) {
			return ta.Before(tb)
		}
		return a.Key.Less(b.Key)
	})
	return out
}

// Cycles returns every key that is a member of a dependency cycle (including
// self-dependencies). Edges to keys not present in deps are ignored.
func Cycles(deps map[Key][]Key) map[Key]bool {
	index := map[Key]int{}
	low := map[Key]int{}
	on := map[Key]bool{}
	var stack []Key
	next := 0
	out := map[Key]bool{}
	var strong func(v Key)
	strong = func(v Key) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		on[v] = true
		for _, w := range deps[v] {
			if _, known := deps[w]; !known {
				continue
			}
			if _, seen := index[w]; !seen {
				strong(w)
				if low[w] < low[v] {
					low[v] = low[w]
				}
			} else if on[w] && index[w] < low[v] {
				low[v] = index[w]
			}
		}
		if low[v] != index[v] {
			return
		}
		var comp []Key
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			on[w] = false
			comp = append(comp, w)
			if w == v {
				break
			}
		}
		if len(comp) > 1 {
			for _, w := range comp {
				out[w] = true
			}
			return
		}
		for _, w := range deps[v] {
			if w == v {
				out[v] = true
			}
		}
	}
	keys := make([]Key, 0, len(deps))
	for k := range deps {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Less(keys[j]) })
	for _, k := range keys {
		if _, seen := index[k]; !seen {
			strong(k)
		}
	}
	return out
}
