package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	clawsync "github.com/zhoushoujianwork/clawflow/internal/sync"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
)

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

	content, err := clawsync.BuildGistContent()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, syncPushResponse{Error: "cannot build config payload: " + err.Error()})
		return
	}

	newGistID, err := clawsync.PushToGist(gh, gistID, content)
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
