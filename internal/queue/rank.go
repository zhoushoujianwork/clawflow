package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// RankVersion is the rank file schema version.
const RankVersion = 1

// RankPath returns ~/.clawflow/data/queue.json.
func RankPath() string { return filepath.Join(snapshot.DataDir(), "queue.json") }

type rankFile struct {
	Version int      `json:"version"`
	Order   []string `json:"order"`
}

// LoadRank reads the manual order. A missing file is an empty order;
// unparseable entries are dropped and duplicates keep their first position.
func LoadRank() ([]Key, error) {
	data, err := os.ReadFile(RankPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f rankFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", RankPath(), err)
	}
	if f.Version != RankVersion {
		return nil, fmt.Errorf("%s: unsupported version %d", RankPath(), f.Version)
	}
	return normalize(f.Order, nil), nil
}

// Positions maps each key to its index in order.
func Positions(order []Key) map[Key]int {
	m := make(map[Key]int, len(order))
	for i, k := range order {
		if _, dup := m[k]; !dup {
			m[k] = i
		}
	}
	return m
}

// normalize parses raw entries, drops invalid and (when keep != nil) not-kept
// keys, and removes duplicates keeping the first.
func normalize(raw []string, keep func(Key) bool) []Key {
	seen := map[Key]bool{}
	out := []Key{}
	for _, s := range raw {
		k, err := ParseKey(s)
		if err != nil || seen[k] || (keep != nil && !keep(k)) {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

func keyStrings(keys []Key) []string {
	s := make([]string, len(keys))
	for i, k := range keys {
		s[i] = k.String()
	}
	return s
}

// UpdateRank applies mutate to the current order under the snapshot lock and
// writes the result atomically. After mutate, duplicates are removed (first
// wins) and, when live is non-nil, keys it rejects (unknown or closed) are
// pruned. If mutate returns an error nothing is written. The stored order
// after the update is returned.
func UpdateRank(mutate func(order []Key) ([]Key, error), live func(Key) bool) ([]Key, error) {
	var result []Key
	err := snapshot.WithSnapshotLock(func() error {
		cur, err := LoadRank()
		if err != nil {
			return err
		}
		next := cur
		if mutate != nil {
			if next, err = mutate(append([]Key(nil), cur...)); err != nil {
				return err
			}
		}
		result = normalize(keyStrings(next), live)
		return writeRank(result)
	})
	return result, err
}

func writeRank(order []Key) error {
	path := RankPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rankFile{Version: RankVersion, Order: keyStrings(order)}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-queue-")
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

// Placement describes where Move puts a key.
type Placement struct {
	Top, Bottom   bool
	Before, After *Key
}

// Move returns order with k placed per p. k is added if absent; a Before/After
// anchor that is not ranked is appended first so the relative position is
// well defined.
func Move(order []Key, k Key, p Placement) ([]Key, error) {
	n := 0
	if p.Top {
		n++
	}
	if p.Bottom {
		n++
	}
	if p.Before != nil {
		n++
	}
	if p.After != nil {
		n++
	}
	if n != 1 {
		return nil, errors.New("exactly one of top, bottom, before, after is required")
	}
	rest := make([]Key, 0, len(order)+1)
	for _, o := range order {
		if o != k {
			rest = append(rest, o)
		}
	}
	insertAt := func(i int) []Key {
		out := make([]Key, 0, len(rest)+1)
		out = append(out, rest[:i]...)
		out = append(out, k)
		return append(out, rest[i:]...)
	}
	switch {
	case p.Top:
		return insertAt(0), nil
	case p.Bottom:
		return insertAt(len(rest)), nil
	}
	anchor := p.Before
	if p.After != nil {
		anchor = p.After
	}
	if *anchor == k {
		return nil, errors.New("cannot place an item relative to itself")
	}
	idx := -1
	for i, o := range rest {
		if o == *anchor {
			idx = i
			break
		}
	}
	if idx < 0 {
		rest = append(rest, *anchor)
		idx = len(rest) - 1
	}
	if p.After != nil {
		idx++
	}
	return insertAt(idx), nil
}
