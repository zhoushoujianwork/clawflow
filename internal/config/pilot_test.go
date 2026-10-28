package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPilotOverrideAliases(t *testing.T) {
	for _, input := range []string{"pilot:\n  override_merge: on\n", "pilot.override_merge: on\n", "pilot_override_merge: on\n"} {
		t.Run(strings.TrimSpace(input), func(t *testing.T) {
			var cfg Config
			if err := yaml.Unmarshal([]byte("repos:\n  o/r:\n    base_branch: main\n    local_path: /local\n    "+strings.ReplaceAll(strings.TrimSuffix(input, "\n"), "\n", "\n    ")+"\n"), &cfg); err != nil {
				t.Fatal(err)
			}
			if !cfg.Repos["o/r"].PilotOverrideMerge {
				t.Fatal("opt-in ignored")
			}
			raw, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var loaded Config
			if err := yaml.Unmarshal(raw, &loaded); err != nil {
				t.Fatal(err)
			}
			if !loaded.Repos["o/r"].PilotOverrideMerge || loaded.Repos["o/r"].LocalPath != "/local" {
				t.Fatal("round trip lost settings")
			}
			var synced syncableRepo
			if err := yaml.Unmarshal([]byte(input), &synced); err != nil {
				t.Fatal(err)
			}
			if !fromSyncable(synced, "").PilotOverrideMerge {
				t.Fatal("sync opt-in ignored")
			}
		})
	}
	for _, input := range []string{"", "pilot: {}", "pilot:\n  override_merge: off", "pilot.override_merge: off", "pilot_override_merge: off"} {
		var repo Repo
		if err := yaml.Unmarshal([]byte(input), &repo); err != nil {
			t.Fatal(err)
		}
		if repo.PilotOverrideMerge {
			t.Fatalf("unexpected opt-in: %s", input)
		}
	}
	for _, input := range []string{"pilot: {override_merge: invalid}", "pilot: {override_merge: off}\npilot_override_merge: on", "pilot.override_merge: off\npilot_override_merge: on"} {
		var repo Repo
		if err := yaml.Unmarshal([]byte(input), &repo); err == nil {
			t.Fatalf("accepted ambiguous/invalid setting: %s", input)
		}
	}
}
