package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	rootmod "github.com/zhoushoujianwork/clawflow"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
	clawsync "github.com/zhoushoujianwork/clawflow/internal/sync"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
)

// refreshSnapshotsAfterPull rewrites the static JSON snapshots the dashboard
// reads (/data/repos.json, /data/projects.json, /data/operators.json) so the
// frontend's next fetch reflects the freshly merged config and any custom
// skills restored from the Gist. Best-effort: failures are logged but never
// abort the surrounding pull.
func refreshSnapshotsAfterPull() {
	if cfg, err := config.Load(); err == nil {
		if werr := snapshot.WriteRepos(cfg); werr != nil {
			fmt.Fprintf(os.Stderr, "⚠ sync pull: refresh repos.json: %v\n", werr)
		}
	} else {
		fmt.Fprintf(os.Stderr, "⚠ sync pull: reload config for snapshot: %v\n", err)
	}
	if werr := snapshot.WriteProjects(); werr != nil {
		fmt.Fprintf(os.Stderr, "⚠ sync pull: refresh projects.json: %v\n", werr)
	}
	if reg, err := loadOperatorRegistry(); err == nil {
		if werr := snapshot.WriteOperators(reg); werr != nil {
			fmt.Fprintf(os.Stderr, "⚠ sync pull: refresh operators.json: %v\n", werr)
		}
	} else {
		fmt.Fprintf(os.Stderr, "⚠ sync pull: rebuild operator registry: %v\n", err)
	}
}

// loadOperatorRegistry mirrors the loadRegistry helper used by `clawflow
// run` and `clawflow web` startup: embedded operators first, then any user
// overrides under ~/.clawflow/skills/. Duplicated here (rather than imported)
// because that helper lives in the cmd/clawflow package which cannot be
// reached from internal/api.
func loadOperatorRegistry() (*operator.Registry, error) {
	reg := operator.NewRegistry()
	if err := reg.LoadEmbedded(rootmod.EmbeddedSkills, "skills"); err != nil {
		return nil, fmt.Errorf("load embedded operators: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return reg, nil // no user dir reachable — embedded is enough
	}
	userDir := filepath.Join(home, ".clawflow", "skills")
	if err := reg.LoadUserDir(userDir); err != nil {
		return nil, fmt.Errorf("load user operators from %s: %w", userDir, err)
	}
	return reg, nil
}

// syncStatusResponse is returned by GET /api/sync/status.
// Credentials and local_path are intentionally absent.
type syncStatusResponse struct {
	GistID      string `json:"gist_id"`
	GHTokenSet  bool   `json:"gh_token_set"`
	LastSyncedAt string `json:"last_synced_at,omitempty"`
}

// HandleSyncStatus handles GET /api/sync/status.
// Returns the current sync state: whether a token is configured, the stored
// Gist ID, and the last-synced timestamp (if recorded).
func HandleSyncStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	creds, err := config.LoadCredentials()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	resp := syncStatusResponse{}
	if creds != nil {
		resp.GHTokenSet = creds.GHToken != ""
		resp.GistID = creds.GistID
		resp.LastSyncedAt = creds.LastSyncedAt
	}
	writeJSON(w, http.StatusOK, resp)
}

// syncPushResponse is returned by POST /api/sync/push.
type syncPushResponse struct {
	Status string `json:"status"`
	GistID string `json:"gist_id,omitempty"`
	Error  string `json:"error,omitempty"`
}

// HandleSyncPush handles POST /api/sync/push.
// Serialises the local config and uploads it to the clawflow-config Gist,
// creating one if it doesn't exist yet.
func HandleSyncPush(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check token first — fail fast before touching the config file.
	gh, gistID, err := clawsync.Client()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, syncPushResponse{Error: err.Error()})
		return
	}

	files, err := clawsync.BuildAllGistFiles()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, syncPushResponse{Error: "cannot build sync payload: " + err.Error()})
		return
	}

	newGistID, err := clawsync.PushAllToGist(gh, gistID, files)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, syncPushResponse{Error: err.Error()})
		return
	}

	if err := clawsync.SaveGistID(newGistID); err != nil {
		// Non-fatal: the push succeeded; warn via the response but don't fail.
		writeJSON(w, http.StatusOK, syncPushResponse{
			Status: "ok",
			GistID: newGistID,
			Error:  "push succeeded but could not persist Gist ID: " + err.Error(),
		})
		return
	}

	// Record the sync timestamp.
	_ = recordLastSynced()

	writeJSON(w, http.StatusOK, syncPushResponse{Status: "ok", GistID: newGistID})
}

// syncPullResponse is returned by POST /api/sync/pull.
type syncPullResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// HandleSyncPull handles POST /api/sync/pull.
// Fetches the clawflow-config Gist and merges it into the local config using
// the field-level merge strategy (settings: cloud wins; repos: union merge).
func HandleSyncPull(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	gh, gistID, err := clawsync.Client()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, syncPullResponse{Error: err.Error()})
		return
	}
	if gistID == "" {
		writeJSON(w, http.StatusBadRequest, syncPullResponse{
			Error: "no Gist ID found — run sync push first or use /api/login to set up sync",
		})
		return
	}

	remoteYAML, err := clawsync.FetchGistContent(gh, gistID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, syncPullResponse{Error: err.Error()})
		return
	}

	if err := config.ApplyGistConfig(remoteYAML); err != nil {
		writeJSON(w, http.StatusInternalServerError, syncPullResponse{Error: "merge failed: " + err.Error()})
		return
	}

	if err := clawsync.FetchAndApplyProjectAssets(gh, gistID); err != nil {
		// Non-fatal: config was already applied; surface as a warning in the response.
		fmt.Fprintf(os.Stderr, "⚠ sync pull: could not restore project assets: %v\n", err)
	}

	// Rewrite the snapshot files so the dashboard's next /data/*.json
	// fetch reflects the merged config (otherwise the UI would still
	// show the pre-pull repo/project list until clawflow run or a
	// subsequent config-mutating API call rewrites the snapshots).
	refreshSnapshotsAfterPull()

	// Record the sync timestamp.
	_ = recordLastSynced()

	writeJSON(w, http.StatusOK, syncPullResponse{Status: "ok"})
}

// loginRequest is the body accepted by POST /api/login.
type loginRequest struct {
	Token string `json:"token"`
}

// loginResponse is returned by POST /api/login.
type loginResponse struct {
	Status string `json:"status"`
	Login  string `json:"login,omitempty"`
	GistID string `json:"gist_id,omitempty"`
	Error  string `json:"error,omitempty"`
}

// HandleLogin handles POST /api/login.
// Accepts {"token": "ghp_xxx"}, validates it via GET /user, discovers or
// creates the clawflow-config Gist, and persists the token + Gist ID.
// The token is never echoed back in the response.
func HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, loginResponse{Error: "invalid JSON"})
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		writeJSON(w, http.StatusBadRequest, loginResponse{Error: "token is required"})
		return
	}

	gh := github.New(token, "")

	// 1. Validate the token.
	login, err := gh.GetAuthenticatedUser()
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, loginResponse{Error: "token validation failed: " + err.Error()})
		return
	}

	// 2. Load existing credentials to preserve other fields.
	creds, err := config.LoadCredentials()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, loginResponse{Error: "cannot load credentials: " + err.Error()})
		return
	}
	if creds == nil {
		creds = &config.Credentials{}
	}
	creds.GHToken = token

	// 3. Discover or create the sync Gist.
	gistID, err := discoverOrCreateGistAPI(gh, creds)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, loginResponse{Error: err.Error()})
		return
	}
	creds.GistID = gistID

	// 4. Persist credentials. Token is stored but never returned.
	if err := config.SaveCredentials(creds); err != nil {
		writeJSON(w, http.StatusInternalServerError, loginResponse{Error: "cannot save credentials: " + err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, loginResponse{
		Status: "ok",
		Login:  login,
		GistID: gistID,
	})
}

// discoverOrCreateGistAPI is the API-layer equivalent of the CLI's
// discoverOrCreateGist. It finds the clawflow-config Gist or creates one,
// pulling config on discovery and pushing on creation.
func discoverOrCreateGistAPI(gh *github.Client, creds *config.Credentials) (string, error) {
	// If we already have a stored Gist ID, verify it still exists.
	if creds.GistID != "" {
		gist, err := gh.GetGist(creds.GistID)
		if err == nil && gist != nil {
			return gist.ID, nil
		}
		// Gist gone — fall through to search.
	}

	// Search for an existing Gist with the canonical description.
	gist, err := gh.FindGistByDescription(config.GistDescription)
	if err != nil {
		return "", fmt.Errorf("cannot search gists: %w", err)
	}

	if gist != nil {
		// Found — pull config from it (best-effort).
		if content, ferr := clawsync.FetchGistContent(gh, gist.ID); ferr == nil {
			_ = config.ApplyGistConfig(content)
		}
		return gist.ID, nil
	}

	// No existing Gist — create one and push current local config.
	content, cerr := clawsync.BuildGistContent()
	if cerr != nil {
		// Seed with an empty placeholder if there's no local config yet.
		content = "# clawflow config — managed by clawflow login\nrepos: {}\n"
	}
	newGist, err := gh.CreateGist(config.GistDescription, map[string]string{
		config.GistConfigFilename: content,
	})
	if err != nil {
		return "", fmt.Errorf("cannot create config Gist: %w", err)
	}
	return newGist.ID, nil
}

// AutoPull performs a best-effort config pull from the sync Gist. It is
// called programmatically at startup (clawflow run, clawflow web) without
// user interaction. Errors are non-fatal: if the Gist is unreachable or no
// token is configured, we log and continue with the local config.
// Returns true when the pull succeeded and the local config was updated.
//
// Manual-edit detection: if config.yaml's mtime is newer than the stored
// LastPulledAt timestamp, the user edited the file directly since the last
// pull. In that case we stamp the changed entries and push instead of pull,
// so the local edits win and propagate to other machines.
func AutoPull() bool {
	gh, gistID, err := clawsync.Client()
	if err != nil {
		// No token configured — sync not set up, silently skip.
		return false
	}
	if gistID == "" {
		return false
	}

	// Manual-edit detection: compare config.yaml mtime against LastPulledAt.
	if locallyEdited() {
		fmt.Fprintf(os.Stderr, "⚠ auto-pull: local config.yaml was edited since last pull — pushing local changes instead\n")
		return AutoPush()
	}

	remoteYAML, err := clawsync.FetchGistContent(gh, gistID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ auto-pull: fetch Gist: %v\n", err)
		return false
	}
	// ApplyGistConfig records LastPulledAt on success.
	if err := config.ApplyGistConfig(remoteYAML); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ auto-pull: apply config: %v\n", err)
		return false
	}
	if err := clawsync.FetchAndApplyProjectAssets(gh, gistID); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ auto-pull: restore project assets: %v\n", err)
		// Non-fatal: config was applied; continue.
	}
	_ = recordLastSynced()
	return true
}

// locallyEdited reports whether config.yaml has been modified since the last
// successful pull. Returns false (safe default) when the timestamp cannot be
// determined.
func locallyEdited() bool {
	creds, err := config.LoadCredentials()
	if err != nil || creds == nil || creds.LastPulledAt == "" {
		return false
	}
	lastPulled, err := time.Parse(time.RFC3339, creds.LastPulledAt)
	if err != nil {
		return false
	}
	info, err := os.Stat(config.ConfigPath())
	if err != nil {
		return false
	}
	return info.ModTime().After(lastPulled)
}

// AutoPush performs a best-effort config push to the sync Gist. It is
// called programmatically at the end of clawflow run. Errors are non-fatal.
// Returns true when the push succeeded.
func AutoPush() bool {
	gh, gistID, err := clawsync.Client()
	if err != nil {
		return false
	}
	files, err := clawsync.BuildAllGistFiles()
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ auto-push: build content: %v\n", err)
		return false
	}
	newGistID, err := clawsync.PushAllToGist(gh, gistID, files)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ auto-push: push Gist: %v\n", err)
		return false
	}
	_ = clawsync.SaveGistID(newGistID)
	_ = recordLastSynced()
	return true
}

// recordLastSynced stamps the current UTC time into credentials.yaml so
// GET /api/sync/status can surface it. Best-effort: failures are silently
// ignored because the sync itself already succeeded.
func recordLastSynced() error {
	creds, err := config.LoadCredentials()
	if err != nil {
		return err
	}
	if creds == nil {
		creds = &config.Credentials{}
	}
	creds.LastSyncedAt = time.Now().UTC().Format(time.RFC3339)
	return config.SaveCredentials(creds)
}
