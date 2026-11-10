package commands

import (
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// The App row for a registered-site project carries the same platform/site/path
// text as `clawflow repo list`, and links to the project path without the alias.
func TestDashboardRepoShowsSiteIdentityAndLinksByPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAWFLOW_STATE_DIR", "")
	r := config.Repo{Platform: "gitlab", Site: "b", ProjectPath: "team/tools/service", BaseURL: "https://gitlab-b.example.com:8443/gitlab", Enabled: true}
	row := dashboardRepo("b:team/tools/service", r)
	if row.Name != "b:team/tools/service" || row.Site != "b" {
		t.Fatalf("row identity: %+v", row)
	}
	if row.URL != "https://gitlab-b.example.com:8443/gitlab/team/tools/service" {
		t.Errorf("link = %q", row.URL)
	}
	if want := config.DescribeRepo("b:team/tools/service", r); row.Target != want {
		t.Errorf("target = %q, want the CLI text %q", row.Target, want)
	}
	for _, want := range []string{"site=b", "team/tools/service"} {
		if !strings.Contains(row.Target, want) {
			t.Errorf("target lacks %q: %s", want, row.Target)
		}
	}
	gh := dashboardRepo("acme/service", config.Repo{Platform: "github"})
	if gh.Site != "" || gh.Target != "github acme/service" || gh.URL != "https://github.com/acme/service" {
		t.Errorf("github row: %+v", gh)
	}
}
