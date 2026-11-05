// Package prfeedback records which human PR feedback items were already
// handed to a development session. Its attempt cap is independent from the
// CI/acceptance/conflict repair budget in package repairledger.
package prfeedback

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/repairledger"
)

const (
	// MaxAttempts bounds feedback-driven sessions per PR.
	MaxAttempts = 3
	// MaxReleased bounds attempts released after infrastructure failures.
	MaxReleased = 5
)

var (
	ErrExhausted  = errors.New("pr feedback attempts exhausted")
	ErrNothingNew = errors.New("no new pr feedback")
)

type Attempt struct {
	Head     string    `json:"head"`
	At       time.Time `json:"at"`
	IDs      []string  `json:"ids"`
	Released bool      `json:"released,omitempty"`
}

type Ledger struct {
	Handled         []string  `json:"handled"`
	Attempts        []Attempt `json:"attempts"`
	BlockedNotified bool      `json:"blocked_notified,omitempty"`
}

func (l Ledger) IsHandled(id string) bool {
	for _, h := range l.Handled {
		if h == id {
			return true
		}
	}
	return false
}

// Counts returns consumed and released attempts.
func (l Ledger) Counts() (consumed, released int) {
	for _, a := range l.Attempts {
		if a.Released {
			released++
		} else {
			consumed++
		}
	}
	return
}

func (l Ledger) Exhausted() bool {
	c, r := l.Counts()
	return c >= MaxAttempts || r >= MaxReleased
}

func Path(root, repo string, pr int) (dir, path string) {
	key := sha256.Sum256([]byte(strings.ToLower(repo)))
	dir = filepath.Join(root, "pr-feedback", hex.EncodeToString(key[:]))
	return dir, filepath.Join(dir, fmt.Sprintf("pr-%d.json", pr))
}

func Load(root, repo string, pr int) (Ledger, error) {
	var l Ledger
	_, path := Path(root, repo, pr)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	if err := json.Unmarshal(data, &l); err != nil {
		return l, fmt.Errorf("invalid pr feedback ledger: %w", err)
	}
	return l, nil
}

func save(root, repo string, pr int, l Ledger) error {
	dir, path := Path(root, repo, pr)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(l)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".fb-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Reserve takes one attempt and marks ids handled so repeated scans do not
// trigger the same feedback twice. Returns the 1-based attempt number.
func Reserve(root, repo string, pr int, head string, ids []string, now time.Time) (int, error) {
	unlock, err := repairledger.Lock(root, repo, pr)
	if err != nil {
		return 0, err
	}
	defer unlock()
	l, err := Load(root, repo, pr)
	if err != nil {
		return 0, err
	}
	var fresh []string
	for _, id := range ids {
		if !l.IsHandled(id) {
			fresh = append(fresh, id)
		}
	}
	if len(fresh) == 0 {
		return 0, ErrNothingNew
	}
	if l.Exhausted() {
		return 0, ErrExhausted
	}
	l.Handled = append(l.Handled, fresh...)
	l.Attempts = append(l.Attempts, Attempt{Head: head, At: now, IDs: fresh})
	if err := save(root, repo, pr, l); err != nil {
		return 0, err
	}
	consumed, _ := l.Counts()
	return consumed, nil
}

// Release returns ids to the pending set after an infrastructure failure.
func Release(root, repo string, pr int, ids []string) error {
	unlock, err := repairledger.Lock(root, repo, pr)
	if err != nil {
		return err
	}
	defer unlock()
	l, err := Load(root, repo, pr)
	if err != nil {
		return err
	}
	drop := map[string]bool{}
	for _, id := range ids {
		drop[id] = true
	}
	kept := l.Handled[:0]
	for _, h := range l.Handled {
		if !drop[h] {
			kept = append(kept, h)
		}
	}
	l.Handled = kept
	for i := len(l.Attempts) - 1; i >= 0; i-- {
		if !l.Attempts[i].Released && len(l.Attempts[i].IDs) > 0 && drop[l.Attempts[i].IDs[0]] {
			l.Attempts[i].Released = true
			break
		}
	}
	return save(root, repo, pr, l)
}

// MarkBlockedNotified records that the exhaustion notice was posted.
func MarkBlockedNotified(root, repo string, pr int) error {
	unlock, err := repairledger.Lock(root, repo, pr)
	if err != nil {
		return err
	}
	defer unlock()
	l, err := Load(root, repo, pr)
	if err != nil {
		return err
	}
	l.BlockedNotified = true
	return save(root, repo, pr, l)
}
