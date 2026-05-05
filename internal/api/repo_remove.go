package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

type repoRemoveRequest struct {
	Repos []string `json:"repos"` // list of "owner/repo" to soft-delete
}

type repoRemoveResponse struct {
	Status  string   `json:"status"`
	Removed []string `json:"removed"`
	Skipped []string `json:"skipped,omitempty"` // repos not found in config
}

// HandleRepoRemove handles POST /api/repo/remove — soft-deletes repos from config.
// Only removes from config.yaml; local clones are left untouched.
func HandleRepoRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req repoRemoveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	if len(req.Repos) == 0 {
		writeJSON(w, 400, map[string]string{"error": "repos list is required"})
		return
	}

	cfg, err := config.Load()
	if err != nil {
		writeErr(w, err)
		return
	}

	var removed, skipped []string
	for _, name := range req.Repos {
		if _, exists := cfg.Repos[name]; !exists {
			skipped = append(skipped, name)
			continue
		}
		delete(cfg.Repos, name)
		removed = append(removed, name)
	}

	if len(removed) == 0 {
		writeJSON(w, 404, map[string]string{"error": fmt.Sprintf("none of the specified repos found in config")})
		return
	}

	if err := cfg.Save(); err != nil {
		writeErr(w, err)
		return
	}
	if err := snapshot.WriteRepos(cfg); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, 200, repoRemoveResponse{
		Status:  "ok",
		Removed: removed,
		Skipped: skipped,
	})
}
