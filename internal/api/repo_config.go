package api

import (
	"encoding/json"
	"net/http"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

type repoConfigRequest struct {
	Repo         string `json:"repo"`
	Enabled      *bool  `json:"enabled,omitempty"`
	AutoApprove  *bool  `json:"auto_approve,omitempty"`
	AutoMerge    *bool  `json:"auto_merge,omitempty"`
	AutoMergeFix *bool  `json:"auto_merge_fix,omitempty"`
}

type repoConfigResponse struct {
	Status       string `json:"status"`
	Enabled      bool   `json:"enabled"`
	AutoApprove  bool   `json:"auto_approve"`
	AutoMerge    bool   `json:"auto_merge"`
	AutoMergeFix bool   `json:"auto_merge_fix"`
}

// HandleRepoConfig handles POST /api/repo/config — updates repo toggles.
func HandleRepoConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req repoConfigRequest
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

	repo, ok := cfg.Repos[req.Repo]
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "repo not found in config"})
		return
	}

	if req.Enabled != nil {
		repo.Enabled = *req.Enabled
	}
	if req.AutoApprove != nil {
		repo.AutoApprove = *req.AutoApprove
	}
	if req.AutoMerge != nil {
		repo.AutoMerge = *req.AutoMerge
	}
	if req.AutoMergeFix != nil {
		repo.AutoMergeFix = *req.AutoMergeFix
	}

	cfg.Repos[req.Repo] = repo
	if err := cfg.Save(); err != nil {
		writeErr(w, err)
		return
	}
	// Refresh data/repos.json so the frontend's next /data/repos.json
	// fetch sees the new toggle states. Without this, a hard refresh
	// shows the pre-edit values from the last `clawflow run` snapshot.
	if err := snapshot.WriteRepos(cfg); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, 200, repoConfigResponse{
		Status:       "ok",
		Enabled:      repo.Enabled,
		AutoApprove:  repo.AutoApprove,
		AutoMerge:    repo.AutoMerge,
		AutoMergeFix: repo.AutoMergeFix,
	})
}
