package webguard

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"path"
	"strings"
)

// RemotePolicy limits LAN clients using only the transport peer, never proxy headers.
type RemotePolicy struct {
	readonly bool
	networks []*net.IPNet
}

func NewRemotePolicy(mode string, cidrs []string) (*RemotePolicy, error) {
	if mode != "off" && mode != "readonly" {
		return nil, fmt.Errorf("remote must be off or readonly")
	}
	p := &RemotePolicy{readonly: mode == "readonly"}
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid allow-cidr %q: %w", cidr, err)
		}
		p.networks = append(p.networks, network)
	}
	if len(cidrs) > 0 && !p.readonly {
		return nil, fmt.Errorf("allow-cidr requires remote readonly")
	}
	return p, nil
}

func peerIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return nil
	}
	// ParseAddr accepts IPv6 zones from TCP peers; policy checks use only
	// the address bytes, since a zone identifies the local interface.
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}
	return net.IP(addr.AsSlice())
}

func (p *RemotePolicy) ReadOnly(r *http.Request) bool {
	return p.readonly && !peerIP(r).IsLoopback()
}

func (p *RemotePolicy) Session(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]bool{"readonly": p.ReadOnly(r)})
}

func (p *RemotePolicy) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.ReadOnly(r) {
			ip := peerIP(r)
			allowed := ip != nil && len(p.networks) == 0
			for _, network := range p.networks {
				allowed = allowed || network.Contains(ip)
			}
			reason := ""
			if !allowed {
				reason = "remote peer is not allowed"
			} else if (r.Method != http.MethodGet && r.Method != http.MethodHead) || !remoteReadPath(r.URL.Path) {
				reason = "read-only session: endpoint is not allowed"
			}
			if reason != "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": reason})
				return
			}
		}
		if p.ReadOnly(r) && r.Method == http.MethodHead {
			read := r.Clone(r.Context())
			read.Method = http.MethodGet
			next.ServeHTTP(headResponseWriter{w}, read)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Keep this list explicit: new handlers and runtime files are private by default.
func remoteReadPath(p string) bool {
	if path.Clean(p) != p || strings.Contains(p, "\\") {
		return false
	}
	switch p {
	case "/api/session", "/api/pipeline", "/api/run/status", "/api/runs", "/api/usage",
		"/data/meta.json", "/data/repos.json", "/data/pending.json", "/data/issues.json",
		"/", "/index.html", "/dashboard", "/pipeline", "/usage", "/favicon.svg", "/logo.svg":
		return true
	}
	return strings.HasPrefix(p, "/assets/")
}

// Existing JSON handlers accept GET only; HEAD runs the same read without a body.
type headResponseWriter struct{ http.ResponseWriter }

func (w headResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
