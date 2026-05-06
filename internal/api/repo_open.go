package api

import (
	"encoding/json"
	"net/http"
	"os/exec"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

type openRepoRequest struct {
	Repo string `json:"repo"`
}

// HandleRepoOpen handles POST /api/repo/open — opens the repo's local_path in VS Code.
func HandleRepoOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req openRepoRequest
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
	if repoCfg.LocalPath == "" {
		writeJSON(w, 400, map[string]string{"error": "no local_path configured for this repo"})
		return
	}

	if err := exec.Command("code", repoCfg.LocalPath).Start(); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, 200, map[string]string{"status": "ok"})
}
