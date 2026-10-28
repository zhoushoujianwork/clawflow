package pipeline

import "strings"

// DecidedSection records a heading, not a mention of Decided in prose.
// A pointer distinguishes inspected bodies from older snapshots without evidence.
func DecidedSection(body string) *bool {
	found := false
	fenced := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced || !strings.HasPrefix(line, "#") {
			continue
		}
		title := strings.TrimLeft(line, "#")
		if title == line || !strings.HasPrefix(title, " ") {
			continue
		}
		title = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(title), "#"))
		if strings.EqualFold(title, "Decided") {
			found = true
		}
	}
	return &found
}
