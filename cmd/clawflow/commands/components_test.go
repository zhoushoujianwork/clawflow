package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/component"
)

func TestComponentsCLILifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "components.json")
	a := component.ReadAdapter{Descriptor: component.Descriptor{ID: "test", Adapter: "test", Version: "1", Reference: "test", Capabilities: []string{"test"}}, Observe: func(context.Context) (component.Observation, error) {
		return component.Observation{Identity: "test", Version: "1", Protocol: component.Schema, Health: "available", Capabilities: []string{"test"}}, nil
	}}
	run := func(args ...string) error {
		c := newComponentsCmd(path, []component.Adapter{a}, func(p string) bool { return p == "example" })
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
