package api

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

type repoBindRequest struct {
	Repo string `json:"repo"`
	Bind bool   `json:"bind"`
}

type repoBindResponse struct {
	Status       string `json:"status"`
	BoundMachine string `json:"bound_machine,omitempty"`
}

// HandleRepoBind handles POST /api/repo/bind.
// When bind=true, sets BoundMachine to the current machine's hostname.
// When bind=false, clears BoundMachine (unbinds the repo).
// BoundMachine is machine-specific and intentionally excluded from cloud sync.
func HandleRepoBind(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req repoBindRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Repo == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo is required"})
		return
	}

	cfg, err := config.Load()
	if err != nil {
		writeErr(w, err)
		return
	}

	repo, ok := cfg.Repos[req.Repo]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "repo not found in config"})
		return
	}

	if req.Bind {
		hostname, err := os.Hostname()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cannot determine hostname: " + err.Error()})
			return
		}
		repo.BoundMachine = hostname
	} else {
		repo.BoundMachine = ""
	}

	cfg.Repos[req.Repo] = repo
	if err := cfg.Save(); err != nil {
		writeErr(w, err)
		return
	}
	// Refresh repos.json so the frontend sees the updated binding state.
	if err := snapshot.WriteRepos(cfg); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, repoBindResponse{
		Status:       "ok",
		BoundMachine: repo.BoundMachine,
	})
}
