package repairledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// MaxCancelReruns bounds automatic reruns of one cancelled check on one head.
// Cancelled CI is not a code failure, so this never touches the repair budget.
const MaxCancelReruns = 2

type cancelRerunState struct {
	Head   string         `json:"head"`
	Counts map[string]int `json:"counts"`
}

func cancelRerunPath(root, repo string, pr int) (string, string) {
	key := sha256.Sum256([]byte(strings.ToLower(repo)))
	dir := filepath.Join(root, "ci-cancel-rerun", hex.EncodeToString(key[:]))
	return dir, filepath.Join(dir, fmt.Sprintf("pr-%d.json", pr))
}

func loadCancelRerun(path, head string) (cancelRerunState, error) {
	st := cancelRerunState{Head: head, Counts: map[string]int{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	var disk cancelRerunState
	if err := json.Unmarshal(data, &disk); err != nil {
		return st, fmt.Errorf("invalid cancel rerun ledger: %w", err)
	}
	if disk.Head == head && disk.Counts != nil {
		st = disk
	}
	return st, nil
}

// ReserveCancelReruns grants one rerun per key that has fewer than
// MaxCancelReruns on this head. Counts reset when the head changes. The repair
// ledger is not read or written.
func ReserveCancelReruns(root, repo string, pr vcs.PR, keys []string) (granted []bool, err error) {
	unlock, err := Lock(root, repo, pr.Number)
	if err != nil {
		return nil, err
	}
	defer unlock()
	dir, path := cancelRerunPath(root, repo, pr.Number)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	st, err := loadCancelRerun(path, pr.HeadSHA)
	if err != nil {
		return nil, err
	}
	granted = make([]bool, len(keys))
	changed := false
	for i, k := range keys {
		if st.Counts[k] < MaxCancelReruns {
			st.Counts[k]++
			granted[i] = true
			changed = true
		}
	}
	if !changed {
		return granted, nil
	}
	data, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return nil, err
	}
	return granted, os.Rename(tmp, path)
}

// CancelRerunsExhausted reports whether any cancelled check on this head has
// used all automatic reruns. Read-only.
func CancelRerunsExhausted(root, repo string, pr vcs.PR) bool {
	_, path := cancelRerunPath(root, repo, pr.Number)
	st, err := loadCancelRerun(path, pr.HeadSHA)
	if err != nil {
		return false
	}
	for _, n := range st.Counts {
		if n >= MaxCancelReruns {
			return true
		}
	}
	return false
}
