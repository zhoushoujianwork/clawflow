// Package preflight records bounded failures before work is dispatched.
// Callers must verify execution ownership before settling an interrupted attempt.
package preflight

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

const MaxFailures = 5
const Cooldown = 5 * time.Minute
const MaxBackoff = time.Hour

var ErrPending = errors.New("preflight execution requires reconciliation")
var ErrExhausted = errors.New("preflight infrastructure budget exhausted")
var ErrCooldown = errors.New("preflight infrastructure cooldown")

// Event retains previous rounds; neither source changes nor comments erase it.
type Event struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Stage   string    `json:"stage,omitempty"`
	At      time.Time `json:"at"`
	EndedAt time.Time `json:"ended_at,omitempty"`
	Outcome string    `json:"outcome,omitempty"`
	Actor   string    `json:"actor,omitempty"`
	Reason  string    `json:"reason,omitempty"`
}

type Ledger struct {
	Version int     `json:"version"`
	Repo    string  `json:"repo"`
	Issue   int     `json:"issue"`
	Events  []Event `json:"events"`
}

type Status struct {
	Failures  int
	NotBefore time.Time
	PendingID string
}

// Status validates the entire history before deriving permission to retry.
func (l Ledger) Status() (Status, error) {
	var s Status
	if l.Version != 1 || l.Repo == "" || l.Issue <= 0 {
		return s, errors.New("invalid preflight ledger identity/version")
	}
	seen := map[string]bool{}
	for _, e := range l.Events {
		if e.ID == "" || seen[e.ID] || e.At.IsZero() || s.PendingID != "" {
			return s, errors.New("invalid preflight event history")
		}
		seen[e.ID] = true
		switch e.Kind {
		case "resume":
			if strings.TrimSpace(e.Actor) == "" || strings.TrimSpace(e.Reason) == "" || e.Outcome != "" || !e.EndedAt.IsZero() {
				return s, errors.New("invalid preflight resume event")
			}
			s = Status{}
		case "attempt":
			if e.Stage == "" {
				return s, errors.New("missing preflight stage")
			}
			if e.Outcome == "" {
				if !e.EndedAt.IsZero() {
					return s, errors.New("unfinished preflight has end time")
				}
				s.PendingID = e.ID
				continue
			}
			if e.EndedAt.IsZero() || e.EndedAt.Before(e.At) {
				return s, errors.New("invalid preflight end time")
			}
			switch e.Outcome {
			case "success":
				s = Status{}
			case "waiting": // Normal dependency/ownership waiting does not reset failures.
			case "failure":
				if e.Reason == "" {
					return s, errors.New("missing preflight failure classification")
				}
				s.Failures++
				delay := Cooldown
				for i := 1; i < s.Failures && delay < MaxBackoff; i++ {
					delay *= 2
				}
				if delay > MaxBackoff {
					delay = MaxBackoff
				}
				s.NotBefore = e.EndedAt.Add(delay)
			default:
				return s, errors.New("unknown preflight outcome")
			}
		default:
			return s, errors.New("unknown preflight event")
		}
	}
	return s, nil
}

type Store struct {
	Root  string
	Repo  string
	Issue int
}

func (s Store) Path() string {
	h := sha256.Sum256([]byte(strings.ToLower(s.Repo)))
	return filepath.Join(s.Root, "preflight", hex.EncodeToString(h[:]), fmt.Sprintf("%d.json", s.Issue))
}

func (s Store) Load() (Ledger, error) {
	if s.Root == "" {
		return Ledger{}, errors.New("preflight storage root is required")
	}
	l := Ledger{Version: 1, Repo: strings.ToLower(s.Repo), Issue: s.Issue}
	b, err := os.ReadFile(s.Path())
	if err == nil {
		l = Ledger{}
		err = json.Unmarshal(b, &l)
	} else if os.IsNotExist(err) {
		err = nil
	}
	if err != nil {
		return l, err
	}
	if l.Repo != strings.ToLower(s.Repo) || l.Issue != s.Issue {
		return l, errors.New("preflight ledger identity mismatch")
	}
	_, err = l.Status()
	return l, err
}

func newID() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}

func (s Store) update(fn func(*Ledger) error) error {
	if s.Root == "" {
		return errors.New("preflight storage root is required")
	}
	dir := filepath.Dir(s.Path())
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := filelock.Try(s.Path()+".lock", map[string]int{"PID": os.Getpid()})
	if err != nil {
		return err
	}
	defer filelock.Release(lock)
	l, err := s.Load()
	if err != nil {
		return err
	}
	if err = fn(&l); err != nil {
		return err
	}
	if _, err = l.Status(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".preflight-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), s.Path()); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Begin persists before any external call. An unresolved attempt always blocks,
// even when its original process no longer holds the ledger lock.
func (s Store) Begin(stage string, now time.Time) (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	err = s.update(func(l *Ledger) error {
		st, err := l.Status()
		if err != nil {
			return err
		}
		if st.PendingID != "" {
			return ErrPending
		}
		if st.Failures >= MaxFailures {
			return ErrExhausted
		}
		if now.Before(st.NotBefore) {
			return fmt.Errorf("%w until %s", ErrCooldown, st.NotBefore.Format(time.RFC3339))
		}
		l.Events = append(l.Events, Event{ID: id, Kind: "attempt", Stage: stage, At: now.UTC()})
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Finish is idempotent for the identical outcome. A caller may report success
// only after the complete preflight, including plan and fresh evidence checks.
// reason must be a safe classification, never raw model or request content.
func (s Store) Finish(id, outcome, reason string, now time.Time) error {
	return s.update(func(l *Ledger) error {
		if len(l.Events) == 0 {
			return errors.New("preflight attempt absent")
		}
		e := &l.Events[len(l.Events)-1]
		if e.ID != id || e.Kind != "attempt" {
			return errors.New("preflight attempt ownership mismatch")
		}
		if e.Outcome != "" {
			if e.Outcome == outcome && e.Reason == reason {
				return nil
			}
			return errors.New("preflight outcome already settled")
		}
		e.Outcome = outcome
		e.Reason = reason
		e.EndedAt = now.UTC()
		return nil
	})
}

// Resume requires a deliberate audited action and refuses pending execution.
// It never edits the separate development/CI repair ledger.
func (s Store) Resume(actor, reason string, now time.Time) error {
	return s.update(func(l *Ledger) error {
		st, err := l.Status()
		if err != nil {
			return err
		}
		if st.PendingID != "" {
			return ErrPending
		}
		if st.Failures < MaxFailures {
			return errors.New("preflight budget is not exhausted")
		}
		id, err := newID()
		if err != nil {
			return err
		}
		l.Events = append(l.Events, Event{ID: id, Kind: "resume", At: now.UTC(), Actor: actor, Reason: reason})
		return nil
	})
}
