package pipeline

import "testing"

// A site-qualified config key must never reach a browsable link.
func TestRepoWebURLIgnoresSiteQualifier(t *testing.T) {
	got := RepoWebURL("b:team/tools/service", "gitlab", "https://gitlab-b.example.com:8443/gitlab")
	if want := "https://gitlab-b.example.com:8443/gitlab/team/tools/service"; got != want {
		t.Fatalf("RepoWebURL = %q, want %q", got, want)
	}
	if got := RepoWebURL("team/tools/service", "gitlab", "http://gitlab-a.example.com/"); got != "http://gitlab-a.example.com/team/tools/service" {
		t.Fatalf("legacy key link = %q", got)
	}
	if got := RepoWebURL("acme/service", "github", ""); got != "https://github.com/acme/service" {
		t.Fatalf("github link = %q", got)
	}
}
