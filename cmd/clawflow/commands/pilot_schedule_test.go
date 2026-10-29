package commands

import "testing"

func TestPilotScheduleCLI(t *testing.T) {
	cmd := NewPilotCmd()
	for _, name := range []string{"add", "list", "show", "enable", "disable", "remove"} {
		found, _, err := cmd.Find([]string{"schedule", name})
		if err != nil || found.Name() != name {
			t.Fatalf("%s: %v", name, err)
		}
		if name == "add" {
			for _, flag := range []string{"cron", "tz", "prompt", "ops", "timeout"} {
				if found.Flags().Lookup(flag) == nil {
					t.Fatalf("missing %s", flag)
				}
			}
		}
	}
}
