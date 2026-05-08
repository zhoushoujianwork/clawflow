package api

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

type repoBindRequest struct {
	Repo  string   `json:"repo"`
	Repos []string `json:"repos,omitempty"`
	Bind  bool     `json:"bind"`
}

type repoBindResponse struct {
	Status       string            `json:"status"`
	BoundMachine string            `json:"bound_machine,omitempty"`
	Results      map[string]string `json:"results,omitempty"`
}

// HandleRepoBind handles POST /api/repo/bind.
// When bind=true, sets BoundMachine to the current machine's hostname.
// When bind=false, clears BoundMachine (unbinds the repo).
// Supports both single-repo (repo field) and batch (repos field) modes.
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

	// Normalize: single-repo mode → batch of one
	targets := req.Repos
	if len(targets) == 0 {
		if req.Repo == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repo or repos is required"})
			return
		}
		targets = []string{req.Repo}
	}

	cfg, err := config.Load()
	if err != nil {
		writeErr(w, err)
		return
	}

	hostname := ""
	if req.Bind {
		h, err := os.Hostname()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cannot determine hostname: " + err.Error()})
			return
		}
		hostname = h
	}

	results := make(map[string]string, len(targets))
	for _, name := range targets {
		repo, ok := cfg.Repos[name]
		if !ok {
			continue
		}
		if req.Bind {
			repo.BoundMachine = hostname
		} else {
			repo.BoundMachine = ""
		}
		cfg.Repos[name] = repo
		results[name] = repo.BoundMachine
	}

	if err := cfg.Save(); err != nil {
		writeErr(w, err)
		return
	}
	if err := snapshot.WriteRepos(cfg); err != nil {
		writeErr(w, err)
		return
	}

	// Single-repo mode: backward-compatible response shape
	if req.Repo != "" && len(req.Repos) == 0 {
		writeJSON(w, http.StatusOK, repoBindResponse{
			Status:       "ok",
			BoundMachine: results[req.Repo],
		})
		return
	}

	writeJSON(w, http.StatusOK, repoBindResponse{
		Status:  "ok",
		Results: results,
	})
}
