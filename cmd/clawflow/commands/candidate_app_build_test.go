package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The native build script must refuse every candidate configuration that could
// overwrite the stable app artifact or reuse the stable bundle identifier. All
// cases exit before swift is invoked, so nothing is built or installed.
func TestNativeBuildScriptRefusesUnsafeCandidateTargets(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "macos", "build-app.sh")
	contents, err := os.ReadFile(source)
	if err != nil {
		t.Skip("build script unavailable")
	}
	// A developer may already have a stable build in the checkout. Exercise
	// refusal in a disposable repository layout instead of inspecting that build.
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "macos"), 0700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "macos", "build-app.sh")
	if err := os.WriteFile(script, contents, 0700); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		env  []string
		want string
	}{
		{"short id", []string{"CLAWFLOW_CANDIDATE_ID=zz", "CLAWFLOW_CANDIDATE_OUT=/tmp/x"}, "12 lowercase hex"},
		{"uppercase id", []string{"CLAWFLOW_CANDIDATE_ID=0123456789AB", "CLAWFLOW_CANDIDATE_OUT=/tmp/x"}, "12 lowercase hex"},
		{"missing out", []string{"CLAWFLOW_CANDIDATE_ID=0123456789ab"}, "CLAWFLOW_CANDIDATE_OUT is required"},
		{"relative out", []string{"CLAWFLOW_CANDIDATE_ID=0123456789ab", "CLAWFLOW_CANDIDATE_OUT=relative"}, "must be absolute"},
		{"stable dist", []string{"CLAWFLOW_CANDIDATE_ID=0123456789ab", "CLAWFLOW_CANDIDATE_OUT=" + filepath.Join(root, "macos", "dist")}, "overlaps"},
		{"stable dist child", []string{"CLAWFLOW_CANDIDATE_ID=0123456789ab", "CLAWFLOW_CANDIDATE_OUT=" + filepath.Join(root, "macos", "dist", "x")}, "overlaps"},
		{"applications", []string{"CLAWFLOW_CANDIDATE_ID=0123456789ab", "CLAWFLOW_CANDIDATE_OUT=/Applications"}, "overlaps"},
	}
	for _, c := range cases {
		cmd := exec.Command("sh", script)
		cmd.Env = append(os.Environ(), c.env...)
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), c.want) {
			t.Errorf("%s: err=%v out=%q", c.name, err, out)
		}
	}
	// Refusal must happen before any directory is created.
	if _, err := os.Stat(filepath.Join(root, "macos", "dist")); err == nil {
		t.Fatal("a refused candidate build created the stable dist directory")
	}
	// Refusal must also preserve an existing stable artifact.
	dist := filepath.Join(root, "macos", "dist")
	if err := os.Mkdir(dist, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dist, "existing-build")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", script)
	cmd.Env = append(os.Environ(), "CLAWFLOW_CANDIDATE_ID=0123456789ab", "CLAWFLOW_CANDIDATE_OUT="+dist)
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "overlaps") {
		t.Fatalf("existing dist was not refused: %v %s", err, out)
	}
	if contents, err := os.ReadFile(marker); err != nil || string(contents) != "keep" {
		t.Fatalf("existing build changed: %q %v", contents, err)
	}
}
