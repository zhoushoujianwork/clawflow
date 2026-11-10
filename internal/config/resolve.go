package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// Target is the result of resolving a user-supplied repository argument. Every
// command, API handler and the App resolve through Config.Resolve, so one
// argument always selects the same platform, site and project.
type Target struct {
	Key        string // config map key: "owner/repo", "group/proj" (legacy GitLab) or "alias:group/proj"
	Platform   string // "github" or "gitlab"
	Site       string // registered GitLab site alias; empty for GitHub and legacy sites
	Path       string // project path inside the platform/site
	BaseURL    string // GitLab site root (empty for github.com)
	Repo       Repo   // the stored entry, or a transient one when !Configured
	Configured bool
}

// Describe renders platform, site and full path for messages and audit text.
func (t Target) Describe() string { return DescribeRepo(t.Key, t.Repo) }

// DescribeRepo renders "github owner/repo" or "gitlab site=<alias> <url> <path>".
func DescribeRepo(key string, r Repo) string {
	if r.Platform != "gitlab" {
		return "github " + key
	}
	site := r.Site
	if site == "" {
		site = "(legacy)"
	}
	path := r.ProjectPath
	if path == "" {
		_, path = vcs.SplitRepoKey(key)
	}
	return fmt.Sprintf("gitlab site=%s %s %s", site, r.BaseURL, path)
}

// AmbiguousError reports a shorthand that matches several configured
// repositories. Nothing is guessed: the caller must name one of them.
type AmbiguousError struct {
	Input      string
	Candidates []string // config keys, sorted
	Described  []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("repository %q is ambiguous; it matches %d configured repositories: %s. Pass the full project URL or one of the keys %s (see: clawflow repo list)",
		e.Input, len(e.Candidates), strings.Join(e.Described, "; "), strings.Join(e.Candidates, ", "))
}

// projectPathOf returns the project path of a configured GitLab entry.
func projectPathOf(key string, r Repo) string {
	if r.ProjectPath != "" {
		return r.ProjectPath
	}
	_, p := vcs.SplitRepoKey(key)
	return p
}

func validProjectPath(p string) bool {
	parts := strings.Split(p, "/")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
				return false
			}
		}
	}
	return true
}

func (c *Config) transientGitHub(path string) (Target, error) {
	parts := strings.Split(path, "/")
	if len(parts) != 2 || !validProjectPath(path) {
		return Target{}, fmt.Errorf("repo must be owner/repo, a full project URL, or alias:group/project, got %q", path)
	}
	return Target{Key: path, Platform: "github", Path: path, Repo: Repo{Platform: "github", Owner: parts[0]}}, nil
}

func (c *Config) siteTarget(site GitLabSite, path string) (Target, error) {
	if !validProjectPath(path) {
		return Target{}, fmt.Errorf("URL does not contain a valid project path for GitLab site %s", site.Name)
	}
	for key, r := range c.Repos {
		if r.Platform == "gitlab" && r.Site == site.Name && projectPathOf(key, r) == path {
			return Target{Key: key, Platform: "gitlab", Site: site.Name, Path: path, BaseURL: site.URL, Repo: r, Configured: true}, nil
		}
	}
	s := site
	key := site.Name + ":" + path
	return Target{Key: key, Platform: "gitlab", Site: site.Name, Path: path, BaseURL: site.URL, Repo: Repo{
		Platform: "gitlab", BaseURL: site.URL, Site: site.Name, SiteCfg: &s, ProjectPath: path, Owner: strings.SplitN(path, "/", 2)[0],
	}}, nil
}

// legacySite is an implicit, unregistered GitLab site known from a stored
// repository's base_url or the legacy gitlab_hosts list.
type legacySite struct{ URL string }

func (c *Config) legacySites() []legacySite {
	seen := map[string]bool{}
	var out []legacySite
	add := func(raw string) {
		n, err := NormalizeSiteURL(raw)
		if err != nil || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, legacySite{URL: n})
	}
	keys := make([]string, 0, len(c.Repos))
	for k := range c.Repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if r := c.Repos[k]; r.Platform == "gitlab" && r.Site == "" && r.BaseURL != "" {
			add(r.BaseURL)
		}
	}
	for _, h := range c.Settings.GitLabHosts {
		add(h)
	}
	return out
}

func (c *Config) legacyTarget(l legacySite, path string) (Target, error) {
	if !validProjectPath(path) {
		return Target{}, fmt.Errorf("URL does not contain a valid project path")
	}
	for key, r := range c.Repos {
		if r.Platform != "gitlab" || r.Site != "" || projectPathOf(key, r) != path {
			continue
		}
		if n, err := NormalizeSiteURL(r.BaseURL); err == nil && n == l.URL {
			return Target{Key: key, Platform: "gitlab", Path: path, BaseURL: l.URL, Repo: r, Configured: true}, nil
		}
	}
	return Target{Key: path, Platform: "gitlab", Path: path, BaseURL: l.URL, Repo: Repo{
		Platform: "gitlab", BaseURL: l.URL, ProjectPath: path, Owner: strings.SplitN(path, "/", 2)[0],
	}}, nil
}

func (c *Config) unregisteredSite(what string) error {
	return fmt.Errorf("%s does not belong to any registered GitLab site; register it first: clawflow gitlab site add <name> --url <https://host[:port][/prefix]> (registered: %s)", what, c.siteNames())
}

func (c *Config) siteNames() string {
	if len(c.Settings.GitLabSites) == 0 {
		return "none"
	}
	names := make([]string, 0, len(c.Settings.GitLabSites))
	for _, s := range c.Settings.GitLabSites {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func isLocalRepoPath(in string) bool {
	return in == "." || strings.HasPrefix(in, "/") || strings.HasPrefix(in, "~/") || strings.HasPrefix(in, "./")
}

// Resolve maps a repository argument to exactly one target or fails. Accepted:
//
//	owner/repo                  GitHub, or the single configured GitLab project with that path
//	alias:group/sub/project     a project on a registered GitLab site
//	https://host[:port][/prefix]/group/sub/project[/-/...]
//	git@host:group/project.git, ssh://git@host[:port]/group/project.git
//	/path/to/checkout           the checkout's origin remote
//
// A shorthand with several candidates yields *AmbiguousError. No input falls
// back to another platform or site, and no credential is ever chosen here.
func (c *Config) Resolve(input string) (Target, error) {
	in := strings.TrimSpace(input)
	if in == "" {
		return Target{}, fmt.Errorf("repository is empty")
	}
	if isLocalRepoPath(in) {
		dir := in
		if strings.HasPrefix(dir, "~/") {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, dir[2:])
		}
		remote, err := ReadGitRemoteURL(dir)
		if err != nil {
			return Target{}, fmt.Errorf("cannot read git remote from %q: %w", in, err)
		}
		in = remote
	}
	in = strings.TrimSuffix(in, ".git")
	switch {
	case strings.HasPrefix(in, "git@") || strings.HasPrefix(in, "ssh://"):
		return c.resolveSSH(in)
	case strings.Contains(in, "://"):
		return c.resolveURL(in)
	}
	return c.resolveShort(in)
}

// ResolveConfigured is Resolve for commands that act on an existing entry.
func (c *Config) ResolveConfigured(input string) (Target, error) {
	t, err := c.Resolve(input)
	if err != nil {
		return t, err
	}
	if !t.Configured {
		return t, fmt.Errorf("repo %q not found in config (%s)", input, t.Describe())
	}
	return t, nil
}

func (c *Config) resolveShort(in string) (Target, error) {
	if alias, path := vcs.SplitRepoKey(in); alias != "" {
		site, ok := c.Settings.GitLabSite(alias)
		if !ok {
			return Target{}, fmt.Errorf("unknown GitLab site %q in %q (registered: %s)", alias, in, c.siteNames())
		}
		return c.siteTarget(site, path)
	}
	if strings.Contains(in, ":") {
		return Target{}, fmt.Errorf("invalid repository %q", in)
	}
	var keys []string
	for key, r := range c.Repos {
		if r.Platform == "gitlab" {
			if projectPathOf(key, r) == in {
				keys = append(keys, key)
			}
		} else if key == in {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	switch len(keys) {
	case 1:
		r := c.Repos[keys[0]]
		t := Target{Key: keys[0], Platform: "github", Path: in, Repo: r, Configured: true}
		if r.Platform == "gitlab" {
			t.Platform, t.Site, t.BaseURL = "gitlab", r.Site, r.BaseURL
		}
		return t, nil
	case 0:
		if strings.Count(in, "/") == 1 {
			return c.transientGitHub(in)
		}
		return Target{}, fmt.Errorf("repo %q is not configured; a nested path is a GitLab project, so pass its full URL or alias:path (registered sites: %s)", in, c.siteNames())
	}
	e := &AmbiguousError{Input: in, Candidates: keys}
	for _, k := range keys {
		e.Described = append(e.Described, DescribeRepo(k, c.Repos[k]))
	}
	return Target{}, e
}

func (c *Config) resolveURL(in string) (Target, error) {
	u, err := url.Parse(in)
	if err != nil {
		return Target{}, fmt.Errorf("invalid repository URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return Target{}, fmt.Errorf("repository URL must be http(s)://host[:port]/group/project")
	}
	if u.User != nil {
		return Target{}, fmt.Errorf("repository URL must not contain credentials")
	}
	path := strings.Trim(u.Path, "/")
	if i := strings.Index("/"+path, "/-/"); i >= 0 {
		path = strings.Trim(("/" + path)[:i], "/")
	}
	host := strings.ToLower(u.Hostname())
	if host == "github.com" || host == "www.github.com" {
		parts := strings.Split(path, "/")
		if len(parts) < 2 {
			return Target{}, fmt.Errorf("URL does not contain owner/repo path")
		}
		return c.githubTarget(parts[0] + "/" + parts[1])
	}
	origin, err := NormalizeSiteURL(u.Scheme + "://" + u.Host)
	if err != nil {
		return Target{}, err
	}
	full := origin + "/" + path

	// Registered sites: the longest URL prefix on a segment boundary wins, so
	// a site mounted at /gitlab is preferred over one at the host root.
	var best *GitLabSite
	for i := range c.Settings.GitLabSites {
		s := &c.Settings.GitLabSites[i]
		if full == s.URL || strings.HasPrefix(full, s.URL+"/") {
			if best == nil || len(s.URL) > len(best.URL) {
				best = s
			}
		}
	}
	if best != nil {
		return c.siteTarget(*best, strings.Trim(strings.TrimPrefix(full, best.URL), "/"))
	}

	legacy := c.legacySites()
	var matches []legacySite
	for _, l := range legacy {
		if full == l.URL || strings.HasPrefix(full, l.URL+"/") {
			matches = append(matches, l)
		}
	}
	if len(matches) == 0 {
		// Legacy entries match by hostname so an http-only instance is honored
		// even when an https URL is typed (existing gitlab_hosts behavior).
		for _, l := range legacy {
			if lu, err := url.Parse(l.URL); err == nil && strings.EqualFold(lu.Hostname(), host) && (lu.Path == "" || strings.HasPrefix("/"+path, lu.Path+"/")) {
				matches = append(matches, l)
			}
		}
	}
	switch len(matches) {
	case 1:
		l := matches[0]
		lu, _ := url.Parse(l.URL)
		return c.legacyTarget(l, strings.Trim(strings.TrimPrefix("/"+path, lu.Path), "/"))
	case 0:
		if len(legacy) == 0 && len(c.Settings.GitLabSites) == 0 {
			// Nothing GitLab is configured yet: the typed origin is the one site.
			return c.legacyTarget(legacySite{URL: origin}, path)
		}
		return Target{}, c.unregisteredSite(origin)
	}
	urls := make([]string, 0, len(matches))
	for _, m := range matches {
		urls = append(urls, m.URL)
	}
	return Target{}, fmt.Errorf("URL %s matches several legacy GitLab sites (%s); register them with clawflow gitlab site add", origin, strings.Join(urls, ", "))
}

func (c *Config) githubTarget(path string) (Target, error) {
	if r, ok := c.Repos[path]; ok {
		if r.Platform == "gitlab" {
			return Target{}, fmt.Errorf("%q is configured as a GitLab project (%s), not a GitHub repository", path, DescribeRepo(path, r))
		}
		return Target{Key: path, Platform: "github", Path: path, Repo: r, Configured: true}, nil
	}
	return c.transientGitHub(path)
}

func (c *Config) resolveSSH(in string) (Target, error) {
	var host, port, path string
	if rest, ok := strings.CutPrefix(in, "ssh://"); ok {
		u, err := url.Parse("ssh://" + rest)
		if err != nil || u.Hostname() == "" {
			return Target{}, fmt.Errorf("invalid SSH URL")
		}
		host, port, path = strings.ToLower(u.Hostname()), u.Port(), strings.Trim(u.Path, "/")
	} else {
		rest := strings.TrimPrefix(in, "git@")
		i := strings.Index(rest, ":")
		if i < 0 {
			return Target{}, fmt.Errorf("invalid SSH URL")
		}
		host, path = strings.ToLower(rest[:i]), strings.Trim(rest[i+1:], "/")
	}
	if host == "github.com" {
		parts := strings.Split(path, "/")
		if len(parts) < 2 {
			return Target{}, fmt.Errorf("SSH URL does not contain owner/repo path")
		}
		return c.githubTarget(parts[0] + "/" + parts[1])
	}
	spec := host
	if port != "" {
		spec = net.JoinHostPort(host, port)
	}
	// 1. An explicit ssh_hosts binding is the only way an SSH address that
	//    differs from the API address selects a site.
	for i := range c.Settings.GitLabSites {
		for _, h := range c.Settings.GitLabSites[i].SSHHosts {
			if h == spec {
				return c.siteTarget(c.Settings.GitLabSites[i], path)
			}
		}
	}
	// 2. The SSH host is the site's own API host.
	var same []GitLabSite
	for _, s := range c.Settings.GitLabSites {
		if su, err := url.Parse(s.URL); err == nil && strings.EqualFold(su.Hostname(), host) {
			same = append(same, s)
		}
	}
	switch len(same) {
	case 1:
		return c.siteTarget(same[0], path)
	default:
		if len(same) > 1 {
			names := make([]string, 0, len(same))
			for _, s := range same {
				names = append(names, s.Name)
			}
			return Target{}, fmt.Errorf("SSH host %s matches several GitLab sites (%s); bind it to one with: clawflow gitlab site set <name> --ssh-host %s", spec, strings.Join(names, ", "), spec)
		}
	}
	// 3. A legacy site with the same host.
	var legacy []legacySite
	for _, l := range c.legacySites() {
		if lu, err := url.Parse(l.URL); err == nil && strings.EqualFold(lu.Hostname(), host) {
			legacy = append(legacy, l)
		}
	}
	if len(legacy) == 1 {
		return c.legacyTarget(legacy[0], path)
	}
	if len(legacy) > 1 {
		return Target{}, fmt.Errorf("SSH host %s matches several GitLab sites; register them and bind it with: clawflow gitlab site set <name> --ssh-host %s", spec, spec)
	}
	if len(c.legacySites()) == 0 && len(c.Settings.GitLabSites) == 0 {
		return c.legacyTarget(legacySite{URL: "https://" + host}, path)
	}
	return Target{}, fmt.Errorf("SSH address %s is not bound to any GitLab site; bind it explicitly: clawflow gitlab site set <name> --ssh-host %s (registered: %s)", spec, spec, c.siteNames())
}

// TargetOf describes a stored entry as a Target without touching the network.
func TargetOf(key string, r Repo) Target {
	t := Target{Key: key, Platform: "github", Path: key, Repo: r, Configured: true}
	if r.Platform == "gitlab" {
		t.Platform, t.Site, t.BaseURL, t.Path = "gitlab", r.Site, r.BaseURL, projectPathOf(key, r)
	}
	return t
}
