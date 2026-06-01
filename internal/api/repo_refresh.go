package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

type repoRefreshIssuesRequest struct {
	Repo string `json:"repo"`
}

type repoRefreshIssuesResponse struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

// HandleRepoRefreshIssues handles POST /api/repo/refresh-issues — re-pulls
// the current open/closed state for every issue in one repo and rewrites
// that repo's slice of issues.json. Unlike POST /api/run this does NOT
// trigger any operator execution; it's the cheap path the dashboard's
// per-repo Sync button uses to reconcile stale state.
//
// Backed by ListIssues(state="all"), which returns up to 100 issues
// sorted by updated_at desc. For repos beyond that we'd need pagination,
// but the small/medium repo case (which is what the dashboard targets)
// is fully covered without per-issue API calls.
func HandleRepoRefreshIssues(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req repoRefreshIssuesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid JSON"})
		return
	}
	if req.Repo == "" {
		writeJSON(w, 400, map[string]string{"error": "repo is required"})
		return
	}

	client, err := clientForRepo(req.Repo)
	if err != nil {
		writeErr(w, err)
		return
	}

	issues, err := client.ListIssues(req.Repo, "all", nil)
	if err != nil {
		writeJSON(w, 502, map[string]string{"error": "list issues: " + err.Error()})
		return
	}

	capturedAt := time.Now().UTC()

	// Resolve sub-issue parentage (GitHub native; no-op on GitLab where
	// SubIssuesTotal stays 0). Only issues that report children incur the
	// extra ListSubIssues call, mirroring the cron scan path in run.go.
	parentOf := map[int]int{} // child issue number → parent issue number
	for _, iss := range issues {
		if iss.SubIssuesTotal == 0 {
			continue
		}
		children, subErr := client.ListSubIssues(req.Repo, iss.Number)
		if subErr != nil {
			continue
		}
		for _, ch := range children {
			parentOf[ch.Number] = iss.Number
		}
	}

	fresh := make([]snapshot.IssueEntry, 0, len(issues))
	for _, iss := range issues {
		entry := snapshot.IssueEntry{
			Repo:         req.Repo,
			IssueNumber:  iss.Number,
			IssueTitle:   iss.Title,
			Labels:       append([]string(nil), iss.Labels...),
			State:        iss.State,
			CapturedAt:   capturedAt,
			CreatedAt:    iss.CreatedAt,
			ClosedAt:     iss.ClosedAt,
			SubTotal:     iss.SubIssuesTotal,
			SubCompleted: iss.SubIssuesCompleted,
		}
		if p, ok := parentOf[iss.Number]; ok {
			entry.ParentNumber = &p
		}
		fresh = append(fresh, entry)
	}

	// Read existing issues.json so we keep entries from OTHER repos
	// untouched. Missing or unreadable file is treated as empty —
	// WriteIssues will create it.
	existing := readIssuesSnapshot()
	merged := make([]snapshot.IssueEntry, 0, len(existing)+len(fresh))
	for _, e := range existing {
		if e.Repo == req.Repo {
			continue
		}
		merged = append(merged, e)
	}
	merged = append(merged, fresh...)

	if err := snapshot.WriteIssues(merged); err != nil {
		writeErr(w, err)
		return
	}

	writeJSON(w, 200, repoRefreshIssuesResponse{Status: "ok", Count: len(fresh)})
}

func readIssuesSnapshot() []snapshot.IssueEntry {
	data, err := os.ReadFile(filepath.Join(snapshot.DataDir(), "issues.json"))
	if err != nil {
		return nil
	}
	var entries []snapshot.IssueEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil
	}
	return entries
}
