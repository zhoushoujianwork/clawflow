package queue

import (
	"fmt"
	"time"
)

// Defaults for the dispatch policy.
const (
	DefaultMaxInFlight = 2
	DefaultParkAfter   = 45 * time.Minute
	// MaxParkedExempt is how many parked items per repo stop counting toward
	// max_in_flight. Parked items beyond this still count, so a systemic
	// failure cannot drain the whole queue.
	MaxParkedExempt = 2
)

// Item states reported by Select.
const (
	StateInFlight = "in_flight"
	StateEligible = "eligible"
	StateBlocked  = "blocked"
	StateParked   = "parked"
	StateWaiting  = "waiting"
)

// Candidate is one open leaf issue of the repo being scheduled.
type Candidate struct {
	Item Item
	// InFlight: a local execution lock is held, or the issue carries
	// agent-implemented with an open PR referencing it.
	InFlight bool
	// FlightBlocked, for an in-flight item, names why delivery is not
	// progressing right now (CI red, acceptance non-progress). Empty = fine.
	FlightBlocked string
	// Ineligible, for a not-in-flight item, names why implement would not be
	// dispatched (agent-failed, ...). Empty = eligible.
	Ineligible string
}

// SelectInput is everything Select needs, as plain data.
type SelectInput struct {
	Candidates   []Candidate
	Rank         map[Key]int
	MaxInFlight  int           // <1 means DefaultMaxInFlight
	ParkAfter    time.Duration // <=0 means DefaultParkAfter
	Now          time.Time
	BlockedSince map[Key]time.Time // from the previous pass
	WasParked    map[Key]bool      // from the previous pass
	// DepOpen reports whether a dependency is still open. Unknown must be
	// answered true (fail closed).
	DepOpen func(Key) bool
}

// Entry is the scheduling verdict for one candidate, in global order.
type Entry struct {
	Key      Key
	Priority Priority
	Rank     int // position in the rank file, -1 when unranked
	State    string
	Reason   string
	Pick     bool // dispatch implement this pass
}

// Label renders the state as shown by the CLI: "blocked:<reason>" etc.
func (e Entry) Label() string {
	if e.Reason == "" {
		return e.State
	}
	return e.State + ":" + e.Reason
}

// SelectResult is the outcome of one scheduling decision.
type SelectResult struct {
	Entries      []Entry
	BlockedSince map[Key]time.Time
	Parked       map[Key]bool
	Newly        struct{ Parked, Unparked []Key }
	InFlight     int // items counted against max_in_flight
}

// Picks returns the keys to dispatch, in order.
func (r SelectResult) Picks() []Key {
	var out []Key
	for _, e := range r.Entries {
		if e.Pick {
			out = append(out, e.Key)
		}
	}
	return out
}

// Select orders the candidates and decides which to dispatch. Blocked items
// are skipped with a reason, never stalling the walk; an in-flight item stuck
// longer than ParkAfter stops counting toward MaxInFlight (at most
// MaxParkedExempt per repo).
func Select(in SelectInput) SelectResult {
	max := in.MaxInFlight
	if max < 1 {
		max = DefaultMaxInFlight
	}
	parkAfter := in.ParkAfter
	if parkAfter <= 0 {
		parkAfter = DefaultParkAfter
	}
	items := make([]Item, len(in.Candidates))
	byKey := make(map[Key]Candidate, len(in.Candidates))
	deps := make(map[Key][]Key, len(in.Candidates))
	for i, c := range in.Candidates {
		items[i] = c.Item
		byKey[c.Item.Key] = c
		deps[c.Item.Key] = c.Item.Deps
	}
	ordered := Order(items, in.Rank)
	cycles := Cycles(deps)

	res := SelectResult{BlockedSince: map[Key]time.Time{}, Parked: map[Key]bool{}}
	rankOf := func(k Key) int {
		if p, ok := in.Rank[k]; ok {
			return p
		}
		return -1
	}
	entries := make([]Entry, 0, len(ordered))
	index := map[Key]int{}

	// Pass 1: in-flight accounting and parking, in global order so the
	// exemption goes to the highest-ranked stuck items.
	counted, exempt := 0, 0
	for _, it := range ordered {
		c := byKey[it.Key]
		e := Entry{Key: it.Key, Priority: it.Priority, Rank: rankOf(it.Key)}
		if c.InFlight {
			e.State = StateInFlight
			if c.FlightBlocked == "" {
				counted++
				if in.WasParked[it.Key] {
					res.Newly.Unparked = append(res.Newly.Unparked, it.Key)
				}
			} else {
				since, ok := in.BlockedSince[it.Key]
				if !ok {
					since = in.Now
				}
				res.BlockedSince[it.Key] = since
				e.Reason = c.FlightBlocked
				if in.Now.Sub(since) >= parkAfter && exempt < MaxParkedExempt {
					exempt++
					e.State = StateParked
					res.Parked[it.Key] = true
					if !in.WasParked[it.Key] {
						res.Newly.Parked = append(res.Newly.Parked, it.Key)
					}
				} else {
					counted++
					if in.WasParked[it.Key] {
						// Still stuck but no longer exempt: counts again.
						res.Newly.Unparked = append(res.Newly.Unparked, it.Key)
					}
				}
			}
		}
		index[it.Key] = len(entries)
		entries = append(entries, e)
	}
	res.InFlight = counted

	// Pass 2: walk the order and dispatch while slots remain.
	slots := max - counted
	for _, it := range ordered {
		c := byKey[it.Key]
		if c.InFlight {
			continue
		}
		e := &entries[index[it.Key]]
		switch {
		case c.Ineligible != "":
			e.State, e.Reason = StateBlocked, c.Ineligible
		case cycles[it.Key]:
			e.State, e.Reason = StateBlocked, "dependency_cycle"
		case hasOpenDep(it.Deps, in.DepOpen):
			e.State, e.Reason = StateWaiting, "dependency"
		case slots > 0:
			e.State, e.Pick = StateEligible, true
			slots--
		default:
			e.State, e.Reason = StateEligible, "max_in_flight"
		}
	}
	res.Entries = entries
	return res
}

func hasOpenDep(deps []Key, open func(Key) bool) bool {
	for _, d := range deps {
		if open == nil || open(d) {
			return true
		}
	}
	return false
}

// String is a compact one-line form for logs.
func (e Entry) String() string {
	return fmt.Sprintf("%s %s rank=%d %s", e.Key, e.Priority, e.Rank, e.Label())
}
