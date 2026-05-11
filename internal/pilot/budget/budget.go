// Package budget enforces a per-wake hard cap on VCS write operations
// performed by Pilot. The cap is implemented in code (not prompt): when
// CLAWFLOW_PILOT_BUDGET_PATH points at a budget state file, every
// budget-aware `clawflow` subcommand calls Reserve before performing
// its VCS write. Reserve takes a cross-process exclusive flock,
// reads/increments/persists the counter, and returns an error when the
// cap is reached — at which point the calling subcommand exits non-zero
// and the Pilot's claude subprocess sees a real failure (not advice).
//
// The env-var gate means non-Pilot CLI invocations (operator runs,
// humans typing `clawflow issue create` by hand) skip the budget check
// entirely — Reserve is a no-op when CLAWFLOW_PILOT_BUDGET_PATH is unset.
package budget

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

// EnvPath is the env var Pilot sets to the per-wake budget file path.
// All budget-aware code paths gate on its presence: unset = no enforcement.
const EnvPath = "CLAWFLOW_PILOT_BUDGET_PATH"

// DefaultMax is the per-wake hard cap on VCS write operations.
// Chosen low (5) for the pilot phase — easy to raise later if too tight
// in practice; impossible to undo a runaway if set too high.
const DefaultMax = 5

// State is the on-disk shape of the budget file.
type State struct {
	Max  int  `json:"max"`
	Used int  `json:"used"`
	Ops  []Op `json:"ops,omitempty"`
}

// Op records one consumed slot. Stored for post-mortem visibility:
// the wake's meta.json + Ops list together tell us exactly what Pilot
// did in a given run, including any rejected attempts.
type Op struct {
	Name string `json:"name"`
	At   string `json:"at"`
}

// Init creates an empty budget file at path with the given cap.
// max <= 0 falls back to DefaultMax. Overwrites any existing file at path.
func Init(path string, max int) error {
	if max <= 0 {
		max = DefaultMax
	}
	s := State{Max: max}
	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode budget: %w", err)
	}
	return os.WriteFile(path, out, 0o644)
}

// Read returns a snapshot of the budget state at path without locking.
// Intended for post-wake reporting — concurrent Reserve calls may race
// with this read, but that's acceptable since the caller is the parent
// process reading after claude has exited.
func Read(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return State{}, fmt.Errorf("parse budget %q: %w", path, err)
	}
	return s, nil
}

// Reserve claims one operation slot from the budget file at the path in
// CLAWFLOW_PILOT_BUDGET_PATH. When the env var is unset, Reserve is a
// no-op (this process is not a Pilot wake). When set, Reserve takes an
// exclusive flock on the file, reads State, returns an error if Used>=Max,
// otherwise increments Used, appends op to Ops, persists, and releases.
//
// The error path is the load-bearing safety guarantee: callers MUST
// propagate the returned error and refuse to perform the write.
func Reserve(op string) error {
	path := os.Getenv(EnvPath)
	if path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open pilot budget %q: %w", path, err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock pilot budget: %w", err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("read pilot budget: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("parse pilot budget: %w", err)
	}
	if s.Max <= 0 {
		s.Max = DefaultMax
	}
	if s.Used >= s.Max {
		return fmt.Errorf("pilot operation budget exhausted (%d/%d) — operation %q rejected; this wake is capped to prevent runaway VCS writes", s.Used, s.Max, op)
	}
	s.Used++
	s.Ops = append(s.Ops, Op{Name: op, At: time.Now().UTC().Format(time.RFC3339)})

	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode pilot budget: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		return fmt.Errorf("seek pilot budget: %w", err)
	}
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("truncate pilot budget: %w", err)
	}
	if _, err := f.Write(out); err != nil {
		return fmt.Errorf("write pilot budget: %w", err)
	}
	return nil
}
