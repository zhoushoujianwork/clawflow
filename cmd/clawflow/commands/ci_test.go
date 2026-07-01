package commands

import "testing"

func TestStripANSI(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\x1b[0Krunning\x1b[0;m", "running"},
		{"\x1b[32;1mok\x1b[0m\n", "ok\n"},
		{"plain text", "plain text"},
		{"", ""},
	}
	for _, c := range cases {
		if got := stripANSI(c.in); got != c.want {
			t.Errorf("stripANSI(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStripSections(t *testing.T) {
	// Marker + CR followed by real content on the same physical line.
	in := "section_start:1782893292:prepare_script\rWaiting for pod\nsection_end:1782893298:prepare_script\rDone\n"
	want := "Waiting for pod\nDone\n"
	if got := stripSections(in); got != want {
		t.Errorf("stripSections = %q, want %q", got, want)
	}
	// Collapsed-section option syntax must also be consumed.
	if got := stripSections("section_start:1:step_script[collapsed=true]\rrun\n"); got != "run\n" {
		t.Errorf("stripSections collapsed = %q", got)
	}
	// Non-marker text is untouched.
	if got := stripSections("no markers here\n"); got != "no markers here\n" {
		t.Errorf("stripSections passthrough = %q", got)
	}
}

func TestShortSHA(t *testing.T) {
	if got := shortSHA("3db92cf9abcdef"); got != "3db92cf9" {
		t.Errorf("shortSHA long = %q, want 3db92cf9", got)
	}
	if got := shortSHA("abc"); got != "abc" {
		t.Errorf("shortSHA short = %q, want abc", got)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("main", 24); got != "main" {
		t.Errorf("truncate no-op = %q", got)
	}
	if got := truncate("release/v0.0.0-smoke-longbranch", 12); got != "release/v0.…" {
		t.Errorf("truncate = %q", got)
	}
}
