// Package snapshot writes JSON snapshots of ClawFlow state to
// ~/.clawflow/dashboard/data/ so the local web dashboard can render
// "what did ClawFlow do" without a backend server. Each operator run
// gets its own directory containing an append-only events.jsonl
// (raw claude stream-json events) plus a meta.json with
// start/end/status/pr_url.
//
// The contract is deliberately file-based: CLI writes, dashboard reads.
// If a user hates the bundled dashboard they can `cd` into the data
// directory, `jq` the JSON themselves, or point any static file server
// at it.
package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/operator"
)

// DashboardRoot is ~/.clawflow/dashboard. The SPA assets live at the root;
// data lives under DashboardRoot/data.
func DashboardRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "dashboard")
}

// DataDir is the JSON snapshot root. Dashboard fetches are relative to this.
func DataDir() string {
	return filepath.Join(DashboardRoot(), "data")
}

// RunDir is the directory for a single operator run. Callers mkdir'p before
// writing into it.
func RunDir(repo string, issueNum int, startedAt time.Time) string {
	return filepath.Join(
		DataDir(),
		"runs",
		strings.ReplaceAll(repo, "/", "__"),
		fmt.Sprintf("issue-%d", issueNum),
		startedAt.UTC().Format("2006-01-02T15-04-05Z"),
	)
}

// RepoView is the dashboard-facing view of one monitored repo. Credentials
// are deliberately NOT included — the dashboard is local but still renders
// in a browser and we don't want tokens in the DOM.
type RepoView struct {
	FullName   string `json:"full_name"`
	Platform   string `json:"platform"`
	BaseURL    string `json:"base_url,omitempty"`
	BaseBranch string `json:"base_branch"`
	LocalPath  string `json:"local_path,omitempty"`
	Enabled    bool   `json:"enabled"`
	AutoFix    bool   `json:"auto_fix"`
	AutoMerge  bool   `json:"auto_merge"`
}

// OperatorView is the dashboard-facing view of one loaded operator.
type OperatorView struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Target         string   `json:"target"`
	LabelsRequired []string `json:"labels_required"`
	LabelsExcluded []string `json:"labels_excluded"`
	LockLabel      string   `json:"lock_label"`
	Source         string   `json:"source"`
}

// Meta is the top-level snapshot metadata; the dashboard reads it on load
// to show "last updated" + version.
type Meta struct {
	ClawFlowVersion string    `json:"clawflow_version"`
	LastRefresh     time.Time `json:"last_refresh"`
}

// RunMeta describes one operator run. events.jsonl lives alongside it.
type RunMeta struct {
	Operator    string    `json:"operator"`
	Repo        string    `json:"repo"`
	IssueNumber int       `json:"issue_number"`
	IssueTitle  string    `json:"issue_title,omitempty"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at,omitempty"`
	// Status is one of "running", "success", "failed", "skipped".
	Status  string `json:"status"`
	PRUrl   string `json:"pr_url,omitempty"`
	Error   string `json:"error,omitempty"`
	Summary string `json:"summary,omitempty"` // operator's final text output
}

// WriteRepos writes data/repos.json.
func WriteRepos(cfg *config.Config) error {
	views := make([]RepoView, 0, len(cfg.Repos))
	for name, r := range cfg.Repos {
		views = append(views, RepoView{
			FullName:   name,
			Platform:   r.Platform,
			BaseURL:    r.BaseURL,
			BaseBranch: r.BaseBranch,
			LocalPath:  r.LocalPath,
			Enabled:    r.Enabled,
			AutoFix:    r.AutoFix,
			AutoMerge:  r.AutoMerge,
		})
	}
	return writeJSON(filepath.Join(DataDir(), "repos.json"), views)
}

// WriteOperators writes data/operators.json.
func WriteOperators(reg *operator.Registry) error {
	ops := reg.All()
	views := make([]OperatorView, 0, len(ops))
	for _, op := range ops {
		views = append(views, OperatorView{
			Name:           op.Name,
			Description:    op.Description,
			Target:         op.Trigger.Target,
			LabelsRequired: op.Trigger.LabelsRequired,
			LabelsExcluded: op.Trigger.LabelsExcluded,
			LockLabel:      op.LockLabel,
			Source:         op.Source,
		})
	}
	return writeJSON(filepath.Join(DataDir(), "operators.json"), views)
}

// WriteMeta writes data/meta.json.
func WriteMeta(version string) error {
	return writeJSON(filepath.Join(DataDir(), "meta.json"), Meta{
		ClawFlowVersion: version,
		LastRefresh:     time.Now().UTC(),
	})
}

// WriteRunMeta writes meta.json inside an already-created run directory.
func WriteRunMeta(runDir string, m RunMeta) error {
	return writeJSON(filepath.Join(runDir, "meta.json"), m)
}

// RunIndexEntry is a flattened row for the dashboard's "recent runs" list.
// Path is a dashboard-relative URL so fetches work directly against the
// static file server (e.g. "./data/runs/foo__bar/issue-7/2026-04-24T.../").
type RunIndexEntry struct {
	RunMeta
	Path string `json:"path"`
}

// PendingEntry is one (issue, operator) pair that matched the operator's
// trigger rules but had not yet been processed when the snapshot was taken.
// One issue can produce multiple entries if it matches multiple operators;
// the dashboard renders each as its own row so the user sees every queued
// action.
type PendingEntry struct {
	Repo        string    `json:"repo"`
	IssueNumber int       `json:"issue_number"`
	IssueTitle  string    `json:"issue_title,omitempty"`
	Operator    string    `json:"operator"`
	Labels      []string  `json:"labels,omitempty"`
	CapturedAt  time.Time `json:"captured_at"`
}

// WritePending writes data/pending.json with the supplied entries. The list
// is replaced wholesale on every refresh so stale entries (issues that just
// got processed) drop off automatically.
func WritePending(entries []PendingEntry) error {
	if entries == nil {
		// JSON [] over null so the dashboard skips a nullability check.
		entries = []PendingEntry{}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Repo != entries[j].Repo {
			return entries[i].Repo < entries[j].Repo
		}
		if entries[i].IssueNumber != entries[j].IssueNumber {
			return entries[i].IssueNumber < entries[j].IssueNumber
		}
		return entries[i].Operator < entries[j].Operator
	})
	return writeJSON(filepath.Join(DataDir(), "pending.json"), entries)
}

// WriteRunsIndex walks data/runs/* and writes data/runs.json containing the
// most recent `limit` runs sorted by StartedAt desc. Used by the dashboard
// to render its home page without having to crawl the filesystem over
// HTTP.
func WriteRunsIndex(limit int) error {
	runsRoot := filepath.Join(DataDir(), "runs")
	entries := collectRunEntries(runsRoot)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].StartedAt.After(entries[j].StartedAt)
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return writeJSON(filepath.Join(DataDir(), "runs.json"), entries)
}

// collectRunEntries walks data/runs/* and reads every meta.json it finds.
// Malformed files are skipped silently — we'd rather render an incomplete
// list than fail the entire index write.
func collectRunEntries(root string) []RunIndexEntry {
	// Non-nil empty slice so JSON renders as [] instead of null when there
	// are no runs yet — saves the dashboard a nullability check.
	out := []RunIndexEntry{}
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "meta.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		var m RunMeta
		if err := json.Unmarshal(data, &m); err != nil {
			return nil
		}
		relDir := strings.TrimPrefix(filepath.Dir(path), DataDir())
		out = append(out, RunIndexEntry{
			RunMeta: m,
			Path:    "./data" + filepath.ToSlash(relDir) + "/",
		})
		return nil
	})
	return out
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	return os.WriteFile(path, data, 0o644)
}
