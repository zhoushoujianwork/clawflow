package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/clone"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// remoteReposPerPage is the page size requested from GitHub/GitLab list APIs.
const remoteReposPerPage = 100

// remoteReposMaxPages caps how many pages we will fetch so an unusually large
// account cannot cause an unbounded number of API requests.
const remoteReposMaxPages = 50

// remoteHTTPClient carries a timeout so a hung VCS instance cannot stall the
// dashboard request indefinitely (the previous code used http.DefaultClient,
// which has no timeout).
var remoteHTTPClient = &http.Client{Timeout: 30 * time.Second}

// linkNextRe extracts the next-page URL from a GitHub "Link" header entry,
// e.g. `<https://api.github.com/user/repos?page=2>; rel="next"`.
var linkNextRe = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// RemoteRepo represents a repository available on GitHub/GitLab.
type RemoteRepo struct {
	FullName      string `json:"full_name"`      // "owner/repo"
	Platform      string `json:"platform"`       // "github" or "gitlab"
	Description   string `json:"description"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
	BaseURL       string `json:"base_url,omitempty"` // GitLab instance URL
	LocalPath     string `json:"local_path,omitempty"` // non-empty if a matching local clone exists
}

type listRemoteReposResponse struct {
	Repos           []RemoteRepo `json:"repos"`
	TokenConfigured bool         `json:"token_configured"`
	Error           string       `json:"error,omitempty"`
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
			writeJSON(w, 200, listRemoteReposResponse{TokenConfigured: false, Error: "GitHub token not configured. Please add your GitHub token in Settings."})
			return
		}
		repos, err = listGitHubRepos(creds.GHToken)
	case "gitlab":
		if creds.GitLabToken == "" {
			writeJSON(w, 200, listRemoteReposResponse{TokenConfigured: false, Error: "GitLab token not configured. Please add your GitLab token in Settings."})
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
		writeJSON(w, 500, listRemoteReposResponse{TokenConfigured: true, Error: err.Error()})
		return
	}

	// Detect existing local clones for each repo so the frontend can
	// show "local clone found" without requiring an actual clone operation.
	cfg, cfgErr := config.Load()
	if cfgErr == nil {
		for i := range repos {
			repoCfg := config.Repo{
				Platform: repos[i].Platform,
				BaseURL:  repos[i].BaseURL,
			}
			if localPath := clone.DetectLocalClone(cfg, repos[i].FullName, repoCfg); localPath != "" {
				repos[i].LocalPath = localPath
			}
		}
	}

	writeJSON(w, 200, listRemoteReposResponse{Repos: repos, TokenConfigured: true})
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

	// Check if a matching local clone already exists — skip clone if so.
	var localPath string
	if detected := clone.DetectLocalClone(cfg, req.FullName, repoCfg); detected != "" {
		localPath = detected
	} else {
		// Clone the repository with output capture
		var logBuf bytes.Buffer
		creds, _ := config.LoadCredentials()
		var token *clone.Token
		if creds != nil {
			token = &clone.Token{GHToken: creds.GHToken, GitLabToken: creds.GitLabToken}
		}
		var cloneErr error
		localPath, cloneErr = clone.EnsureLocalClone(cfg, req.FullName, repoCfg, &logBuf, token)
		if cloneErr != nil {
			// Remove from config on clone failure
			delete(cfg.Repos, req.FullName)
			_ = cfg.Save()
			writeJSON(w, 500, addRemoteRepoResponse{
				Error: fmt.Sprintf("clone failed: %v\nLog:\n%s", cloneErr, logBuf.String()),
			})
			return
		}
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

// listGitHubRepos fetches repositories from GitHub API, following the
// `Link: rel="next"` pagination header so accounts with more than one page of
// repos (>100) are returned in full instead of being silently truncated.
func listGitHubRepos(token string) ([]RemoteRepo, error) {
	// GitHub API endpoint for listing user repos
	type ghRepo struct {
		FullName      string `json:"full_name"`
		Description   string `json:"description"`
		DefaultBranch string `json:"default_branch"`
		Private       bool   `json:"private"`
		HTMLURL       string `json:"html_url"`
	}

	url := fmt.Sprintf("https://api.github.com/user/repos?per_page=%d&sort=updated", remoteReposPerPage)
	var repos []RemoteRepo

	for page := 0; page < remoteReposMaxPages && url != ""; page++ {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

		resp, err := remoteHTTPClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != 200 {
			resp.Body.Close()
			return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
		}

		var ghRepos []ghRepo
		if err := json.NewDecoder(resp.Body).Decode(&ghRepos); err != nil {
			resp.Body.Close()
			return nil, err
		}
		nextURL := parseLinkNext(resp.Header.Get("Link"))
		resp.Body.Close()

		for _, r := range ghRepos {
			repos = append(repos, RemoteRepo{
				FullName:      r.FullName,
				Platform:      "github",
				Description:   r.Description,
				DefaultBranch: r.DefaultBranch,
				Private:       r.Private,
				HTMLURL:       r.HTMLURL,
			})
		}

		url = nextURL
	}

	return repos, nil
}

// parseLinkNext returns the next-page URL from a GitHub Link header, or "" if
// there is no further page.
func parseLinkNext(link string) string {
	if link == "" {
		return ""
	}
	if m := linkNextRe.FindStringSubmatch(link); m != nil {
		return m[1]
	}
	return ""
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

	var repos []RemoteRepo
	page := 1

	for fetched := 0; fetched < remoteReposMaxPages && page > 0; fetched++ {
		apiURL := fmt.Sprintf("%s/api/v4/projects?membership=true&per_page=%d&order_by=last_activity_at&page=%d",
			baseURL, remoteReposPerPage, page)
		req, err := http.NewRequest("GET", apiURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("PRIVATE-TOKEN", token)

		resp, err := remoteHTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to GitLab: %v", err)
		}

		if resp.StatusCode == 401 {
			resp.Body.Close()
			return nil, fmt.Errorf("GitLab token is invalid or expired. Please update your token in Settings and test the connection.")
		}
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("GitLab API returned status %d: %s", resp.StatusCode, string(body))
		}

		var glProjects []glProject
		if err := json.NewDecoder(resp.Body).Decode(&glProjects); err != nil {
			resp.Body.Close()
			return nil, err
		}
		// GitLab returns the next page number in X-Next-Page (empty/0 on last page).
		page = parseNextPage(resp.Header.Get("X-Next-Page"))
		resp.Body.Close()

		for _, p := range glProjects {
			repos = append(repos, RemoteRepo{
				FullName:      p.PathWithNamespace,
				Platform:      "gitlab",
				Description:   p.Description,
				DefaultBranch: p.DefaultBranch,
				Private:       p.Visibility == "private",
				HTMLURL:       p.WebURL,
				BaseURL:       baseURL,
			})
		}
	}

	return repos, nil
}

// parseNextPage parses GitLab's X-Next-Page header. It returns 0 when there is
// no further page (empty or unparseable header).
func parseNextPage(header string) int {
	if header == "" {
		return 0
	}
	n, err := strconv.Atoi(header)
	if err != nil {
		return 0
	}
	return n
}
