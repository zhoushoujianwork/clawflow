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
	// Machine, if non-empty AND bind=true, pins the repo to this
	// specific hostname instead of the current machine. Lets the
	// dashboard reassign ownership across a known fleet without
	// SSH'ing into each box. Ignored when bind=false (unbind clears
	// the field regardless).
	Machine string `json:"machine,omitempty"`
}

type repoBindResponse struct {
	Status       string            `json:"status"`
	BoundMachine string            `json:"bound_machine,omitempty"`
	Results      map[string]string `json:"results,omitempty"`
}

// HandleRepoBind handles POST /api/repo/bind.
// When bind=true and machine is empty, sets BoundMachine to the current
// machine's hostname. When bind=true and machine is non-empty, pins
// BoundMachine to that value (used by the dashboard's bound-machine
// dropdown to reassign repos across a known fleet). When bind=false,
// clears BoundMachine (unbinds the repo).
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
		if req.Machine != "" {
			// Explicit target machine — trust the caller. The dashboard
			// only offers hostnames that already appear in existing
			// bound_machine values, so typos are unlikely.
			hostname = req.Machine
		} else {
			h, err := os.Hostname()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cannot determine hostname: " + err.Error()})
				return
			}
			hostname = h
		}
	}

	results := make(map[string]string, len(targets))
	for _, name := range targets {
		if _, ok := cfg.Repos[name]; !ok {
			continue
		}
		config.TouchRepo(cfg, name, func(r config.Repo) config.Repo {
			if req.Bind {
				r.BoundMachine = hostname
			} else {
				r.BoundMachine = ""
			}
			return r
		})
		results[name] = cfg.Repos[name].BoundMachine
	}

	if err := cfg.Save(); err != nil {
		writeErr(w, err)
		return
	}
	if err := snapshot.WriteRepos(cfg); err != nil {
		writeErr(w, err)
		return
	}

	// Push the new bound_machine to Gist synchronously. Without this, the
	// next AutoPull (clawflow run / web startup) would silently revert
	// the local change to whatever the cloud last had — that's the
	// "I bound it but after restart it came back" bug.
	AutoPush()

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
