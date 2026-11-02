// Package devmode enforces the runtime boundary of a development candidate.
//
// A candidate process is started by `clawflow candidate up` with
// CLAWFLOW_CANDIDATE_ID set. In that mode every outbound HTTP request must
// target a loopback address, listeners must stay on the candidate's own
// loopback port, the database schema must be the candidate's own, and host
// mutations (launchd labels, binary replacement) must target candidate-owned
// resources. The checks fail closed: a malformed candidate identity blocks
// everything instead of disabling the guard.
//
// The package depends only on the standard library so any layer can import it.
package devmode

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Environment variables that carry the candidate identity into child processes.
const (
	EnvID     = "CLAWFLOW_CANDIDATE_ID"
	EnvRoot   = "CLAWFLOW_CANDIDATE_ROOT"
	EnvSchema = "CLAWFLOW_CANDIDATE_SCHEMA"
	EnvPort   = "CLAWFLOW_CANDIDATE_PORT"
)

// LabelBase is the launchd label namespace reserved for candidates. Stable
// ClawFlow services never use the ".dev." segment.
const LabelBase = "com.zhoushoujianwork.clawflow.dev."

// SchemaPrefix is the only schema namespace a candidate may own.
const SchemaPrefix = "cf_cand_"

var idPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

// ErrBlocked marks an action refused by the candidate boundary.
var ErrBlocked = errors.New("blocked by candidate mode")

func blocked(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBlocked, fmt.Sprintf(format, args...))
}

// Active reports whether this process runs as a development candidate.
func Active() bool { return os.Getenv(EnvID) != "" }

// ID returns the validated candidate id, or "" when it is missing or malformed.
func ID() string {
	if id := os.Getenv(EnvID); idPattern.MatchString(id) {
		return id
	}
	return ""
}

// ValidID reports whether id is a well-formed candidate id.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Root returns the candidate-owned directory.
func Root() string { return os.Getenv(EnvRoot) }

// Schema returns the candidate database schema.
func Schema() string { return os.Getenv(EnvSchema) }

// LabelPrefix returns the launchd label prefix owned by a candidate id.
func LabelPrefix(id string) string { return LabelBase + id + "." }

// ServiceLabel returns the launchd label for a candidate service kind.
func ServiceLabel(id, kind string) string { return LabelPrefix(id) + kind }

// Port returns the candidate's only permitted listener port, or 0.
func Port() int {
	p, err := strconv.Atoi(os.Getenv(EnvPort))
	if err != nil || p < 1 || p > 65535 {
		return 0
	}
	return p
}

// Loopback reports whether host is a literal loopback address or "localhost".
func Loopback(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type guardedTransport struct{ base http.RoundTripper }

func (g guardedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := CheckURL(r.URL); err != nil {
		if r.Body != nil {
			_ = r.Body.Close()
		}
		return nil, err
	}
	return g.base.RoundTrip(r)
}

// Transport wraps base so that only loopback destinations are reachable.
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return guardedTransport{base: base}
}

// Install replaces http.DefaultTransport when the process is a candidate.
// Clients that leave Transport nil (all VCS, update and sync clients) are
// covered, including every redirect hop. Install is idempotent.
func Install() {
	if !Active() {
		return
	}
	if _, ok := http.DefaultTransport.(guardedTransport); ok {
		return
	}
	http.DefaultTransport = Transport(http.DefaultTransport)
	// Children (git, offload) inherit this environment: no real VCS tokens, and
	// git may only use local file remotes.
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GITLAB_TOKEN", "SSH_AUTH_SOCK"} {
		_ = os.Unsetenv(name)
	}
	_ = os.Setenv("GIT_ALLOW_PROTOCOL", "file")
}

// CheckURL refuses any non-loopback destination.
func CheckURL(u *url.URL) error {
	if u == nil || !Loopback(u.Hostname()) {
		host := ""
		if u != nil {
			host = u.Hostname()
		}
		return blocked("outbound request to %q refused; candidates may only reach loopback fixtures", host)
	}
	return nil
}

// CheckBaseURL validates a repository base URL for candidate use. An empty
// URL means the public default host and is refused.
func CheckBaseURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return blocked("repository without a loopback base_url would target the real service")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return blocked("unparseable base_url")
	}
	return CheckURL(u)
}

// CheckListen requires a loopback listener on the candidate's own port.
func CheckListen(addr string) error {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return blocked("listen address %q is not host:port", addr)
	}
	if !Loopback(host) {
		return blocked("listener must bind a loopback address, got %q", host)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port != Port() || port == 0 {
		return blocked("listener port %q is not this candidate's port", portText)
	}
	return nil
}

// CheckDSN requires the connection to be pinned to the candidate schema.
func CheckDSN(dsn string) error {
	schema := Schema()
	if !strings.HasPrefix(schema, SchemaPrefix) {
		return blocked("candidate schema is not configured")
	}
	var got string
	if strings.Contains(dsn, "://") {
		if u, err := url.Parse(dsn); err == nil {
			got = u.Query().Get("search_path")
		}
	} else {
		for _, field := range strings.Fields(dsn) {
			if v, ok := strings.CutPrefix(field, "search_path="); ok {
				got = v
			}
		}
	}
	if got != schema {
		return blocked("database connection is not pinned to the candidate schema")
	}
	return nil
}

// Within reports whether path is dir or lies below it, after resolving the
// longest existing prefix so symlinks cannot escape.
func Within(dir, path string) bool {
	d, p := resolve(dir), resolve(path)
	if d == "" || p == "" {
		return false
	}
	rel, err := filepath.Rel(d, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func resolve(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	rest := ""
	for cur := abs; ; cur = filepath.Dir(cur) {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		if filepath.Dir(cur) == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
	}
}

// CheckWritePath refuses file replacement outside the candidate root.
func CheckWritePath(path string) error {
	if root := Root(); root == "" || !Within(root, path) {
		return blocked("path %q is outside the candidate root", path)
	}
	return nil
}

// CheckLaunchTarget permits launchd operations only on candidate-owned labels
// and property lists below the candidate root.
func CheckLaunchTarget(label, plist string) error {
	id := ID()
	if id == "" || !strings.HasPrefix(label, LabelPrefix(id)) {
		return blocked("launchd label %q is not owned by this candidate", label)
	}
	if plist != "" {
		if err := CheckWritePath(plist); err != nil {
			return err
		}
	}
	return nil
}

// HostMutatingCommands target the installed CLI, real credentials or the real
// VCS, so a candidate refuses them outright.
var HostMutatingCommands = map[string]bool{"update": true, "sync": true, "login": true, "web": true, "candidate": true, "__respawn": true}

// CheckCommand refuses host-mutating commands in candidate mode.
func CheckCommand(name string) error {
	if Active() && HostMutatingCommands[name] {
		return blocked("command %q can change the installed host and is unavailable in candidate mode", name)
	}
	return nil
}
