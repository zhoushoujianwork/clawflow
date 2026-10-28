package acceptance

import "testing"

func TestFindingLocations(t *testing.T) {
	for _, l := range []FindingLocation{{"src/main.go", 1, "head"}, {"目录/文件.go", 42, "head"}} {
		if err := l.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range []FindingLocation{{"", 1, "head"}, {"../a", 1, "head"}, {"/a", 1, "head"}, {"a/../b", 1, "head"}, {"a\\b", 1, "head"}, {"C:/a", 1, "head"}, {"a", 0, "head"}, {"a", 1, "base"}} {
		if err := l.Validate(); err == nil {
			t.Errorf("accepted %+v", l)
		}
		if _, err := validateFindings([]Finding{{Kind: "advisory", Summary: "test", Locations: []FindingLocation{l}}}); err == nil {
			t.Errorf("finding accepted %+v", l)
		}
	}
}
