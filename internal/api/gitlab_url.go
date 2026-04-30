package api

import "strings"

// normalizeGitLabURL ensures a GitLab host string becomes a full URL.
// Accepts:
//   - "gitlab.company.com" → "https://gitlab.company.com"
//   - "http://git.internal.com:8080" → "http://git.internal.com:8080"
//   - "https://gitlab.com" → "https://gitlab.com"
//
// This allows users to configure gitlab_hosts with either bare hostnames
// (defaulting to https) or full URLs with custom protocols and ports.
func normalizeGitLabURL(host string) string {
	host = strings.TrimSpace(host)
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return strings.TrimSuffix(host, "/")
	}
	return "https://" + host
}
