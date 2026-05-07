// Package sync — codec.go provides bidirectional conversion between local
// project asset paths and their flat Gist filename equivalents.
//
// Local path (relative to ~/.clawflow/):
//
//	projects/<name>/context.md
//	projects/<name>/project.yaml
//
// Gist filename (flat, no slashes):
//
//	projects--<name>--context.md
//	projects--<name>--project.yaml
//
// The separator "--" was chosen because it cannot appear in a project name
// (names are validated as simple identifiers) and is visually distinct from
// the path separator.
package sync

import (
	"path/filepath"
	"strings"
)

const gistPathSep = "--"

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
// Returns ("", false) when the filename does not look like a project asset
// (i.e. does not start with "projects--").
func DecodeGistFilename(filename string) (relPath string, ok bool) {
	if !strings.HasPrefix(filename, "projects"+gistPathSep) {
		return "", false
	}
	return strings.ReplaceAll(filename, gistPathSep, "/"), true
}

// IsProjectAssetFilename reports whether a Gist filename was produced by
// EncodeGistFilename for a project asset (starts with "projects--").
func IsProjectAssetFilename(filename string) bool {
	return strings.HasPrefix(filename, "projects"+gistPathSep)
}
