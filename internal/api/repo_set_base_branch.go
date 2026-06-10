package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/gitsync"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

type setBaseBranchRequest struct {
	Repo      string `json:"repo"`
	NewBranch string `json:"branch"`
}

type setBaseBranchResponse struct {
	Status     string         `json:"status"`
	HeadAction string         `json:"head_action"` // "switched" | "kept"
	GitStatus  gitsync.Status `json:"git_status"`
}

// HandleRepoSetBaseBranch handles POST /api/repo/set-base-branch.
// Updates config.Repo.BaseBranch, saves config, refreshes snapshot,
// pushes to Gist, and re-computes gitsync status. If HEAD == old base
// and worktree is clean, also checks out the new branch.
func HandleRepoSetBaseBranch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req setBaseBranchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Repo == "" || req.NewBranch == "" {
		writeJSON(w, 400, map[string]string{"error": "repo and branch are required"})
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

	local := gitsync.LocalPath(cfg, req.Repo)
	if local == "" {
		writeJSON(w, 404, map[string]string{"error": "repo not cloned locally"})
		return
	}

	// Capture current state before mutation.
	oldBase := baseBranchFromCfg(cfg, req.Repo)
	oldStatus := gitsync.Refresh(cfg, req.Repo)

	// Update BaseBranch in config.
	config.TouchRepo(cfg, req.Repo, func(r config.Repo) config.Repo {
		r.BaseBranch = req.NewBranch
		return r
	})

	if err := cfg.Save(); err != nil {
		writeErr(w, err)
		return
	}
	if err := snapshot.WriteRepos(cfg); err != nil {
		// Non-fatal: config already saved.
		fmt.Fprintf(os.Stderr, "⚠ set-base-branch: refresh repos.json: %v\n", err)
	}
	AutoPush()

	// Optionally checkout the new branch when HEAD == old base and clean.
	headAction := "kept"
	if oldStatus.Current == oldBase && !oldStatus.Dirty {
		if checkoutErr := gitCheckout(local, req.NewBranch); checkoutErr == nil {
			headAction = "switched"
		} else {
			fmt.Fprintf(os.Stderr, "⚠ set-base-branch: checkout %s: %v\n", req.NewBranch, checkoutErr)
		}
	}

	// Recompute sync status with new base.
	newStatus := gitsync.Refresh(cfg, req.Repo)

	writeJSON(w, 200, setBaseBranchResponse{
		Status:     "ok",
		HeadAction: headAction,
		GitStatus:  newStatus,
	})
}

func gitCheckout(dir, branch string) error {
	c := exec.Command("git", "checkout", branch)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout %s: %w\n%s", branch, err, string(out))
	}
	return nil
}
