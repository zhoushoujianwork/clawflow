package commands

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// baseBranchMarkerRe matches a ClawFlow-Base-Branch: <value> line anywhere in text.
// The value is captured as the first submatch group.
var baseBranchMarkerRe = regexp.MustCompile(`(?m)^ClawFlow-Base-Branch:\s*(\S+)\s*$`)

// validBranchNameRe restricts allowed characters to a conservative safe set.
var validBranchNameRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// ParseBaseBranchOverride scans the issue body and comments for a
//
//	ClawFlow-Base-Branch: <branch>
//
// marker. Resolution order:
//  1. Comments are sorted newest-first by CreatedAt (ISO 8601 lexicographic).
//     The first (newest) comment containing the marker wins.
//  2. If no comment contains the marker, the issue body is checked.
//
// Returns:
//   - branch: the trimmed branch name (non-empty only when ok == true)
//   - source: human-readable description of where the marker was found
//   - ok: true if any marker was found (valid or invalid)
//   - err: non-nil if a marker was found but the branch name fails validation
//
// When ok == false and err == nil, no marker was found; the caller should use
// the repo-default base branch without modification.
func ParseBaseBranchOverride(body string, comments []vcs.IssueComment) (branch, source string, ok bool, err error) {
	// Sort comments newest-first so we pick the latest marker.
	sorted := make([]vcs.IssueComment, len(comments))
	copy(sorted, comments)
	sort.Slice(sorted, func(i, j int) bool {
		// ISO 8601 strings compare lexicographically correctly.
		return sorted[i].CreatedAt > sorted[j].CreatedAt
	})

	for _, c := range sorted {
		if val, found := extractBaseBranchMarker(c.Body); found {
			src := fmt.Sprintf("comment by %s", c.Author)
			if c.Author == "" {
				src = "comment"
			}
			if valErr := validateBranchName(val); valErr != nil {
				return "", src, true, fmt.Errorf("invalid ClawFlow-Base-Branch marker in %s: %w", src, valErr)
			}
			return val, src, true, nil
		}
	}

	if val, found := extractBaseBranchMarker(body); found {
		if valErr := validateBranchName(val); valErr != nil {
			return "", "issue body", true, fmt.Errorf("invalid ClawFlow-Base-Branch marker in issue body: %w", valErr)
		}
		return val, "issue body", true, nil
	}

	return "", "", false, nil
}

// extractBaseBranchMarker returns the branch value from the first matching
// ClawFlow-Base-Branch: <value> line in text, or ("", false) if none found.
func extractBaseBranchMarker(text string) (string, bool) {
	m := baseBranchMarkerRe.FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// validateBranchName checks that branch is a safe, non-empty git branch name
// free of shell-injection and path-traversal sequences.
func validateBranchName(branch string) error {
	if branch == "" {
		return fmt.Errorf("branch name must not be empty")
	}
	if len(branch) > 250 {
		return fmt.Errorf("branch name too long (%d chars, max 250)", len(branch))
	}
	if !validBranchNameRe.MatchString(branch) {
		return fmt.Errorf("branch name %q contains invalid characters (allowed: A-Za-z0-9._/-)", branch)
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("branch name %q must not start with '-' (would be interpreted as a git flag)", branch)
	}
	if strings.HasPrefix(branch, "/") {
		return fmt.Errorf("branch name %q must not start with '/'", branch)
	}
	if strings.HasPrefix(branch, ".") {
		return fmt.Errorf("branch name %q must not start with '.'", branch)
	}
	if strings.Contains(branch, "..") {
		return fmt.Errorf("branch name %q must not contain '..' (path traversal)", branch)
	}
	return nil
}
