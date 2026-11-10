package pipeline

import (
	"strconv"
	"strings"
)

// RepoWebURL returns the browsable root of a repository. baseURL is the
// configured instance URL: GitLab is browsed at the same root as its API,
// GitHub Enterprise serves its API under /api/v3 of the web host, and
// github.com serves its API from api.github.com.
func RepoWebURL(repo, platform, baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if platform == "gitlab" {
		if base == "" {
			base = "https://gitlab.com"
		}
		return base + "/" + repo
	}
	base = strings.TrimSuffix(base, "/api/v3")
	if base == "" || base == "https://api.github.com" {
		base = "https://github.com"
	}
	return base + "/" + repo
}

// IssueWebURL links an issue. The /issues/ path works on GitHub and on every
// GitLab version (modern GitLab redirects it to /-/issues/).
func IssueWebURL(root string, number int) string {
	return root + "/issues/" + strconv.Itoa(number)
}

// PRWebURL links a pull request or merge request.
func PRWebURL(root, platform string, number int) string {
	if platform == "gitlab" {
		return root + "/merge_requests/" + strconv.Itoa(number)
	}
	return root + "/pull/" + strconv.Itoa(number)
}
