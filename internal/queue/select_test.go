package queue

import (
	"reflect"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func cand(key string, prio Priority, day int) Candidate {
	return Candidate{Item: Item{Key: k(key), Priority: prio, CreatedAt: time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)}}
}

func picks(r SelectResult) []string {
	var out []string
	for _, key := range r.Picks() {
		out = append(out, key.String())
	}
	return out
}

func stateOf(r SelectResult, key string) string {
	for _, e := range r.Entries {
		if e.Key == k(key) {
			return e.Label()
		}
	}
	return "missing"
}

func TestSelectPerRepoCap(t *testing.T) {
	in := SelectInput{Now: t0, MaxInFlight: 2, Candidates: []Candidate{
		cand("o/r#1", 2, 1), cand("o/r#2", 2, 2), cand("o/r#3", 2, 3),
	}}
	r := Select(in)
	if got := picks(r); !reflect.DeepEqual(got, []string{"o/r#1", "o/r#2"}) {
		t.Fatalf("picks = %v", got)
	}
	if stateOf(r, "o/r#3") != "eligible:max_in_flight" {
		t.Fatalf("third = %s", stateOf(r, "o/r#3"))
	}
	in.Candidates[0].InFlight = true
	r = Select(in)
	if got := picks(r); !reflect.DeepEqual(got, []string{"o/r#2"}) || r.InFlight != 1 {
		t.Fatalf("picks = %v inflight=%d", got, r.InFlight)
	}
	in.MaxInFlight = 0 // default cap is 2
	in.Candidates[0].InFlight = false
	if got := picks(Select(in)); len(got) != 2 {
		t.Fatalf("default cap: %v", got)
	}
}

func TestSelectOrderAcrossPriority(t *testing.T) {
	in := SelectInput{Now: t0, MaxInFlight: 1, Candidates: []Candidate{
		cand("o/r#1", 2, 1), cand("o/r#2", 0, 5), cand("o/r#3", 2, 2),
	}, Rank: Positions([]Key{k("o/r#3")})}
	if got := picks(Select(in)); !reflect.DeepEqual(got, []string{"o/r#2"}) {
		t.Fatalf("P0 first: %v", got)
	}
	in.Candidates[1].Ineligible = "agent-failed"
	r := Select(in)
	if got := picks(r); !reflect.DeepEqual(got, []string{"o/r#3"}) {
		t.Fatalf("rank beats age: %v", got)
	}
	if stateOf(r, "o/r#2") != "blocked:agent-failed" {
		t.Fatalf("blocked = %s", stateOf(r, "o/r#2"))
	}
}

func TestSelectBlockedDoesNotStall(t *testing.T) {
	in := SelectInput{Now: t0, MaxInFlight: 1, Candidates: []Candidate{cand("o/r#1", 2, 1), cand("o/r#2", 2, 2)}}
	in.Candidates[0].Ineligible = "agent-failed"
	if got := picks(Select(in)); !reflect.DeepEqual(got, []string{"o/r#2"}) {
		t.Fatalf("picks = %v", got)
	}
}

func TestSelectDependencies(t *testing.T) {
	open := map[Key]bool{k("o/r#1"): true}
	in := SelectInput{Now: t0, MaxInFlight: 5, DepOpen: func(d Key) bool { return open[d] }, Candidates: []Candidate{
		cand("o/r#1", 2, 1), cand("o/r#2", 2, 2), cand("o/r#3", 2, 3), cand("o/r#4", 2, 4), cand("o/r#5", 2, 5),
	}}
	in.Candidates[1].Item.Deps = []Key{k("o/r#1")}
	in.Candidates[2].Item.Deps = []Key{k("o/r#4")} // cycle 3<->4
	in.Candidates[3].Item.Deps = []Key{k("o/r#3")}
	in.Candidates[4].Item.Deps = []Key{k("o/r#9")} // closed dep: ok
	r := Select(in)
	if got := picks(r); !reflect.DeepEqual(got, []string{"o/r#1", "o/r#5"}) {
		t.Fatalf("picks = %v", got)
	}
	if stateOf(r, "o/r#2") != "waiting:dependency" || stateOf(r, "o/r#3") != "blocked:dependency_cycle" || stateOf(r, "o/r#4") != "blocked:dependency_cycle" {
		t.Fatalf("states: %s %s %s", stateOf(r, "o/r#2"), stateOf(r, "o/r#3"), stateOf(r, "o/r#4"))
	}
	in.DepOpen = nil // unknown dependency state fails closed
	if got := picks(Select(in)); !reflect.DeepEqual(got, []string{"o/r#1"}) {
		t.Fatalf("nil DepOpen must block dependents: %v", got)
	}
}

func TestSelectParkAndUnpark(t *testing.T) {
	mk := func() SelectInput {
		in := SelectInput{Now: t0, MaxInFlight: 2, ParkAfter: 45 * time.Minute, Candidates: []Candidate{
			cand("o/r#1", 2, 1), cand("o/r#2", 2, 2), cand("o/r#3", 2, 3), cand("o/r#4", 2, 4), cand("o/r#5", 2, 5),
		}}
		in.Candidates[0].InFlight, in.Candidates[0].FlightBlocked = true, "ci_failed"
		in.Candidates[1].InFlight, in.Candidates[1].FlightBlocked = true, "no_verdict"
		return in
	}
	r := Select(mk())
	if len(picks(r)) != 0 || r.InFlight != 2 || len(r.Parked) != 0 {
		t.Fatalf("fresh block: picks=%v inflight=%d parked=%v", picks(r), r.InFlight, r.Parked)
	}
	if !r.BlockedSince[k("o/r#1")].Equal(t0) {
		t.Fatal("blocked_since must be recorded")
	}
	in := mk()
	in.BlockedSince = r.BlockedSince
	in.Now = t0.Add(46 * time.Minute)
	r = Select(in)
	if got := picks(r); !reflect.DeepEqual(got, []string{"o/r#3", "o/r#4"}) {
		t.Fatalf("picks after park = %v", got)
	}
	if stateOf(r, "o/r#1") != "parked:ci_failed" || len(r.Newly.Parked) != 2 {
		t.Fatalf("parked: %s %v", stateOf(r, "o/r#1"), r.Newly.Parked)
	}
	in2 := mk()
	in2.Candidates[0].FlightBlocked = ""
	in2.WasParked = r.Parked
	in2.BlockedSince = r.BlockedSince
	in2.Now = t0.Add(60 * time.Minute)
	r2 := Select(in2)
	if !reflect.DeepEqual(r2.Newly.Unparked, []Key{k("o/r#1")}) || stateOf(r2, "o/r#1") != "in_flight" {
		t.Fatalf("unpark: %v %s", r2.Newly.Unparked, stateOf(r2, "o/r#1"))
	}
	if _, kept := r2.BlockedSince[k("o/r#1")]; kept {
		t.Fatal("blocked_since must clear when unblocked")
	}
}

func TestSelectParkExemptionCapped(t *testing.T) {
	in := SelectInput{Now: t0.Add(time.Hour), MaxInFlight: 3, Candidates: []Candidate{
		cand("o/r#1", 2, 1), cand("o/r#2", 2, 2), cand("o/r#3", 2, 3), cand("o/r#4", 2, 4),
	}}
	since := map[Key]time.Time{}
	for i := 0; i < 3; i++ {
		in.Candidates[i].InFlight, in.Candidates[i].FlightBlocked = true, "ci_failed"
		since[in.Candidates[i].Item.Key] = t0
	}
	in.BlockedSince = since
	r := Select(in)
	if len(r.Parked) != MaxParkedExempt || r.InFlight != 1 {
		t.Fatalf("parked=%d inflight=%d", len(r.Parked), r.InFlight)
	}
	if got := picks(r); !reflect.DeepEqual(got, []string{"o/r#4"}) {
		t.Fatalf("picks = %v", got)
	}
}

func TestStateRoundTripAndWarnings(t *testing.T) {
	tempHome(t)
	st, err := LoadState("group/sub/p")
	if err != nil || st.Repo != "group/sub/p" {
		t.Fatalf("empty: %+v %v", st, err)
	}
	if !st.Warn(3, "bad label") || st.Warn(3, "bad label") || !st.Warn(3, "other") {
		t.Fatal("Warn must report each (issue, message) once")
	}
	st.Skipped = 3
	st.BlockedSince = map[int]time.Time{7: t0}
	st.Parked = []int{7}
	if err := SaveState(st); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState("group/sub/p")
	if err != nil || got.Skipped != 3 || !got.ParkedKeys()[k("group/sub/p#7")] || !got.BlockedSinceKeys()[k("group/sub/p#7")].Equal(t0) || got.Warn(3, "bad label") {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	got.PruneWarned(map[int]bool{})
	if len(got.Warned) != 0 {
		t.Fatal("warnings of closed issues must be pruned")
	}
	if all := ListStates(); len(all) != 1 || all[0].Repo != "group/sub/p" {
		t.Fatalf("ListStates = %+v", all)
	}
}

func TestCoordinationWaitingDoesNotConsumeCapacity(t *testing.T) {
	in := SelectInput{Now: time.Now(), MaxInFlight: 1, Candidates: []Candidate{
		{Item: Item{Key: Key{Repo: "o/r", Number: 1}}, Ineligible: "coordination_waiting — active task"},
		{Item: Item{Key: Key{Repo: "o/r", Number: 2}}},
	}}
	r := Select(in)
	if len(r.Picks()) != 1 || r.Picks()[0].Number != 2 {
		t.Fatalf("waiting starved compatible work: %+v", r)
	}
	if r.Entries[0].State != StateWaiting {
		t.Fatalf("not presented as waiting: %+v", r.Entries[0])
	}
}
