package queue

import (
	"path"
	"regexp"
	"strings"
)

// Paths are hints, never instructions or filesystem reads. URLs and absolute
// paths are excluded; bare filenames also retain their literal root path.
var fileHint = regexp.MustCompile("[A-Za-z0-9_./-]+")

// EstimateFiles extracts explicit repository file hints from requirements or
// saved acceptance-plan text. No hints means overlap checking is skipped.
func EstimateFiles(text string) []string {
	var out []string
	dir := ""
	for _, loc := range fileHint.FindAllStringIndex(text, -1) {
		name := strings.TrimRight(text[loc[0]:loc[1]], ".")
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") || (loc[0] > 0 && strings.Contains(":@", text[loc[0]-1:loc[0]])) {
			continue
		}
		// Accept arbitrary extensions and paths. Inline code also permits
		// extensionless names; ordinary prose must not become a file hint.
		quoted := loc[0] > 0 && text[loc[0]-1] == '`' && loc[1] < len(text) && text[loc[1]] == '`'
		if name == "" || strings.HasSuffix(name, "/") || (!strings.ContainsAny(name, "./") && !quoted && name != "Dockerfile" && name != "Makefile") {
			continue
		}
		rootRelative := strings.HasPrefix(name, "./")
		name = strings.TrimPrefix(name, "./")
		if strings.Contains(name, "/") {
			dir = path.Dir(name)
		} else if dir != "" && !rootRelative {
			// A bare name may be a root file, not a sibling abbreviation.
			// Keep both hints so directory inheritance cannot hide it.
			out = append(out, name)
			name = dir + "/" + name
		}
		out = append(out, name)
	}
	return out
}

func storeMigration(name string) bool {
	return name == "internal/store/migrations.go" || strings.HasPrefix(name, "internal/store/migrations/")
}

// FilesOverlap includes the shared numbering namespace of store migrations.
func FilesOverlap(estimated, changed []string) bool {
	for _, a := range estimated {
		for _, b := range changed {
			if a == b || (storeMigration(a) && storeMigration(b)) {
				return true
			}
		}
	}
	return false
}
