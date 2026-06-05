package config_test

import (
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func TestParseRepoInput(t *testing.T) {
	cases := []struct {
		input        string
		gitlabHosts  []string
		wantOwner    string
		wantPlatform string
		wantBaseURL  string
		wantErr      bool
	}{
		// plain owner/repo → github
		{"owner/repo", []string{"gitlab.company.com"}, "owner/repo", "github", "", false},
		// github HTTPS URL
		{"https://github.com/owner/repo", []string{"gitlab.company.com"}, "owner/repo", "github", "", false},
		{"https://github.com/owner/repo.git", []string{"gitlab.company.com"}, "owner/repo", "github", "", false},
		// github SSH
		{"git@github.com:owner/repo.git", []string{"gitlab.company.com"}, "owner/repo", "github", "", false},
		// gitlab HTTPS — known host
		{"https://gitlab.company.com/ns/repo", []string{"gitlab.company.com"}, "ns/repo", "gitlab", "https://gitlab.company.com", false},
		// gitlab HTTPS — nested namespace
		{"https://gitlab.company.com/ns/group/repo", []string{"gitlab.company.com"}, "ns/group/repo", "gitlab", "https://gitlab.company.com", false},
		// gitlab HTTPS — unknown host (self-hosted), user-typed scheme preserved
		{"http://git.patsnap.com/devops/platform/insightgo.git", []string{"gitlab.company.com"}, "devops/platform/insightgo", "gitlab", "http://git.patsnap.com", false},
		// gitlab SSH
		{"git@gitlab.company.com:ns/group/repo.git", []string{"gitlab.company.com"}, "ns/group/repo", "gitlab", "https://gitlab.company.com", false},

		// --- issue #248: SSH branch must honor configured scheme/port ---
		// SSH host matches a configured http:// entry → base_url keeps http
		{"git@git.patsnap.com:devops/pop-web.git", []string{"http://git.patsnap.com"}, "devops/pop-web", "gitlab", "http://git.patsnap.com", false},
		// SSH host with bare-hostname entry → defaults to https
		{"git@git.patsnap.com:devops/pop-web.git", []string{"git.patsnap.com"}, "devops/pop-web", "gitlab", "https://git.patsnap.com", false},
		// SSH host with custom port entry → scheme and port preserved
		{"git@git.internal.com:ns/repo.git", []string{"http://git.internal.com:8080"}, "ns/repo", "gitlab", "http://git.internal.com:8080", false},
		// SSH host not in config → fallback https + host (backward compatible)
		{"git@git.unknown.com:ns/repo.git", []string{"http://git.patsnap.com"}, "ns/repo", "gitlab", "https://git.unknown.com", false},
		// HTTPS URL whose host matches an http:// config entry → config scheme wins
		{"https://git.patsnap.com/devops/pop-web", []string{"http://git.patsnap.com"}, "devops/pop-web", "gitlab", "http://git.patsnap.com", false},

		// invalid
		{"notarepo", []string{"gitlab.company.com"}, "", "", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			info, err := config.ParseRepoInput(tc.input, tc.gitlabHosts)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.OwnerRepo != tc.wantOwner {
				t.Errorf("OwnerRepo: got %q, want %q", info.OwnerRepo, tc.wantOwner)
			}
			if info.Platform != tc.wantPlatform {
				t.Errorf("Platform: got %q, want %q", info.Platform, tc.wantPlatform)
			}
			if info.BaseURL != tc.wantBaseURL {
				t.Errorf("BaseURL: got %q, want %q", info.BaseURL, tc.wantBaseURL)
			}
		})
	}
}
