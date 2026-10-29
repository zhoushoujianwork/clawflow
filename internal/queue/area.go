package queue

import (
	"path"
	"regexp"
	"strings"
)

// Paths are hints, never instructions or filesystem reads. URLs and absolute
// paths are excluded; a bare filename inherits the last explicit directory.
var fileHint = regexp.MustCompile("[A-Za-z0-9_./-]+\\.(?:go|tsx?|jsx?|sql|py|rs|json|ya?ml|md|sh|css|html|toml)\\b")

// EstimateFiles extracts explicit repository file hints from requirements or
// saved acceptance-plan text. No hints means overlap checking is skipped.
func EstimateFiles(text string) []string {
	var out []string
	dir := ""
	for _, loc := range fileHint.FindAllStringIndex(text, -1) {
		name := text[loc[0]:loc[1]]
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") || (loc[0] > 0 && strings.Contains(":@", text[loc[0]-1:loc[0]])) {
			continue
		}
		name = strings.TrimPrefix(name, "./")
		if strings.Contains(name, "/") {
			dir = path.Dir(name)
		} else if dir != "" {
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
			if a == b || (!strings.Contains(a, "/") && a == path.Base(b)) || (storeMigration(a) && storeMigration(b)) {
				return true
			}
		}
	}
	return false
}
