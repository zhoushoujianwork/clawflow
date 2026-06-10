package api

import (
	"net/http"
	"os/exec"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/branch"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/gitsync"
)

type branchesResponse struct {
	Branches   []branchEntry `json:"branches"`
	Base       string        `json:"base"`
	Current    string        `json:"current"`
	FetchError string        `json:"fetch_error,omitempty"`
}

type branchEntry struct {
	Name    string `json:"name"`
	Remote  bool   `json:"remote"`
	IsBase  bool   `json:"is_base"`
	Current bool   `json:"is_current"`
}

// HandleRepoBranches handles GET /api/repo/branches?repo=...
// Returns the local and remote-tracking branches for a repo,
// annotated with which one is the configured base and which is HEAD.
func HandleRepoBranches(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	repo := r.URL.Query().Get("repo")
	if repo == "" {
		writeJSON(w, 400, map[string]string{"error": "repo is required"})
		return
	}

	cfg, err := config.Load()
	if err != nil {
		writeErr(w, err)
		return
	}
	if _, ok := cfg.Repos[repo]; !ok {
		writeJSON(w, 404, map[string]string{"error": "repo not found in config"})
		return
	}

	local := gitsync.LocalPath(cfg, repo)
	if local == "" {
		writeJSON(w, 404, map[string]string{"error": "repo not cloned locally"})
		return
	}

	base := baseBranchFromCfg(cfg, repo)

	// Fetch remote refs; tolerate failure and return stale cached refs.
	var fetchErr string
	if err := branch.Fetch(local); err != nil {
		fetchErr = err.Error()
	}

	current := headBranch(local)

	localOut, _ := gitForEachRef(local, "refs/heads/")
	localNames := parseTabRefLines(localOut, false)

	remoteOut, _ := gitForEachRef(local, "refs/remotes/origin/")
	remoteNames := parseTabRefLines(remoteOut, true)

	// Deduplicate: skip remote branches that already exist locally.
	localSet := map[string]bool{}
	for _, n := range localNames {
		localSet[n] = true
	}

	var entries []branchEntry
	for _, name := range localNames {
		if name == "HEAD" {
			continue
		}
		entries = append(entries, branchEntry{
			Name:    name,
			Remote:  false,
			IsBase:  name == base,
			Current: name == current,
		})
	}
	for _, name := range remoteNames {
		if name == "HEAD" || localSet[name] {
			continue
		}
		entries = append(entries, branchEntry{
			Name:    name,
			Remote:  true,
			IsBase:  name == base,
			Current: name == current,
		})
	}

	writeJSON(w, 200, branchesResponse{
		Branches:   entries,
		Base:       base,
		Current:    current,
		FetchError: fetchErr,
	})
}

// parseTabRefLines parses TAB-delimited "name\tdate" output from gitForEachRef.
func parseTabRefLines(out string, remote bool) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name := strings.SplitN(line, "\t", 2)[0]
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if remote {
			name = strings.TrimPrefix(name, "origin/")
		}
		names = append(names, name)
	}
	return names
}

// gitForEachRef lists refs under pattern using TAB as field separator.
// TAB avoids the NUL-in-args exec rejection on macOS.
func gitForEachRef(dir, pattern string) (string, error) {
	c := exec.Command("git", "for-each-ref", "--format=%(refname:short)\t%(committerdate:unix)", pattern)
	c.Dir = dir
	out, err := c.Output()
	return string(out), err
}

// headBranch returns the currently checked-out branch name (short form).
func headBranch(local string) string {
	c := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	c.Dir = local
	out, err := c.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// baseBranchFromCfg returns the configured base branch for a repo, defaulting to "main".
func baseBranchFromCfg(cfg *config.Config, repo string) string {
	if cfg != nil {
		if r, ok := cfg.Repos[repo]; ok && r.BaseBranch != "" {
			return r.BaseBranch
		}
	}
	return "main"
}
