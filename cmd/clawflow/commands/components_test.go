package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/component"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func TestComponentsCLILifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "components.json")
	a := component.ReadAdapter{Descriptor: component.Descriptor{ID: "test", Adapter: "test", Version: "1", Reference: "test", Capabilities: []string{"test"}}, Observe: func(context.Context) (component.Observation, error) {
		return component.Observation{Identity: "test", Version: "1", Protocol: component.Schema, Health: "available", Capabilities: []string{"test"}}, nil
	}}
	run := func(args ...string) error {
		c := newComponentsCmd(path, []component.Adapter{a}, func(p string) bool { return p == "example" }, componentCommandAccess{credentials: func(string, component.Binding) (string, error) { return "test-secret", nil }})
		c.SetArgs(args)
		c.SetOut(&bytes.Buffer{})
		c.SetErr(&bytes.Buffer{})
		return c.Execute()
	}
	if err := run("discover"); err != nil {
		t.Fatal(err)
	}
	if err := run("list"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("read created inventory")
	}
	if run("register", "unknown") == nil {
		t.Fatal("unknown registered")
	}
	for _, args := range [][]string{{"register", "test"}, {"verify", "test"}, {"bind", "test", "--project", "example", "--role", "test", "--resource", "repo", "--capability", "test", "--credential-ref", "projects/example/credentials/test"}, {"activate", "test", "--project", "example", "--role", "test"}} {
		if err := run(args...); err != nil {
			t.Fatal(args, err)
		}
	}
	r, err := component.Load(path)
	if err != nil || r.Records["test"].Bindings[0].State != "active" {
		t.Fatal(r, err)
	}
	if run("activate", "test", "--project", "../example", "--role", "test") == nil {
		t.Fatal("invalid project")
	}
	if run("bind", "test", "--project", "missing", "--role", "test", "--resource", "repo", "--capability", "test") == nil {
		t.Fatal("missing project")
	}
	if err = run("disable", "test", "--project", "example", "--role", "test"); err != nil {
		t.Fatal(err)
	}
}

type componentEvidenceFixture struct {
	head  string
	calls int
}

func (f *componentEvidenceFixture) GetRepoID(repo string) (int64, error) {
	f.calls++
	if repo != "owner/repo" {
		return 0, component.ErrRefused
	}
	return 123, nil
}
func (f *componentEvidenceFixture) GetCIEvidence(string, int) (vcs.CIEvidence, error) {
	f.calls++
	return vcs.CIEvidence{HeadSHA: f.head, Status: vcs.CIStatusSuccess, CheckIDs: []int64{456}}, nil
}

func TestComponentsGitHubBindingAndCredentialRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "components.json")
	grantsPath := filepath.Join(t.TempDir(), "access.json")
	secret := "test-project-private-token"
	t.Setenv("COMPONENT_CI_TOKEN", secret)
	t.Setenv("GITHUB_TOKEN", secret) // A global token must not rescue a revoked project slot.
	grants := component.CredentialGrants{Schema: component.Schema, Grants: []component.CredentialGrant{{Component: "github-actions", Project: "example", Role: "test", Resource: "owner/repo", CredentialRef: "projects/example/credentials/test", Capabilities: []string{"exact-revision-ci"}, Environment: "COMPONENT_CI_TOKEN"}}}
	data, _ := json.Marshal(grants)
	if err := os.WriteFile(grantsPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	source := &componentEvidenceFixture{head: strings.Repeat("a", 40)}
	access := componentCommandAccess{credentials: component.FileCredentials(grantsPath), github: func(ctx context.Context, token string) component.GitHubEvidenceSource {
		if token != secret {
			t.Fatal("wrong credential resolved")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("unbounded provider request")
		}
		return source
	}}
	run := func(args ...string) error {
		c := newComponentsCmd(path, component.Builtins(func() (execution.Config, error) { t.Fatal("unrelated execution probe"); return execution.Config{}, nil }), func(p string) bool { return p == "example" || p == "other" }, access)
		c.SetArgs(args)
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetErr(&out)
		err := c.Execute()
		if strings.Contains(out.String(), secret) || err != nil && strings.Contains(err.Error(), secret) {
			t.Fatal("credential in output")
		}
		return err
	}
	if err := run("discover"); err != nil || source.calls != 0 {
		t.Fatal("discovery contacted provider", err)
	}
	for _, args := range [][]string{
		{"register", "github-actions"},
		{"verify", "github-actions", "--project", "example", "--role", "test", "--resource", "owner/repo", "--credential-ref", "projects/example/credentials/test", "--pr", "1", "--sha", source.head},
		{"bind", "github-actions", "--project", "example", "--role", "test", "--resource", "owner/repo", "--credential-ref", "projects/example/credentials/test", "--capability", "exact-revision-ci"},
		{"activate", "github-actions", "--project", "example", "--role", "test", "--pr", "1", "--sha", source.head},
		{"check", "github-actions", "--project", "example", "--role", "test", "--pr", "1", "--sha", source.head, "--capability", "exact-revision-ci"},
	} {
		if err := run(args...); err != nil {
			t.Fatal(args, err)
		}
	}
	if source.calls != 6 {
		t.Fatalf("expected three real adapter probes; calls=%d", source.calls)
	}
	if run("bind", "github-actions", "--project", "other", "--role", "test", "--resource", "owner/repo", "--credential-ref", "projects/other/credentials/test", "--capability", "exact-revision-ci") == nil {
		t.Fatal("other project inherited credentials")
	}
	t.Setenv("COMPONENT_CI_TOKEN", "")
	if run("check", "github-actions", "--project", "example", "--role", "test", "--pr", "1", "--sha", source.head, "--capability", "exact-revision-ci") == nil {
		t.Fatal("revoked credential authorized")
	}
	if source.calls != 6 {
		t.Fatal("provider called after revocation")
	}
	r, err := component.Load(path)
	if err != nil || r.Records["github-actions"].Bindings[0].State != "disabled" {
		t.Fatal("revocation not persisted", err)
	}
	data, err = os.ReadFile(path)
	if err != nil || strings.Contains(string(data), secret) {
		t.Fatal("secret persisted")
	}
}
