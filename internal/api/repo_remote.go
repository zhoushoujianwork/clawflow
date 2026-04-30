package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/clone"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// RemoteRepo represents a repository available on GitHub/GitLab.
type RemoteRepo struct {
	FullName      string `json:"full_name"`      // "owner/repo"
	Platform      string `json:"platform"`       // "github" or "gitlab"
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
	BaseURL       string `json:"base_url,omitempty"` // GitLab instance URL
}

type listRemoteReposResponse struct {
	Repos []RemoteRepo `json:"repos"`
	Error string       `json:"error,omitempty"`
}

type addRemoteRepoRequest struct {
	FullName      string `json:"full_name"`
	Platform      string `json:"platform"`
	DefaultBranch string `json:"default_branch"`
	Description   string `json:"description"`
	BaseURL       string `json:"base_url,omitempty"` // for GitLab self-hosted
}

type addRemoteRepoResponse struct {
	Status    string `json:"status"`
	LocalPath string `json:"local_path,omitempty"`
	Error     string `json:"error,omitempty"`
}

// HandleListRemoteRepos handles GET /api/repos/list-remote
// Fetches available repositories from GitHub or GitLab using the configured token.
func HandleListRemoteRepos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	platform := r.URL.Query().Get("platform")
	if platform == "" {
		platform = "github" // default to GitHub
	}

	creds, err := config.LoadCredentials()
	if err != nil {
		writeJSON(w, 500, listRemoteReposResponse{Error: fmt.Sprintf("load credentials: %v", err)})
		return
	}

	var repos []RemoteRepo
	switch platform {
	case "github":
		if creds.GHToken == "" {
			writeJSON(w, 400, listRemoteReposResponse{Error: "GitHub token not configured"})
			return
		}
		repos, err = listGitHubRepos(creds.GHToken)
	case "gitlab":
		if creds.GitLabToken == "" {
			writeJSON(w, 400, listRemoteReposResponse{Error: "GitLab token not configured. Please add your GitLab token in Settings."})
			return
		}
		cfg, err := config.Load()
		if err != nil {
			writeJSON(w, 500, listRemoteReposResponse{Error: fmt.Sprintf("load config: %v", err)})
			return
		}
		// Use first configured GitLab host or default to gitlab.com
		baseURL := "https://gitlab.com"
		if len(cfg.Settings.GitLabHosts) > 0 {
			baseURL = normalizeGitLabURL(cfg.Settings.GitLabHosts[0])
		}
		repos, err = listGitLabRepos(creds.GitLabToken, baseURL)
	default:
		writeJSON(w, 400, listRemoteReposResponse{Error: "unsupported platform"})
		return
	}

	if err != nil {
		writeJSON(w, 500, listRemoteReposResponse{Error: err.Error()})
		return
	}

	writeJSON(w, 200, listRemoteReposResponse{Repos: repos})
}

// HandleAddRemoteRepo handles POST /api/repos/add-remote
// Clones a selected repository and adds it to the config.
func HandleAddRemoteRepo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req addRemoteRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, addRemoteRepoResponse{Error: "invalid JSON"})
		return
	}

	if req.FullName == "" || req.Platform == "" {
		writeJSON(w, 400, addRemoteRepoResponse{Error: "full_name and platform are required"})
		return
	}

	cfg, err := config.Load()
	if err != nil {
		writeJSON(w, 500, addRemoteRepoResponse{Error: fmt.Sprintf("load config: %v", err)})
		return
	}

	// Check if repo already exists
	if _, exists := cfg.Repos[req.FullName]; exists {
		writeJSON(w, 400, addRemoteRepoResponse{Error: "repository already configured"})
		return
	}

	// Create repo config
	repoCfg := config.Repo{
		Enabled:     true,
		Platform:    req.Platform,
		BaseURL:     req.BaseURL,
		BaseBranch:  req.DefaultBranch,
		Description: req.Description,
		AddedAt:     time.Now().Format(time.RFC3339),
	}

	// Extract owner from full_name
	owner := req.FullName
	if idx := len(req.FullName) - 1; idx >= 0 {
		for i := 0; i < len(req.FullName); i++ {
			if req.FullName[i] == '/' {
				owner = req.FullName[:i]
				break
			}
		}
	}
	repoCfg.Owner = owner

	// Add to config first (EnsureLocalClone expects it to be there)
	cfg.Repos[req.FullName] = repoCfg
	if err := cfg.Save(); err != nil {
		writeJSON(w, 500, addRemoteRepoResponse{Error: fmt.Sprintf("save config: %v", err)})
		return
	}

	// Clone the repository with output capture
	var logBuf bytes.Buffer
	localPath, err := clone.EnsureLocalClone(cfg, req.FullName, repoCfg, &logBuf)
	if err != nil {
		// Remove from config on clone failure
		delete(cfg.Repos, req.FullName)
		_ = cfg.Save()
		writeJSON(w, 500, addRemoteRepoResponse{
			Error: fmt.Sprintf("clone failed: %v\nLog:\n%s", err, logBuf.String()),
		})
		return
	}

	// Update config with local path
	repoCfg.LocalPath = localPath
	cfg.Repos[req.FullName] = repoCfg
	if err := cfg.Save(); err != nil {
		writeJSON(w, 500, addRemoteRepoResponse{Error: fmt.Sprintf("save local path: %v", err)})
		return
	}

	// Refresh snapshot
	_ = snapshot.WriteRepos(cfg)

	writeJSON(w, 200, addRemoteRepoResponse{
		Status:    "ok",
		LocalPath: localPath,
	})
}

// listGitHubRepos fetches repositories from GitHub API.
func listGitHubRepos(token string) ([]RemoteRepo, error) {
	// GitHub API endpoint for listing user repos
	type ghRepo struct {
		FullName      string `json:"full_name"`
		Description   string `json:"description"`
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
		HTMLURL       string `json:"html_url"`
	}
	req, err := http.NewRequest("GET", "https://api.github.com/user/repos?per_page=100&sort=updated", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var ghRepos []ghRepo
	if err := json.NewDecoder(resp.Body).Decode(&ghRepos); err != nil {
		return nil, err
	}

	repos := make([]RemoteRepo, len(ghRepos))
	for i, r := range ghRepos {
		repos[i] = RemoteRepo{
			FullName:      r.FullName,
			Platform:      "github",
			Description:   r.Description,
			DefaultBranch: r.DefaultBranch,
			Private:       r.Private,
			HTMLURL:       r.HTMLURL,
		}
	}

	return repos, nil
}

// listGitLabRepos fetches repositories from GitLab API.
func listGitLabRepos(token, baseURL string) ([]RemoteRepo, error) {
	// GitLab API endpoint for listing user projects
	type glProject struct {
		PathWithNamespace string `json:"path_with_namespace"`
		Description       string `json:"description"`
		DefaultBranch     string `json:"default_branch"`
		Visibility        string `json:"visibility"` // "private", "internal", "public"
		WebURL            string `json:"web_url"`
	}

	apiURL := baseURL + "/api/v4/projects?membership=true&per_page=100&order_by=last_activity_at"
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to GitLab: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("GitLab token is invalid or expired. Please update your token in Settings and test the connection.")
	}
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitLab API returned status %d: %s", resp.StatusCode, string(body))
	}

	var glProjects []glProject
	if err := json.NewDecoder(resp.Body).Decode(&glProjects); err != nil {
		return nil, err
	}

	repos := make([]RemoteRepo, len(glProjects))
	for i, p := range glProjects {
		repos[i] = RemoteRepo{
			FullName:      p.PathWithNamespace,
			Platform:      "gitlab",
			Description:   p.Description,
			DefaultBranch: p.DefaultBranch,
			Private:       p.Visibility == "private",
			HTMLURL:       p.WebURL,
			BaseURL:       baseURL,
		}
	}

	return repos, nil
}
