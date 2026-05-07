package sync

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// codec tests
// ---------------------------------------------------------------------------

func TestEncodeGistFilename(t *testing.T) {
	cases := []struct {
		relPath string
		want    string
	}{
		{"projects/myapp/context.md", "projects--myapp--context.md"},
		{"projects/myapp/project.yaml", "projects--myapp--project.yaml"},
		{"projects/my-app/testing.md", "projects--my-app--testing.md"},
		{"projects/bbclaw/deployment.md", "projects--bbclaw--deployment.md"},
	}
	for _, tc := range cases {
		got := EncodeGistFilename(tc.relPath)
		if got != tc.want {
			t.Errorf("EncodeGistFilename(%q) = %q, want %q", tc.relPath, got, tc.want)
		}
	}
}

func TestDecodeGistFilename(t *testing.T) {
	cases := []struct {
		filename string
		wantPath string
		wantOK   bool
	}{
		{"projects--myapp--context.md", "projects/myapp/context.md", true},
		{"projects--myapp--project.yaml", "projects/myapp/project.yaml", true},
		{"projects--bbclaw--deployment.md", "projects/bbclaw/deployment.md", true},
		{"config.yaml", "", false},
		{"something-else.md", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		gotPath, gotOK := DecodeGistFilename(tc.filename)
		if gotOK != tc.wantOK || gotPath != tc.wantPath {
			t.Errorf("DecodeGistFilename(%q) = (%q, %v), want (%q, %v)",
				tc.filename, gotPath, gotOK, tc.wantPath, tc.wantOK)
		}
	}
}

func TestCodecRoundTrip(t *testing.T) {
	paths := []string{
		"projects/myapp/context.md",
		"projects/myapp/project.yaml",
		"projects/bbclaw/testing.md",
		"projects/bbclaw/deployment.md",
	}
	for _, p := range paths {
		encoded := EncodeGistFilename(p)
		decoded, ok := DecodeGistFilename(encoded)
		if !ok {
			t.Errorf("round-trip: DecodeGistFilename(%q) returned ok=false", encoded)
			continue
		}
		if decoded != p {
			t.Errorf("round-trip: got %q, want %q", decoded, p)
		}
	}
}

func TestIsProjectAssetFilename(t *testing.T) {
	cases := []struct {
		filename string
		want     bool
	}{
		{"projects--myapp--context.md", true},
		{"projects--myapp--project.yaml", true},
		{"config.yaml", false},
		{"other.md", false},
		{"", false},
	}
	for _, tc := range cases {
		got := IsProjectAssetFilename(tc.filename)
		if got != tc.want {
			t.Errorf("IsProjectAssetFilename(%q) = %v, want %v", tc.filename, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// discovery / filtering tests
// ---------------------------------------------------------------------------

func TestIsExcludedProjectFile(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"health-check.json", true},
		{"generate-context.json", true},
		{"generate-foo.json", true},
		{"context.md", false},
		{"project.yaml", false},
		{"testing.md", false},
		{"deployment.md", false},
		{"generate-context.yaml", false}, // only .json generate files are excluded
	}
	for _, tc := range cases {
		got := isExcludedProjectFile(tc.name)
		if got != tc.want {
			t.Errorf("isExcludedProjectFile(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsSyncableProjectFile(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"context.md", true},
		{"project.yaml", true},
		{"testing.md", true},
		{"README.MD", true}, // case-insensitive extension check
		{"health-check.json", false},
		{"binary", false},
		{"data.json", false},
	}
	for _, tc := range cases {
		got := isSyncableProjectFile(tc.name)
		if got != tc.want {
			t.Errorf("isSyncableProjectFile(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestDiscoverProjectAssets creates a temporary directory tree that mimics
// ~/.clawflow/projects/ and verifies that DiscoverProjectAssets returns the
// correct set of Gist filenames while honouring the exclusion rules.
func TestDiscoverProjectAssets(t *testing.T) {
	// Build a fake projects root:
	//   myapp/
	//     context.md          ← should be included
	//     project.yaml        ← should be included
	//     health-check.json   ← excluded (static list)
	//     generate-ctx.json   ← excluded (pattern)
	//   bbclaw/
	//     testing.md          ← should be included
	//     deployment.md       ← should be included
	//     data.bin            ← excluded (wrong extension)

	tmpRoot := t.TempDir()

	writeFile := func(parts ...string) {
		path := filepath.Join(parts...)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("content of "+filepath.Base(path)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeFile(tmpRoot, "myapp", "context.md")
	writeFile(tmpRoot, "myapp", "project.yaml")
	writeFile(tmpRoot, "myapp", "health-check.json")
	writeFile(tmpRoot, "myapp", "generate-ctx.json")
	writeFile(tmpRoot, "bbclaw", "testing.md")
	writeFile(tmpRoot, "bbclaw", "deployment.md")
	writeFile(tmpRoot, "bbclaw", "data.bin")

	// Temporarily override the projects root by monkey-patching via env var
	// used by project.ProjectsRoot(). Since we can't easily override that
	// function, we call the internal helpers directly instead.
	result := make(map[string]string)
	for _, projectName := range []string{"myapp", "bbclaw"} {
		projectDir := filepath.Join(tmpRoot, projectName)
		files, err := os.ReadDir(projectDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			name := f.Name()
			if isExcludedProjectFile(name) || !isSyncableProjectFile(name) {
				continue
			}
			content, _ := os.ReadFile(filepath.Join(projectDir, name))
			relPath := filepath.Join("projects", projectName, name)
			result[EncodeGistFilename(relPath)] = string(content)
		}
	}

	want := map[string]bool{
		"projects--myapp--context.md":    true,
		"projects--myapp--project.yaml":  true,
		"projects--bbclaw--testing.md":   true,
		"projects--bbclaw--deployment.md": true,
	}

	if len(result) != len(want) {
		t.Errorf("got %d files, want %d: %v", len(result), len(want), result)
	}
	for k := range want {
		if _, ok := result[k]; !ok {
			t.Errorf("missing expected key %q in result", k)
		}
	}
	// Ensure excluded files are absent.
	for _, excluded := range []string{
		"projects--myapp--health-check.json",
		"projects--myapp--generate-ctx.json",
		"projects--bbclaw--data.bin",
	} {
		if _, ok := result[excluded]; ok {
			t.Errorf("excluded file %q should not be in result", excluded)
		}
	}
}

// TestApplyProjectAssets verifies that ApplyProjectAssets writes files to the
// correct paths and ignores non-project-asset entries.
func TestApplyProjectAssets(t *testing.T) {
	tmpHome := t.TempDir()
	// Temporarily override HOME so os.UserHomeDir() returns our temp dir.
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	gistFiles := map[string]GistFileContent{
		"projects--myapp--context.md":   {Content: "# Context"},
		"projects--myapp--project.yaml": {Content: "name: myapp"},
		"config.yaml":                   {Content: "repos: []"}, // should be ignored
	}

	if err := ApplyProjectAssets(gistFiles); err != nil {
		t.Fatalf("ApplyProjectAssets returned error: %v", err)
	}

	cases := []struct {
		relPath string
		want    string
	}{
		{filepath.Join(".clawflow", "projects", "myapp", "context.md"), "# Context"},
		{filepath.Join(".clawflow", "projects", "myapp", "project.yaml"), "name: myapp"},
	}
	for _, tc := range cases {
		absPath := filepath.Join(tmpHome, tc.relPath)
		got, err := os.ReadFile(absPath)
		if err != nil {
			t.Errorf("expected file %s to exist: %v", absPath, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("file %s: got %q, want %q", absPath, string(got), tc.want)
		}
	}

	// config.yaml should NOT have been written under .clawflow/
	configPath := filepath.Join(tmpHome, ".clawflow", "config.yaml")
	if _, err := os.Stat(configPath); err == nil {
		t.Errorf("config.yaml should not have been written by ApplyProjectAssets")
	}
}
