// Package queue implements ClawFlow's single cross-repository work queue:
// item keys, priority labels, dependency lines, the manual rank file and the
// global ordering. Everything here is pure or file-local so it can be tested
// without a VCS.
package queue

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MaxDependencies is how many dependencies of one item are honoured.
const MaxDependencies = 10

// DefaultPriority is the tier of items without a valid priority label.
const DefaultPriority Priority = 2

var keyRe = regexp.MustCompile(`^(?P<repo>[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)+)#(?P<n>[1-9][0-9]*)$`)

// Key identifies one issue across repositories: owner/repo#N (GitLab:
// group/sub/project#N).
type Key struct {
	Repo   string
	Number int
}

// ParseKey parses the canonical item key.
func ParseKey(s string) (Key, error) {
	m := keyRe.FindStringSubmatch(s)
	if m == nil {
		return Key{}, fmt.Errorf("invalid item key %q (want owner/repo#N)", s)
	}
	n, err := strconv.Atoi(m[keyRe.SubexpIndex("n")])
	if err != nil {
		return Key{}, fmt.Errorf("invalid item key %q: %w", s, err)
	}
	return Key{Repo: m[keyRe.SubexpIndex("repo")], Number: n}, nil
}

// String returns the canonical form.
func (k Key) String() string { return fmt.Sprintf("%s#%d", k.Repo, k.Number) }

// Less orders keys by repo then issue number.
func (k Key) Less(o Key) bool {
	if k.Repo != o.Repo {
		return k.Repo < o.Repo
	}
	return k.Number < o.Number
}

// Priority is the tier of an item: 0 (P0, most urgent) to 3 (P3).
type Priority int

// String renders the label form, e.g. "P1".
func (p Priority) String() string { return "P" + strconv.Itoa(int(p)) }

var priorityLabels = map[string]Priority{"P0": 0, "P1": 1, "P2": 2, "P3": 3}

// lookalikeRe matches labels that resemble a priority label but are not one.
var lookalikeRe = regexp.MustCompile(`(?i)^(p[0-9]+|priority\s*[:/_-]?\s*[0-9a-z]+)$`)

// PriorityFromLabels returns the priority for an issue's labels: the highest
// (lowest number) exact P0..P3 label wins, none yields DefaultPriority.
// Look-alikes (p1, priority:1) are ignored and returned as warnings.
func PriorityFromLabels(labels []string) (Priority, []string) {
	best, found := DefaultPriority, false
	var warns []string
	for _, l := range labels {
		if p, ok := priorityLabels[l]; ok {
			if !found || p < best {
				best, found = p, true
			}
			continue
		}
		if lookalikeRe.MatchString(l) {
			warns = append(warns, fmt.Sprintf("invalid priority label %q ignored (use P0, P1, P2 or P3)", l))
		}
	}
	return best, warns
}

const dependsPrefix = "Depends on:"

// ParseDependsOn parses the single `Depends on:` body line. Tokens are
// comma-separated `#N` (same repo) or full item keys. At most MaxDependencies
// are returned; unparseable tokens, extra tokens and extra Depends lines
// produce warnings while the valid ones still apply.
func ParseDependsOn(body, repo string) ([]Key, []string) {
	var line string
	var warns []string
	found := false
	for _, l := range strings.Split(body, "\n") {
		l = strings.TrimRight(l, "\r")
		if !strings.HasPrefix(l, dependsPrefix) {
			continue
		}
		if found {
			warns = append(warns, "multiple \"Depends on:\" lines; only the first is used")
			break
		}
		line, found = strings.TrimPrefix(l, dependsPrefix), true
	}
	if !found {
		return nil, nil
	}
	var deps []Key
	seen := map[Key]bool{}
	for _, tok := range strings.Split(line, ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		k, err := parseDepToken(tok, repo)
		if err != nil {
			warns = append(warns, fmt.Sprintf("dependency %q ignored: not #N or owner/repo#N", tok))
			continue
		}
		if seen[k] {
			continue
		}
		if len(deps) >= MaxDependencies {
			warns = append(warns, fmt.Sprintf("more than %d dependencies; extra %q ignored", MaxDependencies, tok))
			continue
		}
		seen[k] = true
		deps = append(deps, k)
	}
	return deps, warns
}

func parseDepToken(tok, repo string) (Key, error) {
	if strings.HasPrefix(tok, "#") {
		return ParseKey(repo + tok)
	}
	return ParseKey(tok)
}
