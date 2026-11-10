package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/devmode"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

type labelRequest struct {
	Repo   string   `json:"repo"`
	Issue  int      `json:"issue"`
	Labels []string `json:"labels"`
}

// resolveConfigured maps a request's repo argument to its configured entry with
// the same rules as the CLI (config.Resolve). An ambiguous shorthand is a 409,
// an unknown repository a 404; nothing falls back to another site.
func resolveConfigured(cfg *config.Config, input string) (string, config.Repo, error) {
	t, err := cfg.ResolveConfigured(input)
	if err != nil {
		var amb *config.AmbiguousError
		if errors.As(err, &amb) {
			return "", config.Repo{}, &httpError{Code: 409, Msg: err.Error()}
		}
		return "", config.Repo{}, &httpError{Code: 404, Msg: "repo not found in config: " + err.Error()}
	}
	return t.Key, t.Repo, nil
}

func clientForRepo(repo string) (vcs.Client, error) {
	client, _, err := clientAndKeyForRepo(repo)
	return client, err
}

// clientAndKeyForRepo returns the client together with the canonical key that
// client methods must be called with.
func clientAndKeyForRepo(input string) (vcs.Client, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "", err
	}
	repo, repoCfg, err := resolveConfigured(cfg, input)
	if err != nil {
		return nil, "", err
	}
	client, err := buildClient(repoCfg)
	return client, repo, err
}

func buildClient(repoCfg config.Repo) (vcs.Client, error) {
	creds, err := config.LoadCredentials()
	if err != nil {
		return nil, err
	}
	if devmode.Active() {
		if err := devmode.CheckBaseURL(repoCfg.BaseURL); err != nil {
			return nil, &httpError{Code: 403, Msg: err.Error()}
		}
	}
	platform := repoCfg.Platform
	if platform == "" {
		platform = "github"
	}
	switch platform {
	case "github":
		return github.New(creds.GHToken, repoCfg.BaseURL), nil
	case "gitlab":
		return gitlabClientForRepo(repoCfg, creds)
	default:
		return nil, &httpError{Code: 400, Msg: "unsupported platform"}
	}
}

// gitlabClientForRepo builds the client of the site a repository is bound to,
// with that site's credential only (see cmd gitlabSiteClient).
func gitlabClientForRepo(repo config.Repo, creds *config.Credentials) (*gitlab.Client, error) {
	if repo.BaseURL == "" {
		return nil, &httpError{Code: 400, Msg: "repo platform is gitlab but base_url is not set"}
	}
	if repo.Site == "" {
		return gitlab.New(creds.GitLabToken, repo.BaseURL), nil
	}
	if repo.SiteCfg == nil {
		return nil, &httpError{Code: 400, Msg: "GitLab site " + repo.Site + " is not registered"}
	}
	token, _ := creds.SiteToken(*repo.SiteCfg)
	if token == "" {
		return nil, &httpError{Code: 401, Msg: (&vcs.AuthError{Site: repo.Site, URL: repo.SiteCfg.URL, NoCred: true}).Error()}
	}
	return gitlab.NewForSite(gitlab.Site{Name: repo.Site, URL: repo.SiteCfg.URL, Token: token, Unsupported: repo.SiteCfg.Unsupported}), nil
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
	client, key, err := clientAndKeyForRepo(req.Repo)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := client.AddLabel(key, req.Issue, req.Labels...); err != nil {
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
	client, key, err := clientAndKeyForRepo(req.Repo)
	if err != nil {
		writeErr(w, err)
		return
	}
	if err := client.RemoveLabel(key, req.Issue, req.Labels...); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ok"})
}
