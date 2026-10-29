package component

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialGrantsIsolationAndRevocation(t *testing.T) {
	for _, stage := range []string{"activate", "authorize"} {
		t.Run(stage, func(t *testing.T) {
			r, a, _ := prepared(t)
			path := filepath.Join(t.TempDir(), "access.json")
			grant := CredentialGrant{Component: "test", Project: "one", Role: "test", Resource: "repo/example", CredentialRef: "projects/one/credentials/test", Capabilities: []string{"test"}, Environment: "COMPONENT_TEST_SECRET"}
			write := func(g CredentialGrant) {
				data, _ := json.Marshal(CredentialGrants{Schema: Schema, Grants: []CredentialGrant{g}})
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			write(grant)
			t.Setenv("COMPONENT_TEST_SECRET", "private-test-value")
			r.Credentials = FileCredentials(path)
			// Existing syntax and an environment value cannot grant another project.
			if r.Bind("test", bind("two")) == nil {
				t.Fatal("cross-project binding accepted")
			}
			if stage == "authorize" {
				if err := r.Activate(context.Background(), "test", "one", "test", a); err != nil {
					t.Fatal(err)
				}
			}
			grant.Capabilities = []string{"other"}
			write(grant)
			var err error
			if stage == "activate" {
				err = r.Activate(context.Background(), "test", "one", "test", a)
			} else {
				err = r.Authorize(context.Background(), "test", "one", "test", "test", a)
			}
			if err == nil || r.Records["test"].Bindings[0].State != "disabled" {
				t.Fatal("revoked capability remained authorized")
			}
			data, _ := json.Marshal(r)
			if strings.Contains(string(data), "private-test-value") || strings.Contains(err.Error(), "private-test-value") {
				t.Fatal("credential disclosure")
			}
			grant.Capabilities = []string{"test"}
			write(grant)
			t.Setenv("COMPONENT_TEST_SECRET", "")
			if _, err = FileCredentials(path)("test", bind("one")); err == nil {
				t.Fatal("missing credential accepted")
			}
		})
	}
}

func TestCredentialResolverRequired(t *testing.T) {
	r, _, _ := prepared(t)
	r.Credentials = nil
	if r.Bind("test", bind("two")) == nil {
		t.Fatal("missing resolver accepted")
	}
}
