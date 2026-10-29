package component

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

func fixture() (Registry, ReadAdapter, *Observation, *int) {
	calls := new(int)
	o := &Observation{Identity: "test-service", Version: "commit-123", Protocol: Schema, Health: "available", Capabilities: []string{"test"}}
	a := ReadAdapter{Descriptor: Descriptor{ID: "test", Adapter: "fake", Version: "1", Reference: "test-command", Capabilities: []string{"test"}}, Observe: func(context.Context) (Observation, error) { *calls++; return *o, nil }}
	return Registry{Schema: Schema, Records: map[string]Record{}}, a, o, calls
}
func bind(project string) Binding {
	return Binding{Project: project, Role: "test", Resource: "repo/example", CredentialRef: "projects/" + project + "/credentials/test", Capabilities: []string{"test"}}
}
func prepared(t *testing.T) (Registry, ReadAdapter, *Observation) {
	t.Helper()
	r, a, o, _ := fixture()
	if err := r.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := r.Verify(context.Background(), "test", a); err != nil {
		t.Fatal(err)
	}
	if err := r.Bind("test", bind("one")); err != nil {
		t.Fatal(err)
	}
	return r, a, o
}
func TestDiscoveryAndOnboardingAreDisabled(t *testing.T) {
	r, a, _, calls := fixture()
	ctx := context.Background()
	inventory, err := Inventory([]Adapter{a})
	if err != nil || inventory[0].State != "discovered" || *calls != 0 {
		t.Fatal(inventory, err, *calls)
	}
	if len(r.Records) != 0 {
		t.Fatal("discovery persisted")
	}
	if err = r.Register(a); err != nil {
		t.Fatal(err)
	}
	if err = r.Bind("test", bind("one")); err == nil {
		t.Fatal("unverified bind")
	}
	if err = r.Authorize(ctx, "test", "one", "test", "test", a); err == nil {
		t.Fatal("registration enabled dispatch")
	}
	if err = r.Verify(ctx, "test", a); err != nil {
		t.Fatal(err)
	}
	if err = r.Bind("test", bind("one")); err != nil {
		t.Fatal(err)
	}
	if err = r.Authorize(ctx, "test", "one", "test", "test", a); err == nil {
		t.Fatal("binding enabled dispatch")
	}
	if err = r.Activate(ctx, "test", "one", "test", a); err != nil {
		t.Fatal(err)
	}
	if err = r.Authorize(ctx, "test", "one", "test", "test", a); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"two", ""} {
		if r.Authorize(ctx, "test", p, "test", "test", a) == nil {
			t.Fatal("cross-project authorization")
		}
	}
	if r.Authorize(ctx, "test", "one", "test", "execute", a) == nil {
		t.Fatal("unapproved capability")
	}
	if err = r.Disable("test", "one", "test"); err != nil {
		t.Fatal(err)
	}
	if r.Authorize(ctx, "test", "one", "test", "test", a) == nil {
		t.Fatal("disabled authorization")
	}
}
func TestIncompatibilityInvalidatesBindings(t *testing.T) {
	for name, change := range map[string]func(*Observation){
		"protocol": func(o *Observation) { o.Protocol++ }, "health": func(o *Observation) { o.Health = "unavailable" },
		"identity": func(o *Observation) { o.Identity = "other" }, "version": func(o *Observation) { o.Version = "new" },
		"capability": func(o *Observation) { o.Capabilities = []string{"other"} },
	} {
		t.Run(name, func(t *testing.T) {
			r, a, o := prepared(t)
			ctx := context.Background()
			if r.Activate(ctx, "test", "one", "test", a) != nil {
				t.Fatal("activate")
			}
			change(o)
			if r.Authorize(ctx, "test", "one", "test", "test", a) == nil {
				t.Fatal("accepted changed observation")
			}
			rec := r.Records["test"]
			if rec.Health != "unavailable" || rec.State != "blocked" || rec.Bindings[0].State != "disabled" {
				t.Fatal(rec)
			}
		})
	}
}
func TestProjectCredentialsAndReverification(t *testing.T) {
	r, a, _ := prepared(t)
	for _, ref := range []string{"projects/one/credentials/test", "Bearer secret", "https://user:secret@example.com", "projects/two/credentials/execute"} {
		b := bind("two")
		b.CredentialRef = ref
		if r.Bind("test", b) == nil {
			t.Fatal("accepted credential", ref)
		}
	}
	b := bind("two")
	b.Capabilities = []string{"execute"}
	if r.Bind("test", b) == nil {
		t.Fatal("unsupported capability")
	}
	if r.Bind("test", bind("two")) != nil {
		t.Fatal("second project")
	}
	if r.Verify(context.Background(), "test", a) != nil || len(r.Records["test"].Bindings) != 0 {
		t.Fatal("verification retained authorization")
	}
}
func TestAdapterSubstitutionAndErrorRedaction(t *testing.T) {
	r, a, _ := prepared(t)
	ctx := context.Background()
	substitute := ReadAdapter{Descriptor: a.Descriptor, Observe: func(context.Context) (Observation, error) {
		return Observation{Identity: "test-service", Version: "commit-123", Protocol: Schema, Health: "available", Capabilities: []string{"test"}}, nil
	}}
	if r.Activate(ctx, "test", "one", "test", substitute) != nil {
		t.Fatal("compatible adapter rejected")
	}
	substitute.Observe = func(context.Context) (Observation, error) { return Observation{}, errors.New("token=do-not-record") }
	err := r.Authorize(ctx, "test", "one", "test", "test", substitute)
	data, _ := json.Marshal(r)
	if err == nil || strings.Contains(err.Error(), "do-not-record") || strings.Contains(string(data), "do-not-record") {
		t.Fatal("unsafe error", err, string(data))
	}
	a.Descriptor.Version = "2"
	if r.Verify(ctx, "test", a) == nil {
		t.Fatal("adapter version upgrade accepted")
	}
}
func TestStorePersistsFailureAndRejectsUnknownSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "components.json")
	r, a, o := prepared(t)
	if err := Update(path, func(out *Registry) error { *out = r; return nil }); err != nil {
		t.Fatal(err)
	}
	o.Health = "unavailable"
	if err := Update(path, func(out *Registry) error { return out.Activate(context.Background(), "test", "one", "test", a) }); err == nil {
		t.Fatal("health accepted")
	}
	loaded, err := Load(path)
	if err != nil || loaded.Records["test"].State != "blocked" {
		t.Fatal(loaded, err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if err = os.WriteFile(path, []byte(`{"schema":2,"records":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(path); err == nil {
		t.Fatal("future schema accepted")
	}
}
func TestKnownInventoryDoesNotProbeExecution(t *testing.T) {
	called := false
	adapters := Builtins(func() (execution.Config, error) { called = true; return execution.Config{}, errors.New("secret") })
	records, err := Inventory(adapters)
	if err != nil || len(records) != 5 || called {
		t.Fatal(records, err, called)
	}
	for _, a := range adapters {
		r := Registry{Schema: Schema}
		if r.Register(a) != nil {
			t.Fatal("register")
		}
		err := r.Verify(context.Background(), a.Discover().ID, a)
		if a.Discover().ID == "offload" || a.Discover().ID == "github-actions" {
			if err == nil {
				t.Fatal("unavailable dependency verified")
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}

type evidenceSource struct {
	evidence vcs.CIEvidence
	repoID   int64
	err      error
}

func (p evidenceSource) GetRepoID(string) (int64, error)                   { return p.repoID, p.err }
func (p evidenceSource) GetCIEvidence(string, int) (vcs.CIEvidence, error) { return p.evidence, p.err }
func TestGitHubEvidenceAdapter(t *testing.T) {
	head := strings.Repeat("a", 40)
	p := evidenceSource{repoID: 123, evidence: vcs.CIEvidence{HeadSHA: head, Status: vcs.CIStatusFailure, CheckIDs: []int64{456}}}
	a := GitHubActions(p, "owner/repo", 1, head)
	r := Registry{Schema: Schema}
	if r.Register(a) != nil || r.Verify(context.Background(), "github-actions", a) != nil {
		t.Fatal("existing evidence interface unavailable")
	}
	// Test failure is distinct from provider health.
	if r.Records["github-actions"].Health != "available" {
		t.Fatal("test failure treated as unavailable")
	}
	for _, change := range []func(*evidenceSource){
		func(p *evidenceSource) { p.repoID = 0 },
		func(p *evidenceSource) { p.evidence.HeadSHA = strings.Repeat("b", 40) },
		func(p *evidenceSource) { p.evidence.CheckIDs = nil },
		func(p *evidenceSource) { p.err = errors.New("credential=secret") },
	} {
		bad := p
		change(&bad)
		if r.Verify(context.Background(), "github-actions", GitHubActions(bad, "owner/repo", 1, head)) == nil {
			t.Fatal("invalid evidence accepted")
		}
	}
}

func TestMissingSchemaIsNotMigrated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "components.json")
	original := []byte(`{"records":{}}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Update(path, func(r *Registry) error { t.Fatal("unsupported record was mutated"); return nil }); err == nil {
		t.Fatal("missing schema accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(original) {
		t.Fatal("unsupported record rewritten")
	}
}
