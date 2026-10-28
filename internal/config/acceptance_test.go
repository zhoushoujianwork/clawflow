package config

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAcceptancePolicyDefaultsAndRoundTrip(t *testing.T) {
	var old Repo
	if err := yaml.Unmarshal([]byte("base_branch: main\n"), &old); err != nil || old.AcceptanceRequired {
		t.Fatalf("legacy configuration enrolled: %+v %v", old, err)
	}
	for _, enabled := range []bool{false, true} {
		r := Repo{AcceptanceRequired: enabled}
		y, err := yaml.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var roundTrip Repo
		if err := yaml.Unmarshal(y, &roundTrip); err != nil || roundTrip.AcceptanceRequired != enabled || roundTrip.Enabled || roundTrip.AutoMerge {
			t.Fatalf("unexpected YAML policy: %+v %v", roundTrip, err)
		}
		j, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(j), `"acceptance_required"`) != enabled || strings.Contains(string(y), "acceptance_required") != enabled {
			t.Fatalf("policy omission mismatch: %s %s", y, j)
		}
	}
}

func TestAcceptanceOverlapDefaultsAndRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  bool
	}{
		{"", false}, {"ci_required: true", true},
		{"ci_required: true\nacceptance_overlap_ci: off", false},
		{"ci_required: true\nacceptance_overlap_ci: on", true},
	} {
		var r, loaded Repo
		if err := yaml.Unmarshal([]byte(tc.input), &r); err != nil {
			t.Fatal(err)
		}
		data, err := yaml.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(data, &loaded); err != nil {
			t.Fatal(err)
		}
		if loaded.AcceptanceOverlapsCI() != tc.want {
			t.Fatalf("%q: %+v", tc.input, loaded)
		}
	}
}
