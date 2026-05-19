package api

import (
	"net/http"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/cloud"
	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// HandleCloudRepos serves GET /api/cloud/repos in two modes:
//
//   - If a cloud is configured (access token present), proxy the request
//     upstream so the browser sees the cloud's authoritative repo list.
//   - Otherwise translate the local ~/.clawflow/config/config.yaml into
//     the cloud Repo shape so the React Repos page can render its
//     existing filter / table UI without any frontend branching.
//
// The local translation is intentionally read-only — POST/DELETE/PATCH
// against /api/cloud/repos still 503 in unconfigured mode because there
// is no cloud Store to mutate.
func HandleCloudRepos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		// Write paths require the real cloud — proxy and let it answer
		// (or 503 if not configured).
		cloudProxy(w, r, "/api/cloud/repos")
		return
	}
	cfg, _ := cloud.LoadConfig()
	if cfg.AccessToken != "" {
		cloudProxy(w, r, "/api/cloud/repos")
		return
	}
	// Local mode: translate config.yaml → []cloud.Repo
	local, err := config.Load()
	if err != nil || local == nil {
		writeJSON(w, http.StatusOK, map[string]any{"repos": []cloud.Repo{}})
		return
	}
	now := time.Now().UTC()
	out := make([]cloud.Repo, 0, len(local.Repos))
	for name, r := range local.Repos {
		platform := r.Platform
		if platform == "" {
			platform = "github"
		}
		out = append(out, cloud.Repo{
			// Use full_name as the opaque ID so binding/filter wiring
			// has a stable handle. The cloud-side uses repo-XXXX
			// random IDs; local mode is single-user so colliding with
			// a real cloud ID is impossible.
			ID:         name,
			Name:       name,
			Platform:   platform,
			BaseBranch: r.BaseBranch,
			CreatedAt:  now,
			UpdatedAt:  now,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": out})
}

// HandleCloudProjects serves GET /api/cloud/projects. Local config has
// no project concept, so the unconfigured-mode path returns an empty
// list — the Repos page's project filter chip count stays at zero, and
// the Projects page renders its empty state.
func HandleCloudProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		cloudProxy(w, r, "/api/cloud/projects")
		return
	}
	cfg, _ := cloud.LoadConfig()
	if cfg.AccessToken != "" {
		cloudProxy(w, r, "/api/cloud/projects")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": []cloud.Project{}})
}

// HandleCloudMachinesLocal replaces the proxy-only HandleCloudMachines
// when local mode is in effect: returns one synthetic Machine per
// unique bound_machine value in config so the Repos page's
// "Last bound machine" column renders the hostname instead of "—".
func HandleCloudMachinesLocal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		HandleCloudMachines(w, r)
		return
	}
	cfg, _ := cloud.LoadConfig()
	if cfg.AccessToken != "" {
		HandleCloudMachines(w, r)
		return
	}
	local, err := config.Load()
	if err != nil || local == nil {
		writeJSON(w, http.StatusOK, map[string]any{"machines": []cloud.Machine{}})
		return
	}
	seen := map[string]bool{}
	now := time.Now().UTC()
	out := []cloud.Machine{}
	for _, r := range local.Repos {
		h := r.BoundMachine
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, cloud.Machine{
			ID:          h,
			Hostname:    h,
			DisplayName: h,
			LastSeenAt:  now,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"machines": out})
}

// HandleCloudBindingsLocal returns one Binding per repo whose
// bound_machine is set. machine_id reuses the hostname (matching the
// synthetic machine IDs above), repo_id reuses the repo's full_name
// (matching HandleCloudRepos).
func HandleCloudBindingsLocal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		HandleCloudBindings(w, r)
		return
	}
	cfg, _ := cloud.LoadConfig()
	if cfg.AccessToken != "" {
		HandleCloudBindings(w, r)
		return
	}
	local, err := config.Load()
	if err != nil || local == nil {
		writeJSON(w, http.StatusOK, map[string]any{"bindings": []cloud.Binding{}})
		return
	}
	now := time.Now().UTC()
	out := []cloud.Binding{}
	for name, r := range local.Repos {
		if r.BoundMachine == "" {
			continue
		}
		out = append(out, cloud.Binding{
			ID:        "local:" + name,
			MachineID: r.BoundMachine,
			RepoID:    name,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"bindings": out})
}
