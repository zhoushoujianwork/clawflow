package pipeline

import "testing"

func TestDecidedSection(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{"## Decided\nShip", true},
		{"### decided ###\nShip", true},
		{"Mention Decided here", false},
		{"## Undecided", false},
		{"```md\n## Decided\n```", false},
		{"", false},
	} {
		if got := DecidedSection(tc.body); got == nil || *got != tc.want {
			t.Errorf("%q: %v", tc.body, got)
		}
	}
}
