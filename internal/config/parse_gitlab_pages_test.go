package config

import "testing"

// A GitLab page link resolves to its project. GitLab 18+ returns
// ".../-/work_items/N" as an issue's web_url, so users paste it as --repo.
func TestParseRepoInputGitLabPageLinks(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://gitlab.example.test/grp/smoke", "grp/smoke"},
		{"https://gitlab.example.test/grp/smoke/-/work_items/1", "grp/smoke"},
		{"https://gitlab.example.test/grp/smoke/-/issues/1", "grp/smoke"},
		{"https://gitlab.example.test/grp/smoke/-/merge_requests/3", "grp/smoke"},
		{"https://gitlab.example.test/grp/smoke/-/jobs/9", "grp/smoke"},
		{"https://gitlab.example.test/grp/sub/smoke/-/work_items/7", "grp/sub/smoke"},
		{"https://gitlab.example.test/grp/smoke/-", "grp/smoke"},
		{"https://gitlab.example.test/grp/smoke.git", "grp/smoke"},
	} {
		info, err := ParseRepoInput(tc.in, nil)
		if err != nil || info.OwnerRepo != tc.want || info.Platform != "gitlab" || info.BaseURL != "https://gitlab.example.test" {
			t.Errorf("%s -> %+v err=%v, want project %q", tc.in, info, err, tc.want)
		}
	}
	// A link that names no project is still rejected rather than guessed.
	if _, err := ParseRepoInput("https://gitlab.example.test/-/work_items/1", nil); err == nil {
		t.Error("a link without a project path must be rejected")
	}
}
