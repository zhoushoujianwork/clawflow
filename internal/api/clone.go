package api

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/zhoushoujianwork/clawflow/internal/clone"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

type cloneRequest struct {
	Repo string `json:"repo"`
}

type cloneResponse struct {
	Status    string `json:"status"`
	LocalPath string `json:"local_path"`
	Log       string `json:"log,omitempty"`
}

// HandleClone handles POST /api/repo/clone — provisions the local
// clone for a configured repo (idempotent: reuses existing clones,
// pulls down a fresh one if the path doesn't exist) and writes the
// resulting LocalPath back to config. Returns the resolved path plus
// any git output for the UI to surface on failure.
//
// Synchronous: a fresh clone of a multi-GB repo can take a while, so
// the frontend should disable the button and show a spinner until
// this returns. We intentionally don't background the work — partial
// state on disk with no path saved would confuse the next caller.
func HandleClone(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req cloneRequest
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
	repoCfg, ok := cfg.Repos[req.Repo]
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "repo not found in config"})
		return
	}

	var log bytes.Buffer
	localPath, err := clone.EnsureLocalClone(cfg, req.Repo, repoCfg, &log)
	if err != nil {
		writeJSON(w, 500, map[string]string{
			"error": err.Error(),
			"log":   log.String(),
		})
		return
	}

	// EnsureLocalClone wrote local_path back into config.yaml; mirror
	// it into data/repos.json so the next dashboard load reflects the
	// new path without waiting for a `clawflow run` to refresh the
	// snapshot. Best-effort — don't 500 the clone result if the
	// snapshot write fails.
	_ = snapshot.WriteRepos(cfg)

	writeJSON(w, 200, cloneResponse{
		Status:    "ok",
		LocalPath: localPath,
		Log:       log.String(),
	})
}
