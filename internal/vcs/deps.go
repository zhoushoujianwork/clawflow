// Package vcs — dependency parsing for blocked/depends-on annotations.
package vcs

import (
	"regexp"
	"strconv"
	"strings"
)

// DependencyType controls when a blocked issue is unlocked.
type DependencyType int

const (
	// DependsOnMerge unlocks only after the dependency's PR is merged (default).
	DependsOnMerge DependencyType = iota
	// DependsOnPR unlocks as soon as the dependency's PR is opened.
	DependsOnPR
)

// Dependency is a single parsed depends-on entry.
type Dependency struct {
	IssueNumber int
	Type        DependencyType
}

var (
	// <!-- clawflow:depends-on #1 #2 --> or <!-- clawflow:depends-on-merge #1 -->
	reMerge = regexp.MustCompile(`(?i)<!--\s*clawflow:depends-on(?:-merge)?\s+([^-]*?)-->`)
	// <!-- clawflow:depends-on-pr #1 -->
	rePR = regexp.MustCompile(`(?i)<!--\s*clawflow:depends-on-pr\s+([^-]*?)-->`)
	// matches #N
	reRef = regexp.MustCompile(`#(\d+)`)
)

// ParseDependencies scans issue body + comments for depends-on annotations.
func ParseDependencies(body string, comments []string) []Dependency {
	sources := append([]string{body}, comments...)
	var deps []Dependency

	for _, src := range sources {
		if m := reMerge.FindStringSubmatch(src); m != nil {
			for _, n := range parseRefs(m[1]) {
				deps = append(deps, Dependency{IssueNumber: n, Type: DependsOnMerge})
			}
		}
		if m := rePR.FindStringSubmatch(src); m != nil {
			for _, n := range parseRefs(m[1]) {
				deps = append(deps, Dependency{IssueNumber: n, Type: DependsOnPR})
			}
		}
	}
	return dedup(deps)
}

func parseRefs(s string) []int {
	var nums []int
	for _, m := range reRef.FindAllStringSubmatch(s, -1) {
		n, err := strconv.Atoi(m[1])
		if err == nil && n > 0 {
			nums = append(nums, n)
		}
	}
	return nums
}

func dedup(deps []Dependency) []Dependency {
	seen := map[string]bool{}
	var out []Dependency
	for _, d := range deps {
		key := strconv.Itoa(d.IssueNumber) + ":" + strconv.Itoa(int(d.Type))
		if !seen[key] {
			seen[key] = true
			out = append(out, d)
		}
	}
	return out
}

// FilterByType returns only dependencies of the given type.
func FilterByType(deps []Dependency, t DependencyType) []Dependency {
	var out []Dependency
	for _, d := range deps {
		if d.Type == t {
			out = append(out, d)
		}
	}
	return out
}

// ContainsIssue reports whether deps contains the given issue number.
func ContainsIssue(deps []Dependency, number int) bool {
	for _, d := range deps {
		if d.IssueNumber == number {
			return true
		}
	}
	return false
}

// DepsSummary returns a human-readable string of dependencies (for logging).
func DepsSummary(deps []Dependency) string {
	if len(deps) == 0 {
		return "(none)"
	}
	var parts []string
	for _, d := range deps {
		t := "merge"
		if d.Type == DependsOnPR {
			t = "pr"
		}
		parts = append(parts, "#"+strconv.Itoa(d.IssueNumber)+"("+t+")")
	}
	return strings.Join(parts, ", ")
}
