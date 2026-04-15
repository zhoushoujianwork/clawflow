package config_test

import (
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

func TestParseRepoInput(t *testing.T) {
	gitlabHosts := []string{"gitlab.company.com"}

	cases := []struct {
		input       string
		wantOwner   string
		wantPlatform string
		wantBaseURL string
		wantErr     bool
	}{
		// plain owner/repo → github
		{"owner/repo", "owner/repo", "github", "", false},
		// github HTTPS URL
		{"https://github.com/owner/repo", "owner/repo", "github", "", false},
		{"https://github.com/owner/repo.git", "owner/repo", "github", "", false},
		// github SSH
		{"git@github.com:owner/repo.git", "owner/repo", "github", "", false},
		// gitlab HTTPS — known host
		{"https://gitlab.company.com/ns/repo", "ns/repo", "gitlab", "https://gitlab.company.com", false},
		// gitlab HTTPS — nested namespace
		{"https://gitlab.company.com/ns/group/repo", "ns/group/repo", "gitlab", "https://gitlab.company.com", false},
		// gitlab HTTPS — unknown host (self-hosted)
		{"http://git.patsnap.com/devops/platform/insightgo.git", "devops/platform/insightgo", "gitlab", "http://git.patsnap.com", false},
		// gitlab SSH
		{"git@gitlab.company.com:ns/group/repo.git", "ns/group/repo", "gitlab", "https://gitlab.company.com", false},
		// invalid
		{"notarepo", "", "", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			info, err := config.ParseRepoInput(tc.input, gitlabHosts)
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
