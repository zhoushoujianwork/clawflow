package queue

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func k(s string) Key {
	key, err := ParseKey(s)
	if err != nil {
		panic(err)
	}
	return key
}

func TestParseKey(t *testing.T) {
	for _, ok := range []string{"o/r#1", "group/sub/project#42", "a.b/c_d-e#10"} {
		got, err := ParseKey(ok)
		if err != nil || got.String() != ok {
			t.Errorf("ParseKey(%q) = %v, %v", ok, got, err)
		}
	}
	if got, _ := ParseKey("group/sub/project#42"); got.Repo != "group/sub/project" || got.Number != 42 {
		t.Errorf("gitlab key = %+v", got)
	}
	for _, bad := range []string{"", "o/r", "o/r#0", "o/r#01", "o#1", "o/r#x", " o/r#1", "o/r#1 ", "o/r#-1", "#1"} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q) should fail", bad)
		}
	}
}

func TestPriorityFromLabels(t *testing.T) {
	cases := []struct {
		labels []string
		want   Priority
		warns  int
	}{
		{nil, 2, 0},
		{[]string{"bug"}, 2, 0},
		{[]string{"P0"}, 0, 0},
		{[]string{"P3"}, 3, 0},
		{[]string{"P3", "P1", "P2"}, 1, 0},
		{[]string{"p1"}, 2, 1},
		{[]string{"priority:1"}, 2, 1},
		{[]string{"p1", "P3"}, 3, 1},
		{[]string{"P4"}, 2, 1},
	}
	for _, c := range cases {
		got, w := PriorityFromLabels(c.labels)
		if got != c.want || len(w) != c.warns {
			t.Errorf("%v = %v,%v want %v,%d warnings", c.labels, got, w, c.want, c.warns)
		}
	}
}

func TestParseDependsOn(t *testing.T) {
	deps, w := ParseDependsOn("intro\nDepends on: #12, o/other#34\nmore", "o/r")
	if len(w) != 0 || !reflect.DeepEqual(deps, []Key{k("o/r#12"), k("o/other#34")}) {
		t.Fatalf("deps=%v warns=%v", deps, w)
	}
	deps, w = ParseDependsOn("Depends on: #1, junk, #2, o/r#2, #x", "o/r")
	if !reflect.DeepEqual(deps, []Key{k("o/r#1"), k("o/r#2")}) || len(w) != 2 {
		t.Fatalf("partial: deps=%v warns=%v", deps, w)
	}
	for _, body := range []string{"depends on: #1", "  Depends on: #1", "see Depends on: #1", ""} {
		if d, w := ParseDependsOn(body, "o/r"); d != nil || w != nil {
			t.Errorf("%q should be ignored, got %v %v", body, d, w)
		}
	}
	var toks []string
	for i := 1; i <= 12; i++ {
		toks = append(toks, fmt.Sprintf("#%d", i))
	}
	deps, w = ParseDependsOn("Depends on: "+strings.Join(toks, ", "), "o/r")
	if len(deps) != MaxDependencies || len(w) != 2 || deps[9].Number != 10 {
		t.Fatalf("cap: %d deps, warns=%v", len(deps), w)
	}
}

func TestParseDependsOnMultipleLines(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		want  []Key
		warns []string
	}{
		{
			name: "union",
			body: "Depends on: #1\nDepends on: #2, owner/repo#3",
			want: []Key{k("o/r#1"), k("o/r#2"), k("owner/repo#3")},
		},
		{
			name: "deduplicate in first-seen order",
			body: "Depends on: #2, owner/repo#3\r\ntext\nDepends on: o/r#2, #1, owner/repo#3\nDepends on: #1, #4",
			want: []Key{k("o/r#2"), k("owner/repo#3"), k("o/r#1"), k("o/r#4")},
		},
		{
			name:  "empty first line and invalid later tokens",
			body:  "Depends on: \nDepends on: #1, junk\nDepends on: #x, #2",
			want:  []Key{k("o/r#1"), k("o/r#2")},
			warns: []string{`dependency "junk" ignored: not #N or owner/repo#N`, `dependency "#x" ignored: not #N or owner/repo#N`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, warns := ParseDependsOn(tc.body, "o/r")
			if !reflect.DeepEqual(deps, tc.want) || !reflect.DeepEqual(warns, tc.warns) {
				t.Fatalf("deps=%v warns=%v; want %v %v", deps, warns, tc.want, tc.warns)
			}
		})
	}
}

func TestParseDependsOnCapAcrossLines(t *testing.T) {
	var lines []string
	var want []Key
	for i := 1; i <= MaxDependencies+2; i++ {
		lines = append(lines, fmt.Sprintf("Depends on: #%d, o/r#1", i))
		if i <= MaxDependencies {
			want = append(want, Key{Repo: "o/r", Number: i})
		}
	}
	deps, warns := ParseDependsOn(strings.Join(lines, "\n"), "o/r")
	wantWarns := []string{
		fmt.Sprintf(`more than %d dependencies; extra "#%d" ignored`, MaxDependencies, MaxDependencies+1),
		fmt.Sprintf(`more than %d dependencies; extra "#%d" ignored`, MaxDependencies, MaxDependencies+2),
	}
	if !reflect.DeepEqual(deps, want) || !reflect.DeepEqual(warns, wantWarns) {
		t.Fatalf("deps=%v warns=%v; want %v %v", deps, warns, want, wantWarns)
	}
}

func TestOrder(t *testing.T) {
	items := []Item{
		{Key: k("o/a#5"), Priority: 2, CreatedAt: "2026-01-05T00:00:00Z"},
		{Key: k("o/a#1"), Priority: 2, CreatedAt: "2026-01-01T00:00:00Z"},
		{Key: k("o/b#9"), Priority: 1, CreatedAt: "2026-01-09T00:00:00Z"},
		{Key: k("o/a#7"), Priority: 2, CreatedAt: "2026-01-07T00:00:00Z"},
		{Key: k("o/a#2"), Priority: 2, CreatedAt: "2026-01-01T00:00:00Z"},
		{Key: k("o/c#3"), Priority: 0, CreatedAt: ""},
		{Key: k("o/a#8"), Priority: 3, CreatedAt: "2026-01-01T00:00:00Z"},
	}
	rank := Positions([]Key{k("o/a#7"), k("o/a#5"), k("o/c#3")})
	var got []string
	for _, it := range Order(items, rank) {
		got = append(got, it.Key.String())
	}
	want := []string{"o/c#3", "o/b#9", "o/a#7", "o/a#5", "o/a#1", "o/a#2", "o/a#8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if items[0].Key != k("o/a#5") {
		t.Fatal("input must not be mutated")
	}
}

func TestCycles(t *testing.T) {
	a, b, c, d := k("o/r#1"), k("o/r#2"), k("o/r#3"), k("o/r#4")
	got := Cycles(map[Key][]Key{a: {b}, b: {a}, c: {a}, d: {d}})
	if !got[a] || !got[b] || !got[d] || got[c] {
		t.Fatalf("cycles = %v", got)
	}
	if len(Cycles(map[Key][]Key{a: {b}, b: {c}, c: nil})) != 0 {
		t.Fatal("chain is not a cycle")
	}
	// Edges to keys outside the candidate set never form a cycle.
	if len(Cycles(map[Key][]Key{a: {k("o/x#9")}})) != 0 {
		t.Fatal("external dep is not a cycle")
	}
}

func tempHome(t *testing.T) {
	t.Helper()
	t.Setenv("CLAWFLOW_STATE_DIR", "")
	t.Setenv("HOME", t.TempDir())
}

func TestRankRoundTripPruneDuplicates(t *testing.T) {
	tempHome(t)
	if o, err := LoadRank(); err != nil || len(o) != 0 {
		t.Fatalf("missing file: %v %v", o, err)
	}
	order, err := UpdateRank(func([]Key) ([]Key, error) {
		return []Key{k("o/r#3"), k("o/r#1"), k("o/r#3"), k("o/r#2")}, nil
	}, nil)
	if err != nil || !reflect.DeepEqual(order, []Key{k("o/r#3"), k("o/r#1"), k("o/r#2")}) {
		t.Fatalf("dedupe: %v %v", order, err)
	}
	loaded, _ := LoadRank()
	if !reflect.DeepEqual(loaded, order) {
		t.Fatalf("round trip: %v", loaded)
	}
	live := func(key Key) bool { return key != k("o/r#1") }
	order, err = UpdateRank(nil, live)
	if err != nil || !reflect.DeepEqual(order, []Key{k("o/r#3"), k("o/r#2")}) {
		t.Fatalf("prune: %v %v", order, err)
	}
	raw, _ := os.ReadFile(RankPath())
	if !strings.Contains(string(raw), `"version": 1`) {
		t.Fatalf("file: %s", raw)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(strings.TrimSuffix(RankPath(), "/queue.json"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("leftover %s", e.Name())
		}
	}
}

func TestRankMutateErrorWritesNothing(t *testing.T) {
	tempHome(t)
	_, err := UpdateRank(func([]Key) ([]Key, error) { return nil, os.ErrInvalid }, nil)
	if err == nil {
		t.Fatal("want error")
	}
	if _, statErr := os.Stat(RankPath()); statErr == nil {
		t.Fatal("file must not be written")
	}
}

func TestLoadRankTolerance(t *testing.T) {
	tempHome(t)
	_ = os.MkdirAll(strings.TrimSuffix(RankPath(), "/queue.json"), 0o755)
	_ = os.WriteFile(RankPath(), []byte(`{"version":1,"order":["o/r#2","bad","o/r#1","o/r#2"]}`), 0o600)
	o, err := LoadRank()
	if err != nil || !reflect.DeepEqual(o, []Key{k("o/r#2"), k("o/r#1")}) {
		t.Fatalf("%v %v", o, err)
	}
	_ = os.WriteFile(RankPath(), []byte(`{"version":2,"order":[]}`), 0o600)
	if _, err := LoadRank(); err == nil {
		t.Fatal("future version must be rejected")
	}
	_ = os.WriteFile(RankPath(), []byte(`not json`), 0o600)
	if _, err := LoadRank(); err == nil {
		t.Fatal("corrupt file must error")
	}
}

func TestMove(t *testing.T) {
	a, b, c, d := k("o/r#1"), k("o/r#2"), k("o/r#3"), k("o/r#4")
	base := []Key{a, b, c}
	cases := []struct {
		name string
		key  Key
		p    Placement
		want []Key
	}{
		{"top", c, Placement{Top: true}, []Key{c, a, b}},
		{"bottom", a, Placement{Bottom: true}, []Key{b, c, a}},
		{"before", c, Placement{Before: &a}, []Key{c, a, b}},
		{"after", a, Placement{After: &b}, []Key{b, a, c}},
		{"new top", d, Placement{Top: true}, []Key{d, a, b, c}},
		{"unranked anchor", a, Placement{After: &d}, []Key{b, c, d, a}},
	}
	for _, tc := range cases {
		got, err := Move(base, tc.key, tc.p)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v %v want %v", tc.name, got, err, tc.want)
		}
	}
	if _, err := Move(base, a, Placement{}); err == nil {
		t.Error("no placement must fail")
	}
	if _, err := Move(base, a, Placement{Top: true, Bottom: true}); err == nil {
		t.Error("two placements must fail")
	}
	if _, err := Move(base, a, Placement{Before: &a}); err == nil {
		t.Error("self anchor must fail")
	}
}
