package commands

import (
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// The remote repo-admin audit resolves arguments with the CLI's rules, so a full
// URL on a site with a deployment prefix and an SSH alias record the registered
// site and the project path without the prefix.
func TestPlatformAdminAuditRepoUsesSharedResolution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", "")
	cfg := &config.Config{Repos: map[string]config.Repo{"acme/service": {Platform: "github", Enabled: true}}}
	if err := cfg.UpsertGitLabSite(config.GitLabSite{Name: "b", URL: "https://gitlab-b.example.com:8443/gitlab", SSHHosts: []string{"ssh.gitlab-b.example.com:2222"}}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	a := &platformAdmin{}
	for _, in := range []string{
		"https://gitlab-b.example.com:8443/gitlab/team/tools/service",
		"ssh://git@ssh.gitlab-b.example.com:2222/team/tools/service.git",
		"b:team/tools/service",
	} {
		got := a.AuditRepo(in)
		for _, want := range []string{"gitlab", "site=b", "https://gitlab-b.example.com:8443/gitlab", " team/tools/service"} {
			if !strings.Contains(got, want) {
				t.Errorf("AuditRepo(%q) = %q, lacks %q", in, got, want)
			}
		}
		if strings.Contains(got, "gitlab/team") {
			t.Errorf("AuditRepo(%q) = %q, deployment prefix leaked into the project path", in, got)
		}
	}
	// GitHub, filesystem paths and unknown sites fall back to the syntactic target.
	for _, in := range []string{"acme/service", "/tmp/checkout", "../x/y", "unknown:team/tools/service", ""} {
		if got := a.AuditRepo(in); got != "" {
			t.Errorf("AuditRepo(%q) = %q, want empty", in, got)
		}
	}
}
