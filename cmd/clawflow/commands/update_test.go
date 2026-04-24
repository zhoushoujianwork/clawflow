package commands

import "testing"

// IsNewerVersion is load-bearing for `clawflow update`'s "skip if already at
// latest" short-circuit — one wrong case and users either re-download the
// same binary repeatedly or get stuck on an old version. These tests lock
// the semantics in.
func TestIsNewerVersion(t *testing.T) {
	cases := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		// Happy path — a real upgrade
		{"older minor", "v0.37.0", "v0.38.0", true},
		{"older patch", "v0.38.0", "v0.38.1", true},
		{"older major", "v0.38.2", "v1.0.0", true},

		// Equal — do not upgrade
		{"exact match", "v0.38.1", "v0.38.1", false},
		{"exact match no-prefix", "0.38.1", "0.38.1", false},

		// Current is ahead — do not downgrade
		{"ahead minor", "v0.38.0", "v0.37.0", false},
		{"ahead patch", "v0.38.1", "v0.38.0", false},
		{"ahead major", "v1.0.0", "v0.99.0", false},

		// Dev/prerelease builds — git-describe format strips dash suffix
		// so they compare by the base tag.
		{"git describe same tag", "v0.38.1-5-gabc123", "v0.38.1", false},
		{"git describe older tag", "v0.37.0-5-gabc123", "v0.38.0", true},

		// Dev build (unparseable) → parses to 0.0.0, so any real tag is newer
		{"dev build", "dev", "v0.38.1", true},
		{"empty current", "", "v0.38.1", true},

		// Both unparseable → equal → not newer
		{"both dev", "dev", "dev", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsNewerVersion(c.current, c.latest)
			if got != c.want {
				t.Errorf("IsNewerVersion(%q, %q) = %v, want %v", c.current, c.latest, got, c.want)
			}
		})
	}
}
