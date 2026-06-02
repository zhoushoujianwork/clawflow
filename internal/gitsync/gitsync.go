// Package gitsync computes and caches the git sync status (ahead/behind/dirty)
// of each configured repo's base branch relative to its remote, and drives the
// one-button pull/push actions exposed by the dashboard.
//
// The cache lives at ~/.clawflow/data/git-status.json (next to the other
// dashboard snapshots) and is indexed by repo full name. The dashboard reads
// the cache for instant render (Hook), a background tick in `clawflow web`
// refreshes it at low frequency (fetch + recompute), and explicit refresh /
// pull / push requests update the relevant entry in place.
package gitsync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/branch"
	"github.com/zhoushoujianwork/clawflow/internal/clone"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// Status is the cached sync state for one repo's base branch. It is the shape
// the dashboard consumes from /data/git-status.json and the git-status API.
type Status struct {
	Repo        string    `json:"repo"`
	Branch      string    `json:"branch"`
	Ahead       int       `json:"ahead"`
	Behind      int       `json:"behind"`
	Dirty       bool      `json:"dirty"`
	HasUpstream bool      `json:"has_upstream"`
	HasClone    bool      `json:"has_clone"`
	Current     string    `json:"current,omitempty"`
	LastFetch   time.Time `json:"last_fetch"`
	Error       string    `json:"error,omitempty"`
}

// cacheMu serializes read-modify-write of the on-disk cache so concurrent
// refresh / pull / push handlers don't clobber each other's entries.
var cacheMu sync.Mutex

// CacheFile is the path to the on-disk status cache.
func CacheFile() string {
	return filepath.Join(snapshot.DataDir(), "git-status.json")
}

// ReadCache loads all cached entries. A missing or unreadable file yields an
// empty slice (not an error) so first-run and corrupt-file cases render empty
// rather than failing the dashboard.
func ReadCache() []Status {
	data, err := os.ReadFile(CacheFile())
	if err != nil {
		return nil
	}
	var entries []Status
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil
	}
	return entries
}

func writeCache(entries []Status) error {
	path := CacheFile()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-gitstatus-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// upsert merges one entry into the cache by repo full name, preserving the
// entries of all other repos.
func upsert(s Status) error {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	entries := ReadCache()
	replaced := false
	for i := range entries {
		if entries[i].Repo == s.Repo {
			entries[i] = s
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, s)
	}
	return writeCache(entries)
}

// LocalPath resolves the on-disk clone path for a repo without cloning. It
// prefers the configured LocalPath (when it exists on disk) and otherwise
// looks for an auto-clone at the conventional location. Returns "" when no
// local clone is present.
func LocalPath(cfg *config.Config, repo string) string {
	repoCfg, ok := cfg.Repos[repo]
	if !ok {
		return ""
	}
	if repoCfg.LocalPath != "" {
		expanded := clone.ExpandHome(repoCfg.LocalPath)
		if _, err := os.Stat(expanded); err == nil {
			return expanded
		}
	}
	return clone.DetectLocalClone(cfg, repo, repoCfg)
}

func baseBranch(cfg *config.Config, repo string) string {
	if r, ok := cfg.Repos[repo]; ok && r.BaseBranch != "" {
		return r.BaseBranch
	}
	return "main"
}

// Refresh runs `git fetch` then recomputes the sync status for one repo and
// writes it to the cache. When no local clone exists, it records a
// HasClone=false entry (so the UI can show "not cloned") rather than erroring.
// A fetch failure is non-fatal: the status is still computed against the
// existing remote-tracking refs and the fetch error is attached for display.
func Refresh(cfg *config.Config, repo string) Status {
	now := time.Now().UTC()
	base := baseBranch(cfg, repo)
	local := LocalPath(cfg, repo)
	if local == "" {
		s := Status{Repo: repo, Branch: base, HasClone: false, LastFetch: now}
		_ = upsert(s)
		return s
	}

	s := Status{Repo: repo, Branch: base, HasClone: true, LastFetch: now}
	if err := branch.Fetch(local); err != nil {
		s.Error = err.Error()
	}
	if sync, err := branch.GetSyncStatus(local, base); err != nil {
		if s.Error == "" {
			s.Error = err.Error()
		}
	} else {
		s.Ahead = sync.Ahead
		s.Behind = sync.Behind
		s.Dirty = sync.Dirty
		s.HasUpstream = sync.HasUpstream
		s.Current = sync.Current
	}
	_ = upsert(s)
	return s
}

// RefreshAll recomputes the status for every configured repo and returns the
// full set. Intended for the low-frequency background tick in `clawflow web`.
func RefreshAll(cfg *config.Config) []Status {
	out := make([]Status, 0, len(cfg.Repos))
	for repo := range cfg.Repos {
		out = append(out, Refresh(cfg, repo))
	}
	return out
}

// Pull fast-forwards the base branch from origin and refreshes the cached
// status. Returns git's combined output, the refreshed status, and any error.
func Pull(cfg *config.Config, repo string) (string, Status, error) {
	base := baseBranch(cfg, repo)
	local := LocalPath(cfg, repo)
	if local == "" {
		return "", Status{Repo: repo, Branch: base, HasClone: false}, errNoClone
	}
	out, err := branch.Pull(local, base)
	st := Refresh(cfg, repo)
	return out, st, err
}

// Push pushes the base branch to origin and refreshes the cached status.
// Returns git's combined output, the refreshed status, and any error.
func Push(cfg *config.Config, repo string) (string, Status, error) {
	base := baseBranch(cfg, repo)
	local := LocalPath(cfg, repo)
	if local == "" {
		return "", Status{Repo: repo, Branch: base, HasClone: false}, errNoClone
	}
	out, err := branch.Push(local, base)
	st := Refresh(cfg, repo)
	return out, st, err
}

// errNoClone is returned by Pull/Push when the repo has no local clone.
var errNoClone = &noCloneError{}

type noCloneError struct{}

func (*noCloneError) Error() string {
	return "该仓库尚未克隆到本地，无法执行 git 操作"
}
