package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// StateVersion is the per-repo state file schema version.
const StateVersion = 1

// EntryRecord is the last scheduling verdict for one issue, kept for
// `queue list` and the board.
type EntryRecord struct {
	Number   int    `json:"number"`
	Title    string `json:"title,omitempty"`
	Priority string `json:"priority"`
	Rank     int    `json:"rank"` // -1 = unranked
	State    string `json:"state"`
	Reason   string `json:"reason,omitempty"`
	Created  string `json:"created_at,omitempty"`
}

// RepoState is the small per-repo file under data/queue-state/. Only the pass
// holding the repo's run lock writes it.
type RepoState struct {
	Skipped      int               `json:"skipped"` // open issues carrying the owner skip label
	Version      int               `json:"version"`
	Repo         string            `json:"repo"`
	UpdatedAt    time.Time         `json:"updated_at"`
	BlockedSince map[int]time.Time `json:"blocked_since,omitempty"`
	Parked       []int             `json:"parked,omitempty"`
	Warned       map[string]bool   `json:"warned,omitempty"` // "<number>|<warning>" already reported
	Entries      []EntryRecord     `json:"entries,omitempty"`
}

// StateDir returns ~/.clawflow/data/queue-state.
func StateDir() string { return filepath.Join(snapshot.DataDir(), "queue-state") }

// StatePath returns the state file of one repo.
func StatePath(repo string) string {
	return filepath.Join(StateDir(), strings.ReplaceAll(repo, "/", "@")+".json")
}

// LoadState reads a repo's state; a missing file is an empty state.
func LoadState(repo string) (RepoState, error) {
	st := RepoState{Version: StateVersion, Repo: repo}
	data, err := os.ReadFile(StatePath(repo))
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return RepoState{Version: StateVersion, Repo: repo}, fmt.Errorf("parse %s: %w", StatePath(repo), err)
	}
	st.Version, st.Repo = StateVersion, repo
	return st, nil
}

// SaveState writes the state atomically (temp + rename).
func SaveState(st RepoState) error {
	st.Version = StateVersion
	path := StatePath(st.Repo)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-state-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// BlockedSinceKeys converts the stored map to keys of repo.
func (s RepoState) BlockedSinceKeys() map[Key]time.Time {
	m := make(map[Key]time.Time, len(s.BlockedSince))
	for n, t := range s.BlockedSince {
		m[Key{Repo: s.Repo, Number: n}] = t
	}
	return m
}

// ParkedKeys converts the stored parked list to a set of keys of repo.
func (s RepoState) ParkedKeys() map[Key]bool {
	m := make(map[Key]bool, len(s.Parked))
	for _, n := range s.Parked {
		m[Key{Repo: s.Repo, Number: n}] = true
	}
	return m
}

// Warn reports whether the warning is new for the issue and remembers it.
func (s *RepoState) Warn(number int, msg string) bool {
	if s.Warned == nil {
		s.Warned = map[string]bool{}
	}
	id := fmt.Sprintf("%d|%s", number, msg)
	if s.Warned[id] {
		return false
	}
	s.Warned[id] = true
	return true
}

// PruneWarned forgets warnings of issues that are no longer open.
func (s *RepoState) PruneWarned(open map[int]bool) {
	for id := range s.Warned {
		var n int
		if _, err := fmt.Sscanf(id, "%d|", &n); err != nil || !open[n] {
			delete(s.Warned, id)
		}
	}
}

// ListStates loads every repo state file, ignoring unreadable ones.
func ListStates() []RepoState {
	entries, err := os.ReadDir(StateDir())
	if err != nil {
		return nil
	}
	var out []RepoState
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(StateDir(), e.Name()))
		if err != nil {
			continue
		}
		var st RepoState
		if json.Unmarshal(data, &st) != nil || st.Repo == "" {
			continue
		}
		out = append(out, st)
	}
	return out
}
