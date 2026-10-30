package repairledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// Limits for the CI repair ledger. Only attempts that reached real repair work
// consume the repair budget; infrastructure outcomes
// are released and counted against a separate bounded budget with backoff.
const (
	MaxAttempts = 3
	MaxInfra    = 5
	Cooldown    = 5 * time.Minute
	MaxBackoff  = time.Hour
)

type Attempt struct {
	TaskID    string    `json:"task_id,omitempty"`
	Completed bool      `json:"completed,omitempty"`
	Head      string    `json:"head"`
	Attempt   int       `json:"attempt"`
	At        time.Time `json:"at"`
	// Released is the infrastructure kind that made this reservation
	// non-consuming. Empty means the attempt counts against the repair budget.
	Released string `json:"released,omitempty"`
	// Reason is ci, acceptance, merge_conflict, or rerun_ci. All share one ledger per PR.
	Reason string `json:"reason,omitempty"`
}
type Ledger struct {
	Attempts []Attempt `json:"attempts"`
	// NotBefore is the infrastructure backoff deadline for the next reservation.
	NotBefore time.Time `json:"not_before,omitempty"`
}

// Counts returns (consumed repair attempts, released infrastructure outcomes).
func (l Ledger) Counts() (consumed, infra int) {
	for _, a := range l.Attempts {
		if a.Released == "" {
			consumed++
		} else if a.Released != "no-change" && a.Released != "resource-busy" {
			infra++
		}
	}
	return
}

func Path(root, repo string, pr int) (dir, path string) {
	key := sha256.Sum256([]byte(strings.ToLower(repo)))
	dir = filepath.Join(root, "ci-repair", hex.EncodeToString(key[:]))
	return dir, filepath.Join(dir, fmt.Sprintf("pr-%d.json", pr))
}

func Load(path string) (Ledger, error) {
	var ledger Ledger
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ledger, nil
	}
	if err != nil {
		return ledger, err
	}
	if err := json.Unmarshal(data, &ledger); err != nil {
		return ledger, fmt.Errorf("invalid repair ledger: %w", err)
	}
	return ledger, nil
}

func Save(dir, path string, ledger Ledger) error {
	data, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".reserve-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	// Persist the directory entry before allowing provider startup.
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Budget refusals. They wrap a sentinel so callers can tell a spent budget
// from a transient wait.
var (
	ErrExhausted = errors.New("repair exhausted")
	ErrBlocked   = errors.New("repair blocked")
	ErrWait      = errors.New("repair waiting")
)

// Budget reports whether the ledger allows another reservation at now.
func Budget(ledger Ledger, now time.Time) error {
	consumed, infra := ledger.Counts()
	if consumed >= MaxAttempts {
		return fmt.Errorf("%w: %d total attempts", ErrExhausted, MaxAttempts)
	}
	if infra >= MaxInfra {
		last := ledger.Attempts[len(ledger.Attempts)-1]
		return fmt.Errorf("%w: infrastructure budget exhausted (%d outcomes, last: %s)", ErrBlocked, infra, last.Released)
	}
	if n := len(ledger.Attempts); n > 0 && now.Sub(ledger.Attempts[n-1].At) < Cooldown {
		return fmt.Errorf("%w: cooldown 5 minutes", ErrWait)
	}
	if now.Before(ledger.NotBefore) {
		return fmt.Errorf("%w: infrastructure backoff until %s", ErrWait, ledger.NotBefore.Format(time.RFC3339))
	}
	return nil
}

// CheckBudget is the read-only form of the reservation check. It never
// creates the ledger.
func CheckBudget(root, repo string, pr int, now time.Time) error {
	_, path := Path(root, repo, pr)
	ledger, err := Load(path)
	if err != nil {
		return err
	}
	return Budget(ledger, now)
}

// Reserve takes one attempt from the PR's shared ledger. CI and
// acceptance and conflict repairs draw on the same budget, cooldown and backoff.
func Reserve(root, repo string, pr vcs.PR, reason string, now time.Time) (int, error) {
	unlock, err := Lock(root, repo, pr.Number)
	if err != nil {
		return 0, err
	}
	defer unlock()
	dir, path := Path(root, repo, pr.Number)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return 0, err
	}
	ledger, err := Load(path)
	if err != nil {
		return 0, err
	}
	if err := Budget(ledger, now); err != nil {
		return 0, err
	}
	consumed, _ := ledger.Counts()
	attempt := consumed + 1
	ledger.Attempts = append(ledger.Attempts, Attempt{Head: pr.HeadSHA, Attempt: attempt, At: now, Reason: reason})
	if err := Save(dir, path, ledger); err != nil {
		return 0, err
	}
	return attempt, nil
}

// Release marks the open reservation for `attempt` as non-consuming
// after an infrastructure or confirmed no-change outcome and applies backoff.
// Only infrastructure releases count toward the bounded infrastructure budget.
func Release(root, repo string, pr vcs.PR, attempt int, kind string, now time.Time) error {
	unlock, err := Lock(root, repo, pr.Number)
	if err != nil {
		return err
	}
	defer unlock()
	dir, path := Path(root, repo, pr.Number)
	ledger, err := Load(path)
	if err != nil {
		return err
	}
	idx := -1
	for i := len(ledger.Attempts) - 1; i >= 0; i-- {
		a := ledger.Attempts[i]
		if a.Released == "" && a.Attempt == attempt && a.Head == pr.HeadSHA {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no open repair reservation for attempt %d", attempt)
	}
	ledger.Attempts[idx].Released = kind
	_, infra := ledger.Counts()
	backoff := Cooldown << uint(infra)
	if backoff > MaxBackoff || backoff <= 0 {
		backoff = MaxBackoff
	}
	ledger.NotBefore = now.Add(backoff)
	return Save(dir, path, ledger)
}

// Lock serializes ledger mutations across runner and pilot processes.
func Lock(root, repo string, pr int) (func(), error) {
	dir, path := Path(root, repo, pr)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// ReserveRerun durably claims the head before any remote effects. An uncertain
// request is not replayed, even if the process dies or a later POST fails.
func ReserveRerun(root, repo string, pr vcs.PR, now time.Time) error {
	unlock, err := Lock(root, repo, pr.Number)
	if err != nil {
		return err
	}
	defer unlock()
	dir, path := Path(root, repo, pr.Number)
	ledger, err := Load(path)
	if err != nil {
		return err
	}
	for _, a := range ledger.Attempts {
		if a.Head == pr.HeadSHA && a.Reason == "rerun_ci" {
			return errors.New("already_rerun_for_head")
		}
	}
	// Reruns consume only infrastructure budget, even after code repairs run out.
	_, infra := ledger.Counts()
	if infra >= MaxInfra {
		return fmt.Errorf("%w: infrastructure budget exhausted", ErrBlocked)
	}
	if now.Before(ledger.NotBefore) {
		return ErrWait
	}
	if n := len(ledger.Attempts); n > 0 && now.Sub(ledger.Attempts[n-1].At) < Cooldown {
		return ErrWait
	}
	ledger.Attempts = append(ledger.Attempts, Attempt{Head: pr.HeadSHA, At: now, Reason: "rerun_ci", Released: "rerun_ci"})
	backoff := Cooldown << uint(infra+1)
	if backoff > MaxBackoff {
		backoff = MaxBackoff
	}
	ledger.NotBefore = now.Add(backoff)
	return Save(dir, path, ledger)
}

// ApplyResult reconciles coordinator results, including legacy tasks without a
// reservation. Task identity makes replay safe even when attempt numbers repeat
// after an infrastructure release. Historical results must not recheck cooldown.
func ApplyResult(root, repo string, pr vcs.PR, attempt int, taskID, reason, kind string, now time.Time) error {
	if attempt < 1 || taskID == "" {
		return errors.New("invalid repair result identity")
	}
	unlock, err := Lock(root, repo, pr.Number)
	if err != nil {
		return err
	}
	defer unlock()
	dir, path := Path(root, repo, pr.Number)
	ledger, err := Load(path)
	if err != nil {
		return err
	}
	idx := -1
	for i := len(ledger.Attempts) - 1; i >= 0; i-- {
		a := ledger.Attempts[i]
		if a.TaskID == taskID {
			return nil
		}
		if idx < 0 && a.TaskID == "" && !a.Completed && a.Head == pr.HeadSHA && a.Attempt == attempt {
			idx = i
		}
	}
	if idx < 0 {
		ledger.Attempts = append(ledger.Attempts, Attempt{Head: pr.HeadSHA, Attempt: attempt, At: now, Reason: reason})
		idx = len(ledger.Attempts) - 1
	}
	a := &ledger.Attempts[idx]
	a.TaskID, a.Completed = taskID, true
	if a.Released == "" && kind != "" {
		a.Released = kind
		_, infra := ledger.Counts()
		backoff := Cooldown << uint(infra)
		if backoff > MaxBackoff || backoff <= 0 {
			backoff = MaxBackoff
		}
		ledger.NotBefore = now.Add(backoff)
	}
	return Save(dir, path, ledger)
}
