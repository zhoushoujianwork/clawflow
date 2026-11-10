package config

import (
	"os"
	"strings"
)

// siteTokenEnv is the conventional per-site environment variable:
// GITLAB_TOKEN_<NAME> with '-' replaced by '_' and upper-cased.
func siteTokenEnv(name string) string {
	return "GITLAB_TOKEN_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// SiteToken returns the credential of a registered site and where it came from
// ("" when none is configured). Lookup order: the site's token_env variable,
// GITLAB_TOKEN_<NAME>, gitlab_tokens.<name> of credentials.yaml, and the legacy
// global token only for the one site that explicitly opted in. A site never
// borrows another site's token or the legacy token implicitly.
func (c *Credentials) SiteToken(site GitLabSite) (token, source string) {
	if c == nil {
		return "", ""
	}
	if site.TokenEnv != "" {
		if v := os.Getenv(site.TokenEnv); v != "" {
			return v, "env " + site.TokenEnv
		}
	}
	if v := os.Getenv(siteTokenEnv(site.Name)); v != "" {
		return v, "env " + siteTokenEnv(site.Name)
	}
	if v := c.GitLabTokens[site.Name]; v != "" {
		return v, "credentials.yaml gitlab_tokens." + site.Name
	}
	if site.UseLegacyToken && c.GitLabToken != "" {
		return c.GitLabToken, "legacy gitlab_token (explicit opt-in)"
	}
	return "", ""
}

// SetSiteToken stores a site's token; an empty token removes it.
func (c *Credentials) SetSiteToken(name, token string) {
	if token == "" {
		delete(c.GitLabTokens, name)
		return
	}
	if c.GitLabTokens == nil {
		c.GitLabTokens = map[string]string{}
	}
	c.GitLabTokens[name] = token
}

// GitLabCredential returns the credential for this repository: its own site's
// token for a repository bound to a registered site, the legacy global token for
// a legacy repository. The result never comes from a different site.
func (r Repo) GitLabCredential(creds *Credentials) string {
	if creds == nil {
		return ""
	}
	if r.Site == "" {
		return creds.GitLabToken
	}
	if r.SiteCfg == nil {
		return ""
	}
	t, _ := creds.SiteToken(*r.SiteCfg)
	return t
}
