package queue

import "testing"

func TestKeyKeepsSiteQualifier(t *testing.T) {
	k, err := ParseKey("b:team/tools/service#7")
	if err != nil || k.Repo != "b:team/tools/service" || k.Number != 7 {
		t.Fatalf("ParseKey = %+v, %v", k, err)
	}
	if k.String() != "b:team/tools/service#7" {
		t.Errorf("String = %q", k.String())
	}
	legacy, err := ParseKey("team/tools/service#7")
	if err != nil || legacy.Repo != "team/tools/service" {
		t.Fatalf("legacy key: %+v, %v", legacy, err)
	}
	if k == legacy {
		t.Error("same number on a site and on the legacy key compare equal")
	}
	for _, bad := range []string{"B:team/service#1", ":team/service#1", "b:#1", "b:team/service#0"} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q) accepted", bad)
		}
	}
}

func TestStatePathsAreDistinctPerSite(t *testing.T) {
	t.Setenv("CLAWFLOW_STATE_DIR", t.TempDir())
	a, b, legacy := "a:team/tools/service", "b:team/tools/service", "team/tools/service"
	if StatePath(a) == StatePath(b) || StatePath(a) == StatePath(legacy) {
		t.Fatal("queue state shared across sites")
	}
	if err := SaveState(RepoState{Repo: a, Entries: []EntryRecord{{Number: 1, State: "queued"}}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(RepoState{Repo: b, Entries: []EntryRecord{{Number: 1, State: "running"}}}); err != nil {
		t.Fatal(err)
	}
	sa, _ := LoadState(a)
	sb, _ := LoadState(b)
	if sa.Entries[0].State != "queued" || sb.Entries[0].State != "running" {
		t.Fatalf("states mixed: %+v / %+v", sa.Entries, sb.Entries)
	}
	if got, _ := LoadState(legacy); len(got.Entries) != 0 {
		t.Errorf("legacy key read a site's state: %+v", got.Entries)
	}
}
