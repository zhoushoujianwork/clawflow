// Package webguard hardens the local `clawflow web` HTTP server against
// two browser-borne attacks that loopback binding alone does NOT stop:
//
//   - DNS-rebinding: a malicious page whose domain resolves back to
//     127.0.0.1 can reach this server despite the loopback bind. Defended
//     by validating the Host header against a whitelist derived from the
//     bind address — a rebinding request carries the attacker's hostname,
//     not ours, and is rejected.
//   - Cross-site request forgery (CSRF): a page on any origin can fire a
//     "simple" POST (Content-Type: text/plain) that skips the CORS
//     preflight and reaches a state-changing handler (token write, chat
//     spawn, self-update). Defended by validating the Origin header on
//     state-changing methods.
//
// The same whitelist is reused by internal/pty for the /ws/pty WebSocket
// upgrader so HTTP and WS can never drift out of sync.
package webguard

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// TrustedHostsEnv extends the Host/Origin whitelist with extra entries
// (comma-separated, "host" or "host:port"). The escape hatch for reverse
// proxies that rewrite the Host header to something the bind address
// can't predict.
const TrustedHostsEnv = "CLAWFLOW_WEB_TRUSTED_HOSTS"

// Guard validates inbound Host and Origin headers against a whitelist
// derived from the server's bind address. The zero value is not usable;
// construct with New.
type Guard struct {
	// enforceHost is false for wildcard binds (0.0.0.0 / ::) where the
	// user has explicitly opted into LAN exposure — there's no single
	// canonical Host to pin, so we only warn at startup and let requests
	// through. Origin/CSRF checks still apply regardless.
	enforceHost bool
	hosts       map[string]struct{} // allowed Host header values (host:port)
	origins     map[string]struct{} // allowed Origin header values (scheme://host:port)
}

// New builds a Guard for a server bound to host:port. The whitelist
// always covers the loopback forms {127.0.0.1, localhost, [::1]} at the
// given port, plus the configured host when it is a concrete address,
// plus any entries in CLAWFLOW_WEB_TRUSTED_HOSTS.
func New(host string, port int) *Guard {
	g := &Guard{
		enforceHost: !IsWildcardHost(host),
		hosts:       map[string]struct{}{},
		origins:     map[string]struct{}{},
	}
	p := strconv.Itoa(port)

	// Loopback variants are always trusted: the dashboard's own requests
	// arrive as one of these regardless of which --host was bound.
	for _, h := range []string{"127.0.0.1", "localhost", "::1"} {
		g.addHostPort(h, p)
	}
	// A concrete --host (e.g. a LAN IP) joins the set so visiting the
	// dashboard at that address is same-origin and passes both checks.
	if host != "" && !IsWildcardHost(host) {
		g.addHostPort(host, p)
	}
	// Reverse-proxy / custom-domain escape hatch.
	for _, entry := range parseTrustedHosts(os.Getenv(TrustedHostsEnv)) {
		g.addTrusted(entry, p)
	}
	return g
}

// addHostPort registers a bare host + port, bracketing IPv6 literals via
// net.JoinHostPort, and the matching http/https origins.
func (g *Guard) addHostPort(host, port string) {
	hp := net.JoinHostPort(host, port)
	g.hosts[hp] = struct{}{}
	g.origins["http://"+hp] = struct{}{}
	g.origins["https://"+hp] = struct{}{}
}

// addTrusted registers an entry from CLAWFLOW_WEB_TRUSTED_HOSTS. Accepts
// "host", "host:port", or a full "scheme://host[:port]" — the scheme is
// stripped since we match Host (no scheme) and synthesize both origins.
func (g *Guard) addTrusted(entry, defaultPort string) {
	entry = strings.TrimSpace(entry)
	entry = strings.TrimPrefix(entry, "https://")
	entry = strings.TrimPrefix(entry, "http://")
	entry = strings.TrimSuffix(entry, "/")
	if entry == "" {
		return
	}
	if _, _, err := net.SplitHostPort(entry); err == nil {
		// Already carries an explicit port — trust it verbatim.
		g.hosts[entry] = struct{}{}
		g.origins["http://"+entry] = struct{}{}
		g.origins["https://"+entry] = struct{}{}
		return
	}
	// No port: trust both the bind-port form and the bare host (the
	// latter covers proxies terminating on standard 80/443).
	g.addHostPort(entry, defaultPort)
	g.hosts[entry] = struct{}{}
	g.origins["http://"+entry] = struct{}{}
	g.origins["https://"+entry] = struct{}{}
}

// HostAllowed reports whether the Host header may be served. Wildcard
// binds always pass. A missing Host is rejected when enforcing — HTTP/1.1
// requires it and browsers always send it, so absence signals a crafted
// request.
func (g *Guard) HostAllowed(host string) bool {
	if !g.enforceHost {
		return true
	}
	if host == "" {
		return false
	}
	_, ok := g.hosts[host]
	return ok
}

// OriginAllowed reports whether a cross-origin-capable request may
// proceed. An empty Origin is allowed: non-browser clients (curl, CLI
// scripts) omit it, and browsers attach it on exactly the cross-site
// requests we care about. A present Origin must match the whitelist.
func (g *Guard) OriginAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	_, ok := g.origins[origin]
	return ok
}

// Middleware wraps next with the Host (all requests) and Origin
// (state-changing requests only) checks.
func (g *Guard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.HostAllowed(r.Host) {
			http.Error(w, "forbidden: Host header not in whitelist (possible DNS-rebinding)", http.StatusForbidden)
			return
		}
		if isStateChanging(r.Method) && !g.OriginAllowed(r.Header.Get("Origin")) {
			http.Error(w, "forbidden: cross-origin request rejected", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isStateChanging is true for HTTP methods that mutate server state, the
// ones a CSRF attack would target. Safe methods (GET/HEAD/OPTIONS) skip
// the Origin check so same-origin asset loads and DNS-rebinding-blocked
// reads aren't affected by it (Host check still guards them).
func isStateChanging(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
		return true
	default:
		return false
	}
}

// IsWildcardHost reports whether host is an all-interfaces bind, where
// no single canonical Host can be pinned for rebinding protection.
func IsWildcardHost(host string) bool {
	return host == "" || host == "0.0.0.0" || host == "::"
}

// parseTrustedHosts splits a comma-separated env value into trimmed,
// non-empty entries.
func parseTrustedHosts(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
