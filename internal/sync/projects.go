package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/project"
)

// isExcludedProjectFile reports whether a filename (base name only) should
// be excluded from sync. Any file matching the "generate-*.json" pattern
// is excluded — those are async-job state caches that are machine-specific
// and meaningless on another machine.
func isExcludedProjectFile(name string) bool {
	return strings.HasPrefix(name, "generate-") && strings.HasSuffix(name, ".json")
}

// isSyncableProjectFile reports whether a file inside a project directory
// should be included in the Gist sync. Only .yaml and .md files are synced;
// everything else (binaries, .git/, etc.) is skipped.
func isSyncableProjectFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yaml" || ext == ".md"
}

// DiscoverProjectAssets walks ~/.clawflow/projects/ and returns a map of
// Gist filename → file content for every eligible project asset file.
//
// Eligible means: extension is .yaml or .md, not in the exclusion list, and
// not inside a .git subdirectory.
//
// The returned map is ready to be merged into the Gist files map alongside
// config.yaml.
func DiscoverProjectAssets() (map[string]string, error) {
	root := project.ProjectsRoot()
	result := make(map[string]string)

	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil // no projects yet — nothing to sync
		}
		return nil, fmt.Errorf("read projects dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projectName := entry.Name()
		projectDir := filepath.Join(root, projectName)

		files, err := os.ReadDir(projectDir)
		if err != nil {
			// Skip unreadable project dirs rather than aborting the whole sync.
			fmt.Fprintf(os.Stderr, "⚠ sync: cannot read project dir %s: %v\n", projectDir, err)
			continue
		}

		for _, f := range files {
			if f.IsDir() {
				continue // skip subdirectories (e.g. .git/)
			}
			name := f.Name()
			if isExcludedProjectFile(name) {
				continue
			}
			if !isSyncableProjectFile(name) {
				continue
			}

			absPath := filepath.Join(projectDir, name)
			content, err := os.ReadFile(absPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "⚠ sync: cannot read %s: %v\n", absPath, err)
				continue
			}

			// Relative path from ~/.clawflow/: "projects/<name>/<file>"
			relPath := filepath.Join("projects", projectName, name)
			gistFilename := EncodeGistFilename(relPath)
			result[gistFilename] = string(content)
		}
	}

	return result, nil
}

// ApplyProjectAssets writes synced asset files received from the Gist back
// to their correct local paths under ~/.clawflow/.
//
// gistFiles is the full map of filename → content from the fetched Gist.
// Only entries whose filename passes DecodeGistFilename (currently project
// assets prefixed "projects--" and user skill assets prefixed "skills--")
// are processed; all others are silently ignored, so it is safe to pass the
// raw Gist file map including config.yaml.
//
// Directories are created as needed. Existing files are overwritten
// (cloud-wins merge strategy, matching config.yaml behaviour).
//
// The name retains "ProjectAssets" for backwards compatibility; the
// implementation has been generalised to all synced asset prefixes.
func ApplyProjectAssets(gistFiles map[string]GistFileContent) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home dir: %w", err)
	}
	clawflowRoot := filepath.Join(home, ".clawflow")

	for filename, f := range gistFiles {
		relPath, ok := DecodeGistFilename(filename)
		if !ok {
			continue // not a project asset
		}

		absPath := filepath.Join(clawflowRoot, filepath.FromSlash(relPath))

		// Ensure the parent directory exists.
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			return fmt.Errorf("create dir for %s: %w", absPath, err)
		}

		if err := os.WriteFile(absPath, []byte(f.Content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", absPath, err)
		}
	}

	return nil
}

// GistFileContent is a minimal representation of a Gist file's content,
// used when iterating over files returned by the GitHub API.
type GistFileContent struct {
	Content string
}
