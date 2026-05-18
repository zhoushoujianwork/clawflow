package cloud

import (
	"testing"
)

// TestSQLiteCloudConfigPersistence drives the projects / repos / bindings /
// vcs_connections CRUD path on a real SQLite (in-memory) database. The
// in-memory and in-SQL contracts were identical until this PR; this test
// makes sure the SQL backed implementation keeps the same behaviour.
//
// Covered:
//   - Create / Get / Update / Delete on projects, repos, bindings
//   - Foreign-key validation (project_id, repo_id, machine_id)
//   - Summary returns the same shape with counts
//   - VCSConnection upsert is keyed by repo (same repo → same ID)
func TestSQLiteCloudConfigPersistence(t *testing.T) {
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer s.Close()

	// ---- Project lifecycle ----
	p, err := s.CreateProject(CreateProjectRequest{Name: "backend", Description: "API service"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	if p.ID == "" || p.Name != "backend" {
		t.Fatalf("project shape: %+v", p)
	}

	if _, err := s.CreateProject(CreateProjectRequest{Name: ""}); err == nil {
		t.Fatal("expected error for empty name")
	}

	// ---- Repo lifecycle including FK ----
	r, err := s.CreateRepo(CreateRepoRequest{
		Name: "acme/api", Platform: "github", ProjectID: p.ID, BaseBranch: "main",
	})
	if err != nil {
		t.Fatalf("create repo: %v", err)
	}
	if r.ProjectID != p.ID {
		t.Fatalf("project link not stored: %+v", r)
	}

	// Bad FK rejected.
	if _, err := s.CreateRepo(CreateRepoRequest{Name: "x/y", ProjectID: "nope"}); err == nil {
		t.Fatal("expected error for unknown project_id")
	}

	// PATCH detaches and updates base_branch.
	empty := ""
	newBranch := "trunk"
	r2, err := s.UpdateRepo(r.ID, UpdateRepoRequest{ProjectID: &empty, BaseBranch: &newBranch})
	if err != nil {
		t.Fatalf("update repo: %v", err)
	}
	if r2.ProjectID != "" || r2.BaseBranch != "trunk" {
		t.Fatalf("update repo state: %+v", r2)
	}

	// ---- Binding lifecycle (needs machine row first) ----
	regResp, err := s.RegisterWorker(RegisterWorkerRequest{Hostname: "laptop", Version: "v1"})
	if err != nil {
		t.Fatalf("register worker: %v", err)
	}

	b, err := s.CreateBinding(CreateBindingRequest{
		MachineID: regResp.MachineID, RepoID: r.ID,
	})
	if err != nil {
		t.Fatalf("create binding: %v", err)
	}
	if b.MachineID != regResp.MachineID || b.RepoID != r.ID {
		t.Fatalf("binding shape: %+v", b)
	}

	// Bad machine FK rejected.
	if _, err := s.CreateBinding(CreateBindingRequest{
		MachineID: "machine-nope", RepoID: r.ID,
	}); err == nil {
		t.Fatal("expected error for unknown machine_id")
	}

	// ---- Summary aggregates everything ----
	sum := s.Summary()
	if sum.Counts.Projects != 1 || sum.Counts.Repos != 1 || sum.Counts.Bindings != 1 || sum.Counts.Machines != 1 {
		t.Fatalf("summary counts: %+v", sum.Counts)
	}

	// ---- DELETE paths ----
	if err := s.DeleteBinding(b.ID); err != nil {
		t.Fatalf("delete binding: %v", err)
	}
	if err := s.DeleteBinding(b.ID); err == nil {
		t.Fatal("expected error deleting binding twice")
	}
	if err := s.DeleteRepo(r.ID); err != nil {
		t.Fatalf("delete repo: %v", err)
	}
	if err := s.DeleteProject(p.ID); err != nil {
		t.Fatalf("delete project: %v", err)
	}

	sum = s.Summary()
	if sum.Counts.Projects != 0 || sum.Counts.Repos != 0 || sum.Counts.Bindings != 0 {
		t.Fatalf("summary after deletes: %+v", sum.Counts)
	}
}

// TestSQLiteVCSConnectionUpsert verifies the connection upsert keys on repo
// — same "owner/repo" string always resolves to the same ID even when
// callers don't pass it explicitly.
func TestSQLiteVCSConnectionUpsert(t *testing.T) {
	s, err := NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	c1, err := s.RegisterConnection(VCSConnection{
		Repo:     "acme/api",
		Platform: "github",
		GitHubApp: &GitHubAppInstallation{
			AppID:          1234,
			InstallationID: 567,
			WebhookSecret:  "hmac-secret",
		},
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if c1.ID == "" || c1.GitHubApp == nil {
		t.Fatalf("first upsert shape: %+v", c1)
	}

	c2, err := s.RegisterConnection(VCSConnection{
		Repo: "acme/api", Platform: "github",
		BoundMachineID: "machine-xyz",
		GitHubApp: &GitHubAppInstallation{
			AppID:          1234,
			InstallationID: 999, // updated
			WebhookSecret:  "hmac-secret",
		},
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if c2.ID != c1.ID {
		t.Fatalf("connection ID changed: %s vs %s", c2.ID, c1.ID)
	}
	if c2.BoundMachineID != "machine-xyz" || c2.GitHubApp.InstallationID != 999 {
		t.Fatalf("second upsert did not update fields: %+v", c2)
	}

	got := s.GetConnectionByRepo("acme/api")
	if got == nil || got.GitHubApp == nil || got.GitHubApp.InstallationID != 999 {
		t.Fatalf("get by repo did not return latest: %+v", got)
	}

	if s.GetConnectionByRepo("unknown/repo") != nil {
		t.Fatal("expected nil for unknown repo")
	}
}
