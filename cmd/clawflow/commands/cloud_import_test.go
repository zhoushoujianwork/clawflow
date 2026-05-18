package commands

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

// fakeImporter is an in-memory cloudImporter for unit testing
// importConfigToCloud without standing up an HTTP server.
type fakeImporter struct {
	existingProjects []*cloud.Project
	existingRepos    []*cloud.Repo

	createdProjects []cloud.CreateProjectRequest
	createdRepos    []cloud.CreateRepoRequest

	nextProjectID int
	nextRepoID    int

	// errors injectable per call.
	getErr     error
	projectErr error
	repoErr    error
}

func (f *fakeImporter) GetCloudConfig(_ context.Context) (*cloud.CloudConfigSummary, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return &cloud.CloudConfigSummary{
		Projects: f.existingProjects,
		Repos:    f.existingRepos,
	}, nil
}

func (f *fakeImporter) CreateProject(_ context.Context, req cloud.CreateProjectRequest) (*cloud.Project, error) {
	if f.projectErr != nil {
		return nil, f.projectErr
	}
	f.createdProjects = append(f.createdProjects, req)
	f.nextProjectID++
	return &cloud.Project{
		ID:        idFromCounter("proj", f.nextProjectID),
		Name:      req.Name,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}, nil
}

func (f *fakeImporter) CreateRepo(_ context.Context, req cloud.CreateRepoRequest) (*cloud.Repo, error) {
	if f.repoErr != nil {
		return nil, f.repoErr
	}
	f.createdRepos = append(f.createdRepos, req)
	f.nextRepoID++
	return &cloud.Repo{
		ID:         idFromCounter("repo", f.nextRepoID),
		Name:       req.Name,
		Platform:   req.Platform,
		ProjectID:  req.ProjectID,
		BaseBranch: req.BaseBranch,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}, nil
}

func idFromCounter(prefix string, n int) string {
	return prefix + "-" + intStr(n)
}

func intStr(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestImportConfigToCloud(t *testing.T) {
	tests := []struct {
		name             string
		projects         []*project.Project
		repos            map[string]config.Repo
		assoc            map[string]string
		existingProjects []*cloud.Project
		existingRepos    []*cloud.Repo
		dryRun           bool
		wantProjects     int
		wantRepos        int
		wantSkipped      int
		wantCreateCalls  int // sum of CreateProject + CreateRepo calls
	}{
		{
			name: "imports new projects and repos with associations",
			projects: []*project.Project{
				{Name: "alpha", Repos: []string{"acme/api"}},
				{Name: "beta", Repos: []string{"acme/web"}},
			},
			repos: map[string]config.Repo{
				"acme/api":  {Platform: "github", BaseBranch: "main"},
				"acme/web":  {Platform: "github", BaseBranch: "main"},
				"acme/lone": {Platform: "github"},
			},
			assoc: map[string]string{
				"acme/api": "alpha",
				"acme/web": "beta",
			},
			wantProjects:    2,
			wantRepos:       3,
			wantSkipped:     0,
			wantCreateCalls: 5,
		},
		{
			name: "skips already-imported projects and repos",
			projects: []*project.Project{
				{Name: "alpha"},
				{Name: "beta"},
			},
			repos: map[string]config.Repo{
				"acme/api": {Platform: "github"},
				"acme/web": {Platform: "github"},
			},
			existingProjects: []*cloud.Project{
				{ID: "proj-old-1", Name: "alpha"},
			},
			existingRepos: []*cloud.Repo{
				{ID: "repo-old-1", Name: "acme/api"},
			},
			wantProjects:    1, // beta
			wantRepos:       1, // acme/web
			wantSkipped:     2, // alpha + acme/api
			wantCreateCalls: 2,
		},
		{
			name: "empty inputs make no calls",
			projects: nil,
			repos:    nil,
			wantProjects: 0,
			wantRepos:    0,
			wantSkipped:  0,
		},
		{
			name: "dry-run records counts but issues no calls",
			projects: []*project.Project{
				{Name: "alpha", Repos: []string{"acme/api"}},
			},
			repos: map[string]config.Repo{
				"acme/api": {Platform: "github"},
			},
			assoc: map[string]string{
				"acme/api": "alpha",
			},
			dryRun:          true,
			wantProjects:    1,
			wantRepos:       1,
			wantSkipped:     0,
			wantCreateCalls: 0,
		},
		{
			name: "defaults platform to github when missing",
			projects: nil,
			repos: map[string]config.Repo{
				"acme/api": {BaseBranch: "main"}, // no platform
			},
			wantProjects:    0,
			wantRepos:       1,
			wantSkipped:     0,
			wantCreateCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeImporter{
				existingProjects: tt.existingProjects,
				existingRepos:    tt.existingRepos,
			}
			var c cloudImporter
			if !tt.dryRun {
				c = f
			}
			sum, err := importConfigToCloud(
				context.Background(),
				c,
				tt.projects,
				tt.repos,
				tt.assoc,
				tt.dryRun,
			)
			if err != nil {
				t.Fatalf("importConfigToCloud: %v", err)
			}
			if sum.ProjectsImported != tt.wantProjects {
				t.Errorf("ProjectsImported = %d, want %d", sum.ProjectsImported, tt.wantProjects)
			}
			if sum.ReposImported != tt.wantRepos {
				t.Errorf("ReposImported = %d, want %d", sum.ReposImported, tt.wantRepos)
			}
			if sum.Skipped != tt.wantSkipped {
				t.Errorf("Skipped = %d, want %d", sum.Skipped, tt.wantSkipped)
			}
			calls := len(f.createdProjects) + len(f.createdRepos)
			if calls != tt.wantCreateCalls {
				t.Errorf("create calls = %d, want %d", calls, tt.wantCreateCalls)
			}
			// In the first case verify that repo create requests carry
			// the freshly-minted project_id for repos that have an assoc.
			if tt.name == "imports new projects and repos with associations" {
				for _, r := range f.createdRepos {
					switch r.Name {
					case "acme/api", "acme/web":
						if r.ProjectID == "" {
							t.Errorf("repo %q: expected ProjectID set, got empty", r.Name)
						}
					case "acme/lone":
						if r.ProjectID != "" {
							t.Errorf("repo %q: expected empty ProjectID, got %q", r.Name, r.ProjectID)
						}
					}
					if r.Platform != "github" {
						t.Errorf("repo %q: Platform = %q, want github", r.Name, r.Platform)
					}
				}
			}
			// In the platform-default test, verify "github" was set on the request.
			if tt.name == "defaults platform to github when missing" {
				if len(f.createdRepos) != 1 || f.createdRepos[0].Platform != "github" {
					t.Errorf("expected platform=github on synthesized request, got %+v", f.createdRepos)
				}
			}
		})
	}
}

func TestImportConfigToCloud_GetConfigError(t *testing.T) {
	f := &fakeImporter{getErr: errors.New("boom")}
	_, err := importConfigToCloud(
		context.Background(),
		f,
		[]*project.Project{{Name: "a"}},
		map[string]config.Repo{"a/b": {}},
		nil,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected boom error, got %v", err)
	}
}

func TestImportConfigToCloud_CreateProjectError(t *testing.T) {
	f := &fakeImporter{projectErr: errors.New("nope")}
	_, err := importConfigToCloud(
		context.Background(),
		f,
		[]*project.Project{{Name: "alpha"}},
		nil,
		nil,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected nope error, got %v", err)
	}
}

func TestBuildProjectAssoc(t *testing.T) {
	got := buildProjectAssoc([]*project.Project{
		{Name: "alpha", Repos: []string{"acme/api", "acme/lib"}},
		{Name: "beta", Repos: []string{"acme/web"}},
		nil,
		// duplicate repo listing: alpha sorts first so it wins.
		{Name: "zzz", Repos: []string{"acme/api"}},
	})
	want := map[string]string{
		"acme/api": "alpha",
		"acme/lib": "alpha",
		"acme/web": "beta",
	}
	if len(got) != len(want) {
		t.Fatalf("size = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("assoc[%q] = %q, want %q", k, got[k], v)
		}
	}
}
