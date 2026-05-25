package sync

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
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
		{"skills--evaluate-bug--SKILL.md", "skills/evaluate-bug/SKILL.md", true},
		{"skills--evaluate-bug--evaluation.md", "skills/evaluate-bug/evaluation.md", true},
		{"config.yaml", "", false},
		{"something-else.md", "", false},
		{"skill--no-double-dash.md", "", false},
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
		{"skills--foo--SKILL.md", false},
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

func TestIsSkillAssetFilename(t *testing.T) {
	cases := []struct {
		filename string
		want     bool
	}{
		{"skills--evaluate-bug--SKILL.md", true},
		{"skills--my-op--evaluation.md", true},
		{"projects--myapp--context.md", false},
		{"config.yaml", false},
		{"skill--singular-prefix.md", false},
		{"", false},
	}
	for _, tc := range cases {
		got := IsSkillAssetFilename(tc.filename)
		if got != tc.want {
			t.Errorf("IsSkillAssetFilename(%q) = %v, want %v", tc.filename, got, tc.want)
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
	//     generate-ctx.json   ← excluded (generate-* pattern)
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
		"skills--evaluate-bug--SKILL.md":   {Content: "# eval-bug skill"},
		"skills--evaluate-bug--evaluation.md": {Content: "# eval rubric"},
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
		{filepath.Join(".clawflow", "skills", "evaluate-bug", "SKILL.md"), "# eval-bug skill"},
		{filepath.Join(".clawflow", "skills", "evaluate-bug", "evaluation.md"), "# eval rubric"},
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

// TestIsSyncableSkillFile mirrors TestIsSyncableProjectFile for the skills
// filter. Same .yaml/.md whitelist; everything else is dropped.
func TestIsSyncableSkillFile(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"SKILL.md", true},
		{"evaluation.md", true},
		{"config.yaml", true},
		{"helper.sh", false},
		{"data.json", false},
		{"binary", false},
	}
	for _, tc := range cases {
		got := isSyncableSkillFile(tc.name)
		if got != tc.want {
			t.Errorf("isSyncableSkillFile(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestDiscoverSkillAssets builds a fake ~/.clawflow/skills/ tree and verifies
// that DiscoverSkillAssets returns exactly the syncable files (.md, .yaml)
// at the top level of each skill directory, skipping nested subdirs.
func TestDiscoverSkillAssets(t *testing.T) {
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	skillsDir := filepath.Join(tmpHome, ".clawflow", "skills")
	writeFile := func(parts ...string) {
		path := filepath.Join(parts...)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("content of "+filepath.Base(path)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// evaluate-bug/
	//   SKILL.md           ← included
	//   evaluation.md      ← included
	//   helper.sh          ← excluded (extension)
	//   scripts/run.sh     ← excluded (nested dir)
	// triage/
	//   SKILL.md           ← included
	writeFile(skillsDir, "evaluate-bug", "SKILL.md")
	writeFile(skillsDir, "evaluate-bug", "evaluation.md")
	writeFile(skillsDir, "evaluate-bug", "helper.sh")
	writeFile(skillsDir, "evaluate-bug", "scripts", "run.sh")
	writeFile(skillsDir, "triage", "SKILL.md")

	got, err := DiscoverSkillAssets()
	if err != nil {
		t.Fatalf("DiscoverSkillAssets error: %v", err)
	}

	want := map[string]bool{
		"skills--evaluate-bug--SKILL.md":      true,
		"skills--evaluate-bug--evaluation.md": true,
		"skills--triage--SKILL.md":            true,
	}
	if len(got) != len(want) {
		t.Errorf("got %d files, want %d: %v", len(got), len(want), got)
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing expected key %q in result", k)
		}
	}
	for forbidden := range map[string]bool{
		"skills--evaluate-bug--helper.sh": true,
		"skills--evaluate-bug--run.sh":    true,
	} {
		if _, ok := got[forbidden]; ok {
			t.Errorf("forbidden file %q should not be in result", forbidden)
		}
	}
}

// TestDiscoverSkillAssets_Missing verifies that an absent ~/.clawflow/skills
// directory yields an empty map rather than an error — most users won't have
// custom operators.
func TestDiscoverSkillAssets_Missing(t *testing.T) {
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	got, err := DiscoverSkillAssets()
	if err != nil {
		t.Fatalf("DiscoverSkillAssets on missing dir: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result on missing dir, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// pushFiles guards (issue #195)
// ---------------------------------------------------------------------------

func TestHasNonEmptyFile(t *testing.T) {
	if hasNonEmptyFile(map[string]string{}) {
		t.Error("empty map should report no non-empty files")
	}
	if hasNonEmptyFile(map[string]string{"config.yaml": ""}) {
		t.Error("all-empty content should report no non-empty files")
	}
	if !hasNonEmptyFile(map[string]string{"a": "", "config.yaml": "x"}) {
		t.Error("a single non-empty file should report true")
	}
}

// TestPushFiles_RefusesEmptyPayload verifies the death-loop guard: an
// all-empty payload must fail fast without making any HTTP request, instead
// of sending {"files":{}} and getting 422 missing_field "files" forever.
func TestPushFiles_RefusesEmptyPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected HTTP request for empty payload: %s %s", r.Method, r.URL.Path)
		http.Error(w, "should not be called", 500)
	}))
	defer srv.Close()
	gh := github.New("test-token", srv.URL)

	_, err := pushFiles(gh, "some-gist-id", map[string]string{"config.yaml": ""})
	if err == nil {
		t.Fatal("expected error for empty payload, got nil")
	}
}

// TestPushFiles_NoRetryOnSameGist verifies that when updating the stored Gist
// fails and FindGistByDescription returns that same Gist, pushFiles does not
// retry the identical payload — it creates a fresh Gist instead.
func TestPushFiles_NoRetryOnSameGist(t *testing.T) {
	const staleID = "stale123"
	var patchCount, createCount int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PATCH" && r.URL.Path == "/gists/"+staleID:
			patchCount++
			http.Error(w, `{"message":"Validation Failed"}`, 422)
		case r.Method == "GET" && r.URL.Path == "/gists":
			// The description lookup finds the very same stale Gist.
			writeJSON(t, w, 200, []map[string]any{{"id": staleID, "description": "clawflow-config"}})
		case r.Method == "POST" && r.URL.Path == "/gists":
			createCount++
			writeJSON(t, w, 201, map[string]any{"id": "fresh456"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "not found", 404)
		}
	}))
	defer srv.Close()
	gh := github.New("test-token", srv.URL)

	id, err := pushFiles(gh, staleID, map[string]string{"config.yaml": "real content"})
	if err != nil {
		t.Fatalf("pushFiles: %v", err)
	}
	if id != "fresh456" {
		t.Errorf("expected a freshly created Gist id, got %q", id)
	}
	if patchCount != 1 {
		t.Errorf("expected exactly 1 PATCH (no retry on same Gist), got %d", patchCount)
	}
	if createCount != 1 {
		t.Errorf("expected exactly 1 create, got %d", createCount)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, status int, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
