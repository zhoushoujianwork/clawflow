package commands

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// A shorthand shared by a legacy bare-key GitLab entry and a registered-site
// entry is ambiguous for `run --repo` too. The bare key being a literal config
// key must not make it win.
func TestRunRepoShorthandSharedByTwoSitesIsAmbiguous(t *testing.T) {
	a, b := newSiteServer(t, "token-a"), newSiteServer(t, "token-b")
	twoSiteHome(t, a, b)

	cmd := NewRunCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--local", "--repo", "team/tools/service"})
	err := cmd.ExecuteContext(context.Background())
	var amb *config.AmbiguousError
	if !errors.As(err, &amb) {
		t.Fatalf("run --repo with an ambiguous shorthand: want AmbiguousError, got %v", err)
	}
	if len(amb.Candidates) != 2 {
		t.Errorf("candidates = %v, want both sites", amb.Candidates)
	}
	if n := len(a.requests()) + len(b.requests()); n != 0 {
		t.Errorf("an ambiguous --repo reached a site: %v %v", a.requests(), b.requests())
	}
}

func TestResolveRunRepoSelectsExactlyOneEntry(t *testing.T) {
	a, b := newSiteServer(t, "token-a"), newSiteServer(t, "token-b")
	twoSiteHome(t, a, b)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	for input, want := range map[string]string{
		"b:team/tools/service":            "b:team/tools/service", // explicit site alias
		"a:team/tools/service":            "team/tools/service",   // legacy bare key bound to site a
		"acme/service":                    "acme/service",         // unique GitHub shorthand
		b.srv.URL + "/team/tools/service": "b:team/tools/service", // full project URL
	} {
		got, err := resolveRunRepo(cfg, input)
		if err != nil || got != want {
			t.Errorf("resolveRunRepo(%q) = %q, %v; want %q", input, got, err, want)
		}
	}

	var amb *config.AmbiguousError
	if _, err := resolveRunRepo(cfg, "team/tools/service"); !errors.As(err, &amb) {
		t.Errorf("shorthand shared by two sites: want AmbiguousError, got %v", err)
	}
	if _, err := resolveRunRepo(cfg, "unknown/nested/path"); err == nil {
		t.Error("an unconfigured nested path was accepted")
	}
}
