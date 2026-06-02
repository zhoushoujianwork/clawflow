package api

import (
	"encoding/json"
	"net/http"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/gitsync"
)

// HandleGitStatus handles GET /api/repo/git-status — returns the cached git
// sync status for every configured repo (read-only, instant). The dashboard
// reads this on render for an immediate view, then triggers an async refresh.
func HandleGitStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, 200, gitsync.ReadCache())
}

type gitRepoRequest struct {
	Repo string `json:"repo"`
}

// HandleGitStatusRefresh handles POST /api/repo/git-status/refresh — runs a
// `git fetch` and recomputes the sync status. With a "repo" field it refreshes
// just that repo (the per-row Hook); without one it refreshes all repos (the
// background backstop, also callable from the UI). Returns the updated
// entries.
func HandleGitStatusRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req gitRepoRequest
	// Body is optional; an empty/absent body means "refresh all".
	_ = json.NewDecoder(r.Body).Decode(&req)

	cfg, err := config.Load()
	if err != nil {
		writeErr(w, err)
		return
	}

	if req.Repo != "" {
		if _, ok := cfg.Repos[req.Repo]; !ok {
			writeJSON(w, 404, map[string]string{"error": "repo not found in config"})
			return
		}
		st := gitsync.Refresh(cfg, req.Repo)
		writeJSON(w, 200, []gitsync.Status{st})
		return
	}
	writeJSON(w, 200, gitsync.RefreshAll(cfg))
}

type gitActionResponse struct {
	Status string         `json:"status"` // "ok" | "error"
	Output string         `json:"output"` // git's combined stdout/stderr
	Error  string         `json:"error,omitempty"`
	Git    gitsync.Status `json:"git_status"`
}

// HandleGitPull handles POST /api/repo/git-pull — fast-forwards the base
// branch from origin for one repo and returns the refreshed status. Git
// failures (non-ff, dirty tree, auth) are returned as a 200 with status:error
// and the git output so the dashboard can surface the message inline for the
// user to resolve locally.
func HandleGitPull(w http.ResponseWriter, r *http.Request) {
	gitAction(w, r, gitsync.Pull)
}

// HandleGitPush handles POST /api/repo/git-push — pushes the base branch to
// origin for one repo and returns the refreshed status. Same error contract as
// HandleGitPull.
func HandleGitPush(w http.ResponseWriter, r *http.Request) {
	gitAction(w, r, gitsync.Push)
}

func gitAction(w http.ResponseWriter, r *http.Request, action func(*config.Config, string) (string, gitsync.Status, error)) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req gitRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Repo == "" {
		writeJSON(w, 400, map[string]string{"error": "repo is required"})
		return
	}
	cfg, err := config.Load()
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, ok := cfg.Repos[req.Repo]; !ok {
		writeJSON(w, 404, map[string]string{"error": "repo not found in config"})
		return
	}

	out, st, actionErr := action(cfg, req.Repo)
	resp := gitActionResponse{Status: "ok", Output: out, Git: st}
	if actionErr != nil {
		resp.Status = "error"
		resp.Error = actionErr.Error()
	}
	// 200 even on git failure: the operation completed cleanly at the HTTP
	// layer and the failure is a domain result the UI renders inline, not a
	// server error.
	writeJSON(w, 200, resp)
}
