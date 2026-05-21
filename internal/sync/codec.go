// Package sync — codec.go provides bidirectional conversion between local
// asset paths under ~/.clawflow/ and their flat Gist filename equivalents.
//
// Local path (relative to ~/.clawflow/):
//
//	projects/<name>/context.md
//	skills/<name>/SKILL.md
//
// Gist filename (flat, no slashes):
//
//	projects--<name>--context.md
//	skills--<name>--SKILL.md
//
// The separator "--" was chosen because it cannot appear in a project or
// skill name (both are validated as simple identifiers) and is visually
// distinct from the path separator.
package sync

import (
	"path/filepath"
	"strings"
)

const gistPathSep = "--"

// syncedAssetPrefixes is the closed set of top-level directories whose
// contents are eligible for Gist sync. Any incoming filename with a different
// prefix (e.g. "config.yaml") is left to other handlers.
var syncedAssetPrefixes = []string{"projects", "skills"}

// EncodeGistFilename converts a slash-separated relative path (relative to
// ~/.clawflow/) into a flat Gist filename by replacing "/" with "--".
//
// Example: "projects/myapp/context.md" → "projects--myapp--context.md"
func EncodeGistFilename(relPath string) string {
	// Normalise to forward slashes regardless of OS.
	relPath = filepath.ToSlash(relPath)
	return strings.ReplaceAll(relPath, "/", gistPathSep)
}

// DecodeGistFilename converts a flat Gist filename back to a slash-separated
// relative path (relative to ~/.clawflow/).
//
// Example: "projects--myapp--context.md" → "projects/myapp/context.md"
//
// Returns ("", false) when the filename does not start with any of the
// known synced-asset prefixes (projects, skills).
func DecodeGistFilename(filename string) (relPath string, ok bool) {
	if !hasSyncedAssetPrefix(filename) {
		return "", false
	}
	return strings.ReplaceAll(filename, gistPathSep, "/"), true
}

// IsProjectAssetFilename reports whether a Gist filename was produced by
// EncodeGistFilename for a project asset (starts with "projects--").
func IsProjectAssetFilename(filename string) bool {
	return strings.HasPrefix(filename, "projects"+gistPathSep)
}

// IsSkillAssetFilename reports whether a Gist filename was produced by
// EncodeGistFilename for a user skill asset (starts with "skills--").
func IsSkillAssetFilename(filename string) bool {
	return strings.HasPrefix(filename, "skills"+gistPathSep)
}

func hasSyncedAssetPrefix(filename string) bool {
	for _, p := range syncedAssetPrefixes {
		if strings.HasPrefix(filename, p+gistPathSep) {
			return true
		}
	}
	return false
}
