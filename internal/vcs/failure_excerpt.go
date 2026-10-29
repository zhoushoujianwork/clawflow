package vcs

import "strings"

// FailureExcerpt selects from already redacted logs. Failure markers take
// precedence over nearby diagnostics, which take precedence over the tail.
// If even the markers exceed the budget, the truncation notice remains explicit.
func FailureExcerpt(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	lines := strings.Split(s, "\n")
	selected := make([]bool, len(lines))
	remaining := limit
	add := func(i int) {
		if i >= 0 && i < len(lines) && !selected[i] && len(lines[i])+1 <= remaining {
			selected[i] = true
			remaining -= len(lines[i]) + 1
		}
	}
	var failures []int
	for i, line := range lines {
		if strings.Contains(line, "--- FAIL:") || strings.Contains(line, "panic:") {
			failures = append(failures, i)
			add(i)
		}
	}
	if len(failures) == 0 {
		return "[truncated]\n" + s[len(s)-limit:]
	}
	// Round-robin context gives each failure a chance to retain its first frames.
	// Go assertion messages commonly precede the --- FAIL marker.
	for _, offset := range []int{1, -1, 2, -2, 3, -3, 4, -4, 5, 6, 7, 8} {
		for _, i := range failures {
			add(i + offset)
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		add(i)
	}
	var out strings.Builder
	out.WriteString("[truncated]\n")
	for i, line := range lines {
		if selected[i] {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	return out.String()
}
