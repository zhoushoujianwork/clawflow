package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
	"github.com/zhoushoujianwork/clawflow/internal/preflight"
)

type preflightOwner struct {
	Version             int
	Attempt, Repository string
	Issue               int
	Stages              map[string]string
	Checkpoints         map[string]bool
}

func writePreflightJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".preflight-evidence-")
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
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func writePreflightOwner(root, id string, s preflight.Store) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	path := filepath.Join(root, "owner.json")
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return errors.New("preflight owner path already exists or is unreadable")
	}
	return writePreflightJSON(path, preflightOwner{Version: 1, Attempt: id, Repository: strings.ToLower(s.Repo), Issue: s.Issue, Stages: map[string]string{}, Checkpoints: map[string]bool{}})
}

func recordPreflightStage(root, stage, state string) error {
	path := filepath.Join(root, "owner.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var owner preflightOwner
	if json.Unmarshal(b, &owner) != nil || owner.Version != 1 || owner.Stages == nil || owner.Checkpoints == nil {
		return errors.New("invalid preflight owner record")
	}
	if stage != "coordination" && stage != "plan" {
		return errors.New("unknown preflight stage")
	}
	if (state == "started" && owner.Stages[stage] != "") || (state == "returned" && owner.Stages[stage] != "started") || (state != "started" && state != "returned") {
		return errors.New("invalid preflight stage transition")
	}
	if state == "returned" {
		_, err := os.Lstat(filepath.Join(root, stage+".json"))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		owner.Checkpoints[stage] = err == nil
	}
	owner.Stages[stage] = state
	return writePreflightJSON(path, owner)
}

type preflightNativeStatus struct {
	Version     int    `json:"schema_version"`
	RunID       string `json:"run_id"`
	CallerRunID string `json:"caller_run_id"`
	Profile     string `json:"profile"`
	Backend     string `json:"backend"`
	Phase       string `json:"phase"`
	Status      string `json:"status"`
	Session     string `json:"session"`
	Active      *bool  `json:"active"`
}

type preflightOutput struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *preflightOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (2 << 20) - b.buffer.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

var queryPreflightNative = func(ctx context.Context, cp execution.Checkpoint) (preflightNativeStatus, error) {
	var st preflightNativeStatus
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, cp.Command, "task", "show", "--id", cp.RunID, "--json")
	var out preflightOutput
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return st, fmt.Errorf("query original preflight invocation failed: %w", err)
	}
	if out.truncated {
		return st, errors.New("preflight status exceeds limit")
	}
	if err := json.Unmarshal(out.buffer.Bytes(), &st); err != nil {
		return st, errors.New("invalid preflight status document")
	}
	return st, nil
}

var preflightExpectedConfig = func(j *runJob, stage string) (execution.Config, error) {
	if stage == "plan" {
		_, ec, err := acceptanceConfiguration(j.repo)
		return ec, err
	}
	ec, err := execution.LoadConfig()
	ec.Profile = execution.ProfileForRole(ec, "light")
	return ec, err
}

// preflightConfigChanged reports whether the recorded invocation ran under a
// different execution configuration than the one in force now. The engine and
// profile always count. The command path counts only for the CLI engine: the
// library engine records no external command, so the configured path cannot
// have been used by the recorded invocation. An empty engine means the CLI
// engine, matching checkpoints written before the engine field existed.
func preflightConfigChanged(cp execution.Checkpoint, expected execution.Config) bool {
	recorded, current := cp.Engine, expected.Engine
	if recorded == "" {
		recorded = execution.EngineCLI
	}
	if current == "" {
		current = execution.EngineCLI
	}
	if recorded != current || cp.Profile != expected.Profile {
		return true
	}
	return recorded == execution.EngineCLI && cp.Command != expected.Command
}

// preflightLibraryEnded is the Status recorded for a library stage. It shows
// in the evidence file that no native terminal record backs the stage, only
// Offload's confirmation that the run's native processes are gone.
const preflightLibraryEnded = "library-ended"

// confirmPreflightLibraryEnded settles a library-engine preflight invocation
// through confirmLibraryRunEnded. returned is true when the invocation's Run
// returned, which lets a missing worker slot count as ended unless the retained
// Offload run record at the checkpoint's artifact directory reports unresolved
// cleanup. Every failure keeps the attempt pending.
func confirmPreflightLibraryEnded(ctx context.Context, cp execution.Checkpoint, returned bool) (preflightNativeStatus, error) {
	var st preflightNativeStatus
	// A started run always records its artifact directory first. Without it the
	// run record cannot be inspected, so do not assume cleanup succeeded.
	if !executionRunID(cp.RunID) || cp.ArtifactDir == "" {
		return st, fmt.Errorf("%w: library invocation lacks run or artifact identity", preflight.ErrPending)
	}
	if err := confirmLibraryRunEnded(ctx, cp.RunID, cp.ArtifactDir, returned); err != nil {
		return st, fmt.Errorf("%w: %w", preflight.ErrPending, err)
	}
	active := false
	return preflightNativeStatus{Version: 1, RunID: cp.RunID, CallerRunID: cp.CallerRunID, Profile: cp.Profile, Backend: cp.Backend, Phase: "done", Status: preflightLibraryEnded, Session: cp.Session, Active: &active}, nil
}

// Caller holds the issue-wide execution fence, not just a PID observation.
// The two inherited invocation locks are held until settlement is persisted.
func reconcileDispatchPreflight(ctx context.Context, j *runJob, s preflight.Store) error {
	l, err := s.Load()
	if err != nil {
		return err
	}
	st, err := l.Status()
	if err != nil {
		return err
	}
	if st.PendingID == "" {
		return nil
	}
	root := filepath.Join(s.Path()+".attempts", st.PendingID)
	b, err := os.ReadFile(filepath.Join(root, "owner.json"))
	if err != nil {
		return fmt.Errorf("%w: preflight ownership record unavailable", preflight.ErrPending)
	}
	var owner preflightOwner
	if json.Unmarshal(b, &owner) != nil || owner.Version != 1 || owner.Attempt != st.PendingID || owner.Repository != l.Repo || owner.Issue != l.Issue || owner.Stages == nil || owner.Checkpoints == nil {
		return fmt.Errorf("%w: preflight ownership mismatch", preflight.ErrPending)
	}
	for stage, state := range owner.Stages {
		if (stage != "coordination" && stage != "plan") || (state != "started" && state != "returned") {
			return errors.New("invalid preflight stage history")
		}
	}
	if owner.Stages["plan"] != "" && owner.Stages["coordination"] != "returned" {
		return errors.New("plan stage precedes coordination completion")
	}
	evidence := map[string]preflightNativeStatus{}
	for _, stage := range []string{"coordination", "plan"} {
		path := filepath.Join(root, stage+".json")
		lock, err := filelock.Try(path+".lock", map[string]int{"PID": os.Getpid()})
		if err != nil {
			return fmt.Errorf("%w: original invocation owns %s", preflight.ErrPending, stage)
		}
		defer filelock.Release(lock)
		if _, err = os.Lstat(path); os.IsNotExist(err) {
			if owner.Stages[stage] == "started" || owner.Checkpoints[stage] {
				return fmt.Errorf("%w: started stage lacks checkpoint", preflight.ErrPending)
			}
			continue
		} else if err != nil {
			return err
		}
		if owner.Stages[stage] == "" {
			return fmt.Errorf("%w: invocation outside recorded stage", preflight.ErrPending)
		}
		cp, err := execution.ReadCheckpoint(path)
		if err != nil {
			return err
		}
		if !cp.ObservationOnly || cp.RunID == "" || cp.CallerRunID == "" || cp.Backend == "" || cp.Session == "" {
			return fmt.Errorf("%w: incomplete invocation identity", preflight.ErrPending)
		}
		expected, err := preflightExpectedConfig(j, stage)
		if err != nil {
			return err
		}
		if preflightConfigChanged(cp, expected) {
			return fmt.Errorf("%w: execution configuration changed", preflight.ErrPending)
		}
		if cp.Engine == execution.EngineLibrary {
			// Offload's task registry does not list embedded runs, so the CLI
			// query cannot see them. Confirm the native processes are gone instead.
			native, err := confirmPreflightLibraryEnded(ctx, cp, owner.Stages[stage] == "returned")
			if err != nil {
				return err
			}
			evidence[stage] = native
			continue
		}
		native, err := queryPreflightNative(ctx, cp)
		if err != nil {
			return err
		}
		if native.Version != 1 || native.RunID != cp.RunID || native.CallerRunID != cp.CallerRunID || native.Profile != cp.Profile || native.Backend != cp.Backend || native.Session != cp.Session || native.Active == nil || *native.Active || native.Phase != "done" {
			return fmt.Errorf("%w: native identity or terminal state unconfirmed", preflight.ErrPending)
		}
		switch native.Status {
		case "ok", "error", "timeout":
		default:
			return fmt.Errorf("%w: unknown native outcome", preflight.ErrPending)
		}
		evidence[stage] = native
	}
	// Persist terminal identities and phase journal before charging interruption.
	if err = writePreflightJSON(filepath.Join(root, "reconciled-terminal.json"), struct {
		Owner  preflightOwner
		Native map[string]preflightNativeStatus
	}{owner, evidence}); err != nil {
		return err
	}
	return s.Finish(st.PendingID, "failure", "interrupted-preflight", dispatchPreflightNow())
}
