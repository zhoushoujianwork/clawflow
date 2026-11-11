package webguard

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Placeholders left in redacted output. Neither contains a quote or backslash,
// so substituting them never changes the shape of a JSON document.
const (
	RedactedSecret = "[redacted]"
	RedactedPath   = "[path]"
)

// Redactor removes credentials and machine-local paths from text that is about
// to leave this host. It is pattern based: values it has been told about
// (configured tokens, the state directory, the home directory) are replaced
// exactly; everything else is caught by credential-shaped and path-shaped
// rules. A secret with no recognizable shape that was never registered can
// still pass through, so callers should register every credential they know.
type Redactor struct {
	secrets func() []string
	paths   func() []string

	mu          sync.Mutex
	loadedAt    time.Time
	secretCache []string
	pathCache   []string
	pathRe      *regexp.Regexp
}

// redactorRefresh bounds how often the literal lists are re-read. Configured
// credentials rarely change, and re-reading them for every response is wasteful.
const redactorRefresh = 10 * time.Second

// NewRedactor builds a Redactor. Either list function may be nil. Both are
// called at most once per redactorRefresh.
func NewRedactor(secrets, paths func() []string) *Redactor {
	return &Redactor{secrets: secrets, paths: paths}
}

func (r *Redactor) literals() (secrets, paths []string) {
	if r == nil {
		return nil, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.loadedAt.IsZero() && time.Since(r.loadedAt) < redactorRefresh {
		return r.secretCache, r.pathCache
	}
	r.secretCache, r.pathCache = nil, nil
	if r.secrets != nil {
		seen := map[string]bool{}
		for _, v := range r.secrets() {
			v = strings.TrimSpace(v)
			// Very short values would corrupt unrelated text.
			if len(v) >= 6 && !seen[v] {
				seen[v] = true
				r.secretCache = append(r.secretCache, v)
			}
		}
	}
	if r.paths != nil {
		seen := map[string]bool{}
		for _, v := range r.paths() {
			v = strings.TrimRight(strings.TrimSpace(v), "/")
			if v != "" {
				v = filepath.Clean(v)
			}
			if len(v) >= 4 && v != "/" && !seen[v] {
				seen[v] = true
				r.pathCache = append(r.pathCache, v)
			}
		}
	}
	byLength := func(s []string) {
		sort.Slice(s, func(i, j int) bool { return len(s[i]) > len(s[j]) })
	}
	byLength(r.secretCache)
	byLength(r.pathCache)
	r.pathRe = nil
	if len(r.pathCache) > 0 {
		quoted := make([]string, len(r.pathCache))
		for i, p := range r.pathCache {
			quoted[i] = regexp.QuoteMeta(p)
		}
		// A known root also hides the sub-path that follows it.
		r.pathRe = regexp.MustCompile(`(?:` + strings.Join(quoted, "|") + `)` + subPath)
	}
	r.loadedAt = time.Now()
	return r.secretCache, r.pathCache
}

func (r *Redactor) knownPaths() *regexp.Regexp {
	if r == nil {
		return nil
	}
	r.literals()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pathRe
}

// subPath matches the "/a/b/c" tail of a path. macOS keeps user data under
// "Application Support", the one common directory name that contains a space.
// A segment stops at a closing Markdown backtick or "*".
const subPath = `(?:/(?:Application Support|[^\s"'<>(){}\[\],;|\\/*` + "`" + `]+))*/?`

var (
	pemRe     = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`)
	urlUserRe = regexp.MustCompile(`(://)[^/?#@\s"'<>]+@`)
	authRe    = regexp.MustCompile(`(?i)(authorization["']?\s*[:=]\s*["']?)(?:(?:bearer|basic|token)\s+)?[^\s"',;}]+`)
	bearerRe  = regexp.MustCompile(`(?i)(\bbearer\s+)[A-Za-z0-9._~+/=-]{16,}`)
	kvRe      = regexp.MustCompile(`(?i)\b([A-Za-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credential|dsn)[A-Za-z0-9_.-]*["']?\s*[=:]\s*["']?)([^\s"',;&}\])]{4,})`)
	shapeRe   = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{16,}|sk-[A-Za-z0-9_-]{16,}|xox[abprs]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{30,}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})`)
	fileURLRe = regexp.MustCompile(`(file://)/[^\s"'<>]*`)
	winPathRe = regexp.MustCompile(`\b[A-Za-z]:\\(?:Users|Windows|Program Files|ProgramData)[^\s"'<>|]*`)
	// "~" or an absolute path; pathRoots decides whether the first segment is
	// one a build machine or CI runner uses. Server-relative URLs such as
	// /api/... or /data/... never start with one of those roots.
	// Markdown wraps paths in backticks, "*" and "_", so those also start one.
	pathRe = regexp.MustCompile("(^|[\\s\"'(\\[{=:,<>@|`*_])(~|/[A-Za-z0-9_.@+-]*)(" + subPath + ")")
)

var pathRoots = map[string]bool{"Users": true, "home": true, "root": true, "var": true, "private": true, "tmp": true, "opt": true,
	"Volumes": true, "etc": true, "usr": true, "mnt": true, "srv": true, "Library": true, "Applications": true, "proc": true, "nix": true,
	"workspace": true, "workspaces": true}

func redactPaths(s string) string {
	return pathRe.ReplaceAllStringFunc(s, func(m string) string {
		parts := pathRe.FindStringSubmatch(m)
		lead, head, rest := parts[1], parts[2], parts[3]
		if head == "~" {
			if rest == "" {
				return m
			}
		} else if !pathRoots[strings.TrimPrefix(head, "/")] {
			return m
		}
		return lead + RedactedPath
	})
}

// plainValue reports values that are not credentials even when their key is
// credential-shaped: counters, flags and values already replaced.
func plainValue(v string) bool {
	switch strings.ToLower(v) {
	case "true", "false", "null", "nil", "none":
		return true
	}
	if strings.HasPrefix(v, "[") || strings.HasPrefix(v, "*") {
		return true
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Text redacts free text.
func (r *Redactor) Text(s string) string {
	if s == "" {
		return s
	}
	secrets, _ := r.literals()
	if re := r.knownPaths(); re != nil {
		s = re.ReplaceAllString(s, RedactedPath)
	}
	for _, v := range secrets {
		s = strings.ReplaceAll(s, v, RedactedSecret)
	}
	s = pemRe.ReplaceAllString(s, RedactedSecret)
	s = urlUserRe.ReplaceAllString(s, "${1}****@")
	s = authRe.ReplaceAllString(s, "${1}****")
	s = bearerRe.ReplaceAllString(s, "${1}****")
	s = kvRe.ReplaceAllStringFunc(s, func(m string) string {
		parts := kvRe.FindStringSubmatch(m)
		if plainValue(parts[2]) {
			return m
		}
		return parts[1] + "****"
	})
	s = shapeRe.ReplaceAllString(s, RedactedSecret)
	s = fileURLRe.ReplaceAllString(s, "${1}"+RedactedPath)
	s = winPathRe.ReplaceAllString(s, RedactedPath)
	return redactPaths(s)
}

var (
	sensitiveKeyRe = regexp.MustCompile(`(?i)(^|[_-])(token|secret|password|passwd|api[_-]?key|apikey|authorization|credentials?|dsn|private[_-]?key)([_-]|$)`)
	pathKeys       = map[string]bool{"local_path": true, "localpath": true, "workdir": true, "worktree": true, "worktree_path": true, "cwd": true, "token_file": true, "tokenfile": true, "state_dir": true, "data_dir": true}
)

func sensitiveKey(k string) bool {
	return pathKeys[strings.ToLower(k)] || sensitiveKeyRe.MatchString(k)
}

func (r *Redactor) walk(v any) any {
	switch x := v.(type) {
	case string:
		return r.Text(x)
	case []any:
		for i := range x {
			x[i] = r.walk(x[i])
		}
	case map[string]any:
		for k, item := range x {
			if sensitiveKey(k) {
				// Counters ("input_tokens") and flags keep their value; only
				// text and structures under a credential-named key are dropped.
				mask := RedactedSecret
				if pathKeys[strings.ToLower(k)] {
					mask = RedactedPath
				}
				switch s := item.(type) {
				case string:
					if s != "" {
						x[k] = mask
					}
					continue
				case map[string]any, []any:
					x[k] = mask
					continue
				}
			}
			x[k] = r.walk(item)
		}
	}
	return v
}

// JSON redacts every string in a JSON document and re-encodes it compactly
// (without a trailing newline). It reports false when b is not one complete
// JSON value, so the caller can fall back to Text.
func (r *Redactor) JSON(b []byte) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, false
	}
	var out bytes.Buffer
	if err := json.NewEncoder(&out).Encode(r.walk(v)); err != nil {
		return nil, false
	}
	return bytes.TrimRight(out.Bytes(), "\n"), true
}

// Bytes redacts a body that is JSON when it parses as JSON and text otherwise.
func (r *Redactor) Bytes(b []byte) []byte {
	if out, ok := r.JSON(b); ok {
		// Keep the trailing newline Go's JSON encoders add, so clients and
		// tools see the same framing as before.
		if bytes.HasSuffix(b, []byte("\n")) {
			out = append(out, '\n')
		}
		return out
	}
	return []byte(r.Text(string(b)))
}
