package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// resolveBinaryPath underpins the issue #230 fix: `clawflow update` must
// replace the binary at its REAL location (following symlinks), not a symlink
// pointing at it, so the update stays on PATH and doesn't leave a stale copy.
func TestResolveBinaryPath(t *testing.T) {
	dir := t.TempDir()

	real := filepath.Join(dir, "clawflow")
	if err := os.WriteFile(real, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The canonical path of the real file — on macOS the temp dir itself sits
	// under a /tmp -> /private/tmp symlink, so normalize via EvalSymlinks
	// rather than comparing against the raw path.
	want, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}

	// A plain regular file resolves to its canonical self.
	if got := resolveBinaryPath(real); got != want {
		t.Errorf("resolveBinaryPath(regular) = %q, want %q", got, want)
	}

	// A symlink resolves to its target — this is the /usr/local/bin/clawflow
	// -> ~/.clawflow/bin/clawflow case we must follow on update.
	link := filepath.Join(dir, "clawflow-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if resolved := resolveBinaryPath(link); resolved != want {
		t.Errorf("resolveBinaryPath(symlink) = %q, want %q", resolved, want)
	}

	// An unresolvable (nonexistent) path falls back to the raw input rather
	// than erroring out.
	missing := filepath.Join(dir, "does-not-exist")
	if resolved := resolveBinaryPath(missing); resolved != missing {
		t.Errorf("resolveBinaryPath(missing) = %q, want %q", resolved, missing)
	}
}

// permissionDeniedErr must surface the destination path and an actionable
// sudo hint so users who installed into /usr/local/bin know how to recover.
func TestPermissionDeniedErr(t *testing.T) {
	err := permissionDeniedErr("/usr/local/bin/clawflow", os.ErrPermission)
	msg := err.Error()
	if !strings.Contains(msg, "/usr/local/bin/clawflow") {
		t.Errorf("error message missing destination path: %q", msg)
	}
	if !strings.Contains(msg, "sudo clawflow update") {
		t.Errorf("error message missing sudo hint: %q", msg)
	}
}
