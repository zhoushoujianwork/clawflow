package cloud

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// authReq builds an authenticated HTTP request.
func authReq(t *testing.T, method, rawURL string, body []byte) *http.Request {
	t.Helper()
	var r *http.Request
	var err error
	if body != nil {
		r, err = http.NewRequest(method, rawURL, bytes.NewReader(body))
	} else {
		r, err = http.NewRequest(method, rawURL, nil)
	}
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	r.Header.Set("Authorization", "Bearer test-token")
	return r
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestCloudConfigAuthRequired verifies that all cloud config endpoints reject
// requests without a valid Authorization: Bearer header.
func TestCloudConfigAuthRequired(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewMemoryStore(), nil))
	defer srv.Close()

	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/cloud/config"},
		{http.MethodPost, "/api/cloud/projects"},
		{http.MethodPost, "/api/cloud/repos"},
		{http.MethodPost, "/api/cloud/bindings"},
		{http.MethodGet, "/api/cloud/machines"},
		{http.MethodGet, "/api/cloud/jobs"},
		{http.MethodGet, "/api/cloud/runs"},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", c.method, c.path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s: want 401, got %d", c.method, c.path, resp.StatusCode)
		}
	}
}

// TestCloudConfigGetSummaryEmpty verifies the initial empty summary shape.
func TestCloudConfigGetSummaryEmpty(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewMemoryStore(), nil))
	defer srv.Close()

	req := authReq(t, http.MethodGet, srv.URL+"/api/cloud/config", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var summary CloudConfigSummary
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Slices must not be nil so JSON round-trips cleanly.
	if summary.Projects == nil || summary.Repos == nil || summary.Machines == nil || summary.Bindings == nil {
		t.Fatalf("nil slices in summary: %+v", summary)
	}
	if summary.Counts.Projects != 0 || summary.Counts.Repos != 0 ||
		summary.Counts.Machines != 0 || summary.Counts.Bindings != 0 {
		t.Errorf("non-zero initial counts: %+v", summary.Counts)
	}
}

// TestCloudConfigCreateProject verifies happy-path project creation.
func TestCloudConfigCreateProject(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewMemoryStore(), nil))
	defer srv.Close()

	body := mustMarshal(t, CreateProjectRequest{Name: "my-project", Description: "test desc"})
	req := authReq(t, http.MethodPost, srv.URL+"/api/cloud/projects", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create project: status = %d", resp.StatusCode)
	}
	var p Project
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.ID == "" || p.Name != "my-project" || p.Description != "test desc" {
		t.Errorf("project = %+v", p)
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
		t.Errorf("zero timestamps: created=%v updated=%v", p.CreatedAt, p.UpdatedAt)
	}
}

// TestCloudConfigCreateProjectMissingName verifies validation failure.
func TestCloudConfigCreateProjectMissingName(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewMemoryStore(), nil))
	defer srv.Close()

	body := mustMarshal(t, CreateProjectRequest{})
	req := authReq(t, http.MethodPost, srv.URL+"/api/cloud/projects", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// TestCloudConfigCreateRepo verifies happy-path repo creation.
func TestCloudConfigCreateRepo(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewMemoryStore(), nil))
	defer srv.Close()

	body := mustMarshal(t, CreateRepoRequest{Name: "owner/repo", Platform: "github"})
	req := authReq(t, http.MethodPost, srv.URL+"/api/cloud/repos", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create repo: status = %d", resp.StatusCode)
	}
	var r Repo
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.ID == "" || r.Name != "owner/repo" || r.Platform != "github" {
		t.Errorf("repo = %+v", r)
	}
}

// TestCloudConfigCreateRepoDefaultsPlatform verifies platform defaults to "github".
func TestCloudConfigCreateRepoDefaultsPlatform(t *testing.T) {
	store := NewMemoryStore()
	repo, err := store.CreateRepo(CreateRepoRequest{Name: "owner/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if repo.Platform != "github" {
		t.Errorf("platform = %q, want github", repo.Platform)
	}
}

// TestCloudConfigCreateRepoMissingName verifies validation failure.
func TestCloudConfigCreateRepoMissingName(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewMemoryStore(), nil))
	defer srv.Close()

	body := mustMarshal(t, CreateRepoRequest{})
	req := authReq(t, http.MethodPost, srv.URL+"/api/cloud/repos", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// TestCloudConfigCreateRepoUnknownProject verifies that referencing a
// non-existent project_id returns 400.
func TestCloudConfigCreateRepoUnknownProject(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewMemoryStore(), nil))
	defer srv.Close()

	body := mustMarshal(t, CreateRepoRequest{Name: "owner/repo", ProjectID: "nonexistent"})
	req := authReq(t, http.MethodPost, srv.URL+"/api/cloud/repos", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// TestCloudConfigUpdateRepo verifies PATCH /api/cloud/repos/{id}.
func TestCloudConfigUpdateRepo(t *testing.T) {
	store := NewMemoryStore()
	srv := httptest.NewServer(NewServer(store, nil))
	defer srv.Close()

	repo, err := store.CreateRepo(CreateRepoRequest{Name: "owner/repo"})
	if err != nil {
		t.Fatal(err)
	}

	newBranch := "develop"
	body := mustMarshal(t, UpdateRepoRequest{BaseBranch: &newBranch})
	req := authReq(t, http.MethodPatch, srv.URL+"/api/cloud/repos/"+repo.ID, body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update repo: status = %d", resp.StatusCode)
	}
	var updated Repo
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if updated.BaseBranch != "develop" {
		t.Errorf("base_branch = %q, want %q", updated.BaseBranch, "develop")
	}
}

// TestCloudConfigUpdateRepoNotFound verifies 404 for unknown repo ID.
func TestCloudConfigUpdateRepoNotFound(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewMemoryStore(), nil))
	defer srv.Close()

	newBranch := "develop"
	body := mustMarshal(t, UpdateRepoRequest{BaseBranch: &newBranch})
	req := authReq(t, http.MethodPatch, srv.URL+"/api/cloud/repos/nonexistent", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

// TestCloudConfigCreateBinding verifies POST /api/cloud/bindings.
func TestCloudConfigCreateBinding(t *testing.T) {
	store := NewMemoryStore()
	srv := httptest.NewServer(NewServer(store, nil))
	defer srv.Close()

	reg, err := store.RegisterWorker(RegisterWorkerRequest{Hostname: "host"})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.CreateRepo(CreateRepoRequest{Name: "owner/repo"})
	if err != nil {
		t.Fatal(err)
	}

	body := mustMarshal(t, CreateBindingRequest{MachineID: reg.MachineID, RepoID: repo.ID})
	req := authReq(t, http.MethodPost, srv.URL+"/api/cloud/bindings", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create binding: status = %d", resp.StatusCode)
	}
	var b Binding
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b.ID == "" || b.MachineID != reg.MachineID || b.RepoID != repo.ID {
		t.Errorf("binding = %+v", b)
	}
}

// TestCloudConfigCreateBindingMissingMachineID verifies validation failure.
func TestCloudConfigCreateBindingMissingMachineID(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewMemoryStore(), nil))
	defer srv.Close()

	body := mustMarshal(t, CreateBindingRequest{RepoID: "some-repo"})
	req := authReq(t, http.MethodPost, srv.URL+"/api/cloud/bindings", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// TestCloudConfigCreateBindingMissingRepoOrProject verifies that at least one
// of repo_id or project_id must be supplied.
func TestCloudConfigCreateBindingMissingRepoOrProject(t *testing.T) {
	store := NewMemoryStore()
	srv := httptest.NewServer(NewServer(store, nil))
	defer srv.Close()

	reg, _ := store.RegisterWorker(RegisterWorkerRequest{Hostname: "host"})
	body := mustMarshal(t, CreateBindingRequest{MachineID: reg.MachineID})
	req := authReq(t, http.MethodPost, srv.URL+"/api/cloud/bindings", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// TestCloudConfigCreateBindingUnknownMachine verifies that referencing a
// non-existent machine_id returns 400.
func TestCloudConfigCreateBindingUnknownMachine(t *testing.T) {
	srv := httptest.NewServer(NewServer(NewMemoryStore(), nil))
	defer srv.Close()

	body := mustMarshal(t, CreateBindingRequest{MachineID: "nonexistent", RepoID: "some-repo"})
	req := authReq(t, http.MethodPost, srv.URL+"/api/cloud/bindings", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// TestCloudConfigUpdateBinding verifies PATCH /api/cloud/bindings/{id}.
func TestCloudConfigUpdateBinding(t *testing.T) {
	store := NewMemoryStore()
	srv := httptest.NewServer(NewServer(store, nil))
	defer srv.Close()

	reg1, _ := store.RegisterWorker(RegisterWorkerRequest{Hostname: "host1"})
	reg2, _ := store.RegisterWorker(RegisterWorkerRequest{Hostname: "host2"})
	repo, _ := store.CreateRepo(CreateRepoRequest{Name: "owner/repo"})

	binding, err := store.CreateBinding(CreateBindingRequest{MachineID: reg1.MachineID, RepoID: repo.ID})
	if err != nil {
		t.Fatal(err)
	}

	body := mustMarshal(t, UpdateBindingRequest{MachineID: reg2.MachineID})
	req := authReq(t, http.MethodPatch, srv.URL+"/api/cloud/bindings/"+binding.ID, body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update binding: status = %d", resp.StatusCode)
	}
	var updated Binding
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if updated.MachineID != reg2.MachineID {
		t.Errorf("machine_id = %q, want %q", updated.MachineID, reg2.MachineID)
	}
}

// TestCloudConfigListMachines verifies GET /api/cloud/machines.
func TestCloudConfigListMachines(t *testing.T) {
	store := NewMemoryStore()
	srv := httptest.NewServer(NewServer(store, nil))
	defer srv.Close()

	reg, err := store.RegisterWorker(RegisterWorkerRequest{Hostname: "host"})
	if err != nil {
		t.Fatal(err)
	}

	req := authReq(t, http.MethodGet, srv.URL+"/api/cloud/machines", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list machines: status = %d", resp.StatusCode)
	}
	var list ListMachinesResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Machines) != 1 || list.Machines[0].ID != reg.MachineID {
		t.Errorf("machines = %+v, want machine %s", list.Machines, reg.MachineID)
	}
}

// TestCloudConfigListJobs verifies GET /api/cloud/jobs.
func TestCloudConfigListJobs(t *testing.T) {
	store := NewMemoryStore()
	srv := httptest.NewServer(NewServer(store, nil))
	defer srv.Close()

	_, err := store.EnqueueJob(JobSpec{
		Repo:     "o/r",
		Operator: "evaluate-bug",
		Target:   "issue",
		Number:   1,
	}, "")
	if err != nil {
		t.Fatal(err)
	}

	req := authReq(t, http.MethodGet, srv.URL+"/api/cloud/jobs", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list jobs: status = %d", resp.StatusCode)
	}
	var list ListJobsResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Jobs) != 1 {
		t.Errorf("jobs count = %d, want 1", len(list.Jobs))
	}
}

// TestCloudConfigListRuns verifies GET /api/cloud/runs.
func TestCloudConfigListRuns(t *testing.T) {
	store := NewMemoryStore()
	srv := httptest.NewServer(NewServer(store, nil))
	defer srv.Close()

	reg, _ := store.RegisterWorker(RegisterWorkerRequest{Hostname: "host"})
	_, _ = store.EnqueueJob(JobSpec{
		Repo:     "o/r",
		Operator: "evaluate-bug",
		Target:   "issue",
		Number:   1,
	}, "")
	_, _ = store.Lease(LeaseRequest{MachineID: reg.MachineID, WorkerID: reg.WorkerID}, DefaultLeaseDuration)

	req := authReq(t, http.MethodGet, srv.URL+"/api/cloud/runs", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list runs: status = %d", resp.StatusCode)
	}
	var list ListRunsResponse
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Runs) != 1 {
		t.Errorf("runs count = %d, want 1", len(list.Runs))
	}
}

// TestCloudConfigSummaryReflectsCreates verifies GET /api/cloud/config counts
// update as resources are created.
func TestCloudConfigSummaryReflectsCreates(t *testing.T) {
	store := NewMemoryStore()
	srv := httptest.NewServer(NewServer(store, nil))
	defer srv.Close()

	store.CreateProject(CreateProjectRequest{Name: "p1"})  //nolint:errcheck
	store.CreateRepo(CreateRepoRequest{Name: "owner/repo"}) //nolint:errcheck

	req := authReq(t, http.MethodGet, srv.URL+"/api/cloud/config", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var summary CloudConfigSummary
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if summary.Counts.Projects != 1 || summary.Counts.Repos != 1 {
		t.Errorf("counts = %+v, want projects=1 repos=1", summary.Counts)
	}
	if len(summary.Projects) != 1 || summary.Projects[0].Name != "p1" {
		t.Errorf("projects slice = %+v", summary.Projects)
	}
}

// TestCloudConfigClientRoundTrip verifies that the typed Client can talk to
// the server and decode the JSON shapes correctly.
func TestCloudConfigClientRoundTrip(t *testing.T) {
	store := NewMemoryStore()
	srv := httptest.NewServer(NewServer(store, nil))
	defer srv.Close()

	client, err := NewClient(Config{BaseURL: srv.URL, AccessToken: "test-token"})
	if err != nil {
		t.Fatal(err)
	}

	// Create project via client.
	proj, err := client.CreateProject(t.Context(), CreateProjectRequest{Name: "rt-project"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if proj.ID == "" || proj.Name != "rt-project" {
		t.Errorf("project = %+v", proj)
	}

	// Create repo via client.
	repo, err := client.CreateRepo(t.Context(), CreateRepoRequest{Name: "owner/rt-repo", ProjectID: proj.ID})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	if repo.ProjectID != proj.ID {
		t.Errorf("repo.project_id = %q, want %q", repo.ProjectID, proj.ID)
	}

	// Update repo via client.
	branch := "main"
	updated, err := client.UpdateRepo(t.Context(), repo.ID, UpdateRepoRequest{BaseBranch: &branch})
	if err != nil {
		t.Fatalf("UpdateRepo: %v", err)
	}
	if updated.BaseBranch != "main" {
		t.Errorf("base_branch = %q, want main", updated.BaseBranch)
	}

	// Register machine directly and bind via client.
	reg, _ := store.RegisterWorker(RegisterWorkerRequest{Hostname: "rt-host"})
	binding, err := client.CreateBinding(t.Context(), CreateBindingRequest{
		MachineID: reg.MachineID,
		RepoID:    repo.ID,
	})
	if err != nil {
		t.Fatalf("CreateBinding: %v", err)
	}
	if binding.RepoID != repo.ID {
		t.Errorf("binding.repo_id = %q, want %q", binding.RepoID, repo.ID)
	}

	// Verify summary counts.
	summary, err := client.GetCloudConfig(t.Context())
	if err != nil {
		t.Fatalf("GetCloudConfig: %v", err)
	}
	if summary.Counts.Projects != 1 || summary.Counts.Repos != 1 ||
		summary.Counts.Machines != 1 || summary.Counts.Bindings != 1 {
		t.Errorf("summary counts = %+v", summary.Counts)
	}

	// List machines via client.
	machines, err := client.ListMachines(t.Context())
	if err != nil {
		t.Fatalf("ListMachines: %v", err)
	}
	if len(machines.Machines) != 1 {
		t.Errorf("machines count = %d, want 1", len(machines.Machines))
	}
}
