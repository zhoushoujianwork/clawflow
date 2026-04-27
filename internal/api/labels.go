package api

import (
	"encoding/json"
	"net/http"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

type labelRequest struct {
	Repo   string   `json:"repo"`
	Issue  int      `json:"issue"`
	Labels []string `json:"labels"`
}

func clientForRepo(repo string) (vcs.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	repoCfg, ok := cfg.Repos[repo]
	if !ok {
		return nil, &httpError{Code: 404, Msg: "repo not found in config"}
	}
	creds, err := config.LoadCredentials()
	if err != nil {
		return nil, err
	}
	platform := repoCfg.Platform
	if platform == "" {
		platform = "github"
	}
	switch platform {
	case "github":
		return github.New(creds.GHToken, repoCfg.BaseURL), nil
	case "gitlab":
		return gitlab.New(creds.GitLabToken, repoCfg.BaseURL), nil
	default:
		return nil, &httpError{Code: 400, Msg: "unsupported platform"}
	}
}

type httpError struct {
	Code int
	Msg  string
}

func (e *httpError) Error() string { return e.Msg }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	if he, ok := err.(*httpError); ok {
		writeJSON(w, he.Code, map[string]string{"error": he.Msg})
		return
	}
	writeJSON(w, 500, map[string]string{"error": err.Error()})
}

// HandleAddLabel handles POST /api/labels/add
func HandleAddLabel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req labelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Repo == "" || req.Issue == 0 || len(req.Labels) == 0 {
		writeJSON(w, 400, map[string]string{"error": "repo, issue, and labels are required"})
		return
	}
	client, err := clientForRepo(req.Repo)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := client.AddLabel(req.Repo, req.Issue, req.Labels...); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}

// HandleRemoveLabel handles POST /api/labels/remove
func HandleRemoveLabel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req labelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Repo == "" || req.Issue == 0 || len(req.Labels) == 0 {
		writeJSON(w, 400, map[string]string{"error": "repo, issue, and labels are required"})
		return
	}
	client, err := clientForRepo(req.Repo)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := client.RemoveLabel(req.Repo, req.Issue, req.Labels...); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
