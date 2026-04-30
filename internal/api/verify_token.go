package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

type verifyTokenRequest struct {
	Platform string `json:"platform"` // "github" or "gitlab"
}

type verifyTokenResponse struct {
	Valid   bool   `json:"valid"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// HandleVerifyToken handles POST /api/settings/verify-token
// Tests if the configured token for a platform is valid.
func HandleVerifyToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req verifyTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, verifyTokenResponse{Error: "invalid JSON"})
		return
	}

	if req.Platform != "github" && req.Platform != "gitlab" {
		writeJSON(w, 400, verifyTokenResponse{Error: "platform must be 'github' or 'gitlab'"})
		return
	}

	creds, err := config.LoadCredentials()
	if err != nil {
		writeJSON(w, 500, verifyTokenResponse{Error: fmt.Sprintf("load credentials: %v", err)})
		return
	}

	var valid bool
	var message string
	var verifyErr error

	switch req.Platform {
	case "github":
		if creds.GHToken == "" {
			writeJSON(w, 200, verifyTokenResponse{
				Valid:   false,
				Message: "GitHub token not configured",
			})
			return
		}
		valid, message, verifyErr = verifyGitHubToken(creds.GHToken)
	case "gitlab":
		if creds.GitLabToken == "" {
			writeJSON(w, 200, verifyTokenResponse{
				Valid:   false,
				Message: "GitLab token not configured",
			})
			return
		}
		cfg, err := config.Load()
		if err != nil {
			writeJSON(w, 500, verifyTokenResponse{Error: fmt.Sprintf("load config: %v", err)})
			return
		}
		baseURL := "https://gitlab.com"
		if len(cfg.Settings.GitLabHosts) > 0 {
			baseURL = normalizeGitLabURL(cfg.Settings.GitLabHosts[0])
		}
		valid, message, verifyErr = verifyGitLabToken(creds.GitLabToken, baseURL)
	}

	if verifyErr != nil {
		writeJSON(w, 200, verifyTokenResponse{
			Valid:   false,
			Message: verifyErr.Error(),
		})
		return
	}

	writeJSON(w, 200, verifyTokenResponse{
		Valid:   valid,
		Message: message,
	})
}

// verifyGitHubToken tests if a GitHub token is valid by calling /user endpoint.
func verifyGitHubToken(token string) (bool, string, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, "", fmt.Errorf("network error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		var user struct {
			Login string `json:"login"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&user); err == nil && user.Login != "" {
			return true, fmt.Sprintf("Connected as @%s", user.Login), nil
		}
		return true, "Token is valid", nil
	}

	if resp.StatusCode == 401 {
		return false, "Invalid token or token expired", nil
	}

	return false, fmt.Sprintf("GitHub API returned status %d", resp.StatusCode), nil
}

// verifyGitLabToken tests if a GitLab token is valid by calling /user endpoint.
func verifyGitLabToken(token, baseURL string) (bool, string, error) {
	req, err := http.NewRequest("GET", baseURL+"/api/v4/user", nil)
	if err != nil {
		return false, "", err
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, "", fmt.Errorf("network error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		var user struct {
			Username string `json:"username"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&user); err == nil && user.Username != "" {
			return true, fmt.Sprintf("Connected as @%s", user.Username), nil
		}
		return true, "Token is valid", nil
	}

	if resp.StatusCode == 401 {
		return false, "Invalid token or token expired", nil
	}

	return false, fmt.Sprintf("GitLab API returned status %d", resp.StatusCode), nil
}
