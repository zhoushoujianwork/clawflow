package config

import (
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// GitLabSite is one registered GitLab connection. Sites are independent: adding,
// changing or removing one never touches another site's URL, credential,
// version or capability declaration.
//
// The token itself is never part of this struct. A site names its credential
// either through TokenEnv (an environment variable) or through the per-site
// entry gitlab_tokens.<name> of credentials.yaml.
type GitLabSite struct {
	// Name is the alias used in repository keys ("name:group/project") and in
	// --repo arguments. Lowercase letters, digits, '_' and '-'.
	Name string `yaml:"name" json:"name"`
	// URL is the GitLab root: scheme, host, non-default port and deployment
	// prefix, e.g. https://git.example.com:8443/gitlab. The REST API is
	// served under <URL>/api/v4.
	URL string `yaml:"url" json:"url"`
	// TokenEnv optionally names an environment variable holding this site's token.
	TokenEnv string `yaml:"token_env,omitempty" json:"token_env,omitempty"`
	// SSHHosts binds Git SSH addresses (host or host:port) to this site. A
	// site whose SSH host differs from its API host needs an entry here:
	// ClawFlow never guesses that mapping.
	SSHHosts []string `yaml:"ssh_hosts,omitempty" json:"ssh_hosts,omitempty"`
	// Version is the GitLab server version this site runs, for display.
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
	// Unsupported lists API capabilities (vcs.Cap*) this site lacks.
	Unsupported []string `yaml:"unsupported,omitempty" json:"unsupported,omitempty"`
	// UseLegacyToken lets this one site use the pre-existing single global
	// gitlab_token. It is an explicit opt-in made when an existing site is
	// registered; at most one site may set it, and no site receives the
	// legacy token otherwise.
	UseLegacyToken bool `yaml:"use_legacy_token,omitempty" json:"use_legacy_token,omitempty"`
}

// APIBase returns the REST API root of the site.
func (s GitLabSite) APIBase() string { return s.URL + "/api/v4" }

// Lacks reports whether the site declares capability unsupported.
func (s GitLabSite) Lacks(capability string) bool { return slices.Contains(s.Unsupported, capability) }

// GitLabSite returns the registered site called name.
func (s Settings) GitLabSite(name string) (GitLabSite, bool) {
	for _, site := range s.GitLabSites {
		if site.Name == name {
			return site, true
		}
	}
	return GitLabSite{}, false
}

// NormalizeSiteURL canonicalizes a GitLab root URL: lowercase scheme and host,
// default ports dropped, trailing slash and a trailing /api/v4 removed. A bare
// host means https. Credentials, queries and fragments are rejected so a site
// URL can be printed and logged safely.
func NormalizeSiteURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("GitLab site URL is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid GitLab site URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Hostname() == "" {
		return "", fmt.Errorf("GitLab site URL must be http(s)://host[:port][/prefix]")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(raw, "?") || strings.Contains(raw, "#") {
		return "", fmt.Errorf("GitLab site URL must not contain credentials, a query or a fragment")
	}
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	host := strings.ToLower(u.Hostname())
	hostport := host
	if strings.Contains(host, ":") {
		hostport = "[" + host + "]"
	}
	if port != "" {
		hostport = net.JoinHostPort(host, port)
	}
	path := strings.TrimRight(u.Path, "/")
	path = strings.TrimSuffix(path, "/api/v4")
	path = strings.TrimRight(path, "/")
	if strings.Contains(path, "//") {
		return "", fmt.Errorf("GitLab site URL has an empty path segment")
	}
	return scheme + "://" + hostport + path, nil
}

// NormalizeSSHHost canonicalizes a Git SSH address binding: "host" or
// "host:port", lowercase. An ssh:// prefix and a user@ part are accepted and dropped.
func NormalizeSSHHost(raw string) (string, error) {
	h := strings.TrimSpace(raw)
	h = strings.TrimPrefix(h, "ssh://")
	if i := strings.LastIndex(h, "@"); i >= 0 {
		h = h[i+1:]
	}
	h = strings.TrimRight(h, "/")
	if h == "" || strings.ContainsAny(h, "/ \t") {
		return "", fmt.Errorf("invalid SSH host %q: want host or host:port", raw)
	}
	host, port := h, ""
	if strings.HasPrefix(h, "[") || strings.Count(h, ":") == 1 {
		if hh, pp, err := net.SplitHostPort(h); err == nil {
			host, port = hh, pp
		} else if strings.HasPrefix(h, "[") {
			host = strings.Trim(h, "[]")
		}
	}
	if host == "" {
		return "", fmt.Errorf("invalid SSH host %q", raw)
	}
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid SSH port in %q", raw)
		}
		return net.JoinHostPort(strings.ToLower(host), port), nil
	}
	return strings.ToLower(host), nil
}

// sshHostName returns the host part of a normalized SSH binding.
func sshHostName(spec string) string {
	if h, _, err := net.SplitHostPort(spec); err == nil {
		return h
	}
	return spec
}

// NormalizeGitLabSite validates one site and returns its canonical form.
func NormalizeGitLabSite(s GitLabSite) (GitLabSite, error) {
	s.Name = strings.TrimSpace(s.Name)
	if !vcs.IsSiteAlias(s.Name) {
		return s, fmt.Errorf("invalid site name %q: use 1-32 lowercase letters, digits, '_' or '-' (starting with a letter or digit)", s.Name)
	}
	u, err := NormalizeSiteURL(s.URL)
	if err != nil {
		return s, fmt.Errorf("site %s: %w", s.Name, err)
	}
	s.URL = u
	s.TokenEnv = strings.TrimSpace(s.TokenEnv)
	if s.TokenEnv != "" && strings.ContainsAny(s.TokenEnv, " =\t\r\n\x00") {
		return s, fmt.Errorf("site %s: token_env must be an environment variable name", s.Name)
	}
	hosts := make([]string, 0, len(s.SSHHosts))
	for _, h := range s.SSHHosts {
		n, err := NormalizeSSHHost(h)
		if err != nil {
			return s, fmt.Errorf("site %s: %w", s.Name, err)
		}
		if !slices.Contains(hosts, n) {
			hosts = append(hosts, n)
		}
	}
	s.SSHHosts = hosts
	s.Version = strings.TrimSpace(s.Version)
	caps := make([]string, 0, len(s.Unsupported))
	for _, c := range s.Unsupported {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !vcs.ValidCapability(c) {
			return s, fmt.Errorf("site %s: unknown capability %q (known: %s)", s.Name, c, strings.Join(vcs.KnownCapabilities, ", "))
		}
		if !slices.Contains(caps, c) {
			caps = append(caps, c)
		}
	}
	slices.Sort(caps)
	s.Unsupported = caps
	return s, nil
}

// ValidateGitLabSites checks the whole site list: every site is valid, and
// names, URLs and SSH bindings are unique so no address can select two sites.
func (s Settings) ValidateGitLabSites() error {
	names := map[string]string{}
	urls := map[string]string{}
	ssh := map[string]string{}
	legacy := ""
	for _, site := range s.GitLabSites {
		n, err := NormalizeGitLabSite(site)
		if err != nil {
			return err
		}
		if _, dup := names[n.Name]; dup {
			return fmt.Errorf("duplicate GitLab site name %q", n.Name)
		}
		names[n.Name] = n.URL
		if n.UseLegacyToken {
			if legacy != "" {
				return fmt.Errorf("sites %q and %q both use the legacy GitLab token; only one site may", legacy, n.Name)
			}
			legacy = n.Name
		}
		if other, dup := urls[n.URL]; dup {
			return fmt.Errorf("GitLab sites %q and %q have the same URL %s", other, n.Name, n.URL)
		}
		urls[n.URL] = n.Name
		for _, h := range n.SSHHosts {
			if other, dup := ssh[h]; dup && other != n.Name {
				return fmt.Errorf("SSH host %s is bound to both site %q and site %q", h, other, n.Name)
			}
			ssh[h] = n.Name
		}
	}
	return nil
}

// UpsertGitLabSite adds or replaces the site with the same name after
// validating the resulting list. It touches no other site and no repository.
func (c *Config) UpsertGitLabSite(site GitLabSite) error {
	n, err := NormalizeGitLabSite(site)
	if err != nil {
		return err
	}
	next := append([]GitLabSite(nil), c.Settings.GitLabSites...)
	replaced := false
	for i := range next {
		if next[i].Name == n.Name {
			next[i], replaced = n, true
		}
	}
	if !replaced {
		next = append(next, n)
	}
	trial := c.Settings
	trial.GitLabSites = next
	if err := trial.ValidateGitLabSites(); err != nil {
		return err
	}
	c.Settings.GitLabSites = next
	return nil
}

// RemoveGitLabSite deletes a site. It refuses while a repository is bound to it.
func (c *Config) RemoveGitLabSite(name string) error {
	idx := -1
	for i, s := range c.Settings.GitLabSites {
		if s.Name == name {
			idx = i
		}
	}
	if idx < 0 {
		return fmt.Errorf("GitLab site %q is not configured", name)
	}
	var bound []string
	for key, r := range c.Repos {
		if r.Platform == "gitlab" && r.Site == name {
			bound = append(bound, key)
		}
	}
	if len(bound) > 0 {
		slices.Sort(bound)
		return fmt.Errorf("GitLab site %q still has repositories: %s (remove them first)", name, strings.Join(bound, ", "))
	}
	c.Settings.GitLabSites = slices.Delete(slices.Clone(c.Settings.GitLabSites), idx, idx+1)
	return nil
}

// BindGitLabRepos makes the site binding of GitLab repositories explicit in
// memory: a repository keyed "alias:path" is bound to that alias, and a legacy
// repository whose base_url equals a registered site's URL is bound to that
// site. A bound repository's base_url follows its site. Nothing is invented for
// repositories whose URL matches no registered site; they stay on their legacy
// site and never receive a registered site's credential.
func (c *Config) BindGitLabRepos() {
	for key, r := range c.Repos {
		if r.Platform != "gitlab" {
			continue
		}
		alias := r.Site
		if alias == "" {
			alias, _ = vcs.SplitRepoKey(key)
		}
		var site GitLabSite
		ok := false
		if alias != "" {
			site, ok = c.Settings.GitLabSite(alias)
		} else if r.BaseURL != "" {
			if norm, err := NormalizeSiteURL(r.BaseURL); err == nil {
				for _, s := range c.Settings.GitLabSites {
					if s.URL == norm {
						site, ok = s, true
					}
				}
			}
		}
		if !ok {
			continue
		}
		site2 := site
		r.Site, r.BaseURL, r.SiteCfg = site.Name, site.URL, &site2
		if r.ProjectPath == "" {
			_, r.ProjectPath = vcs.SplitRepoKey(key)
		}
		c.Repos[key] = r
	}
}
