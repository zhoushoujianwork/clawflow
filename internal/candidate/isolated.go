package candidate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

// IsolatedInput describes one isolated run of a pinned commit.
type IsolatedInput struct {
	Commit string
	// Source is any git repository that contains Commit. It is cloned, never
	// built in place, so the caller's checkout is not touched.
	Source string
	// Base holds candidate runtimes; EvidenceDir receives the surviving evidence.
	Base, EvidenceDir string
	Stable            Stable
	Probe             StableProbe
	GoCache, ModCache string
	TaskTimeout       time.Duration
	// LockPath serializes isolated runs on this host. Two runs would otherwise
	// compete for ports and CPU, and an environment race would be misjudged as a
	// defect of the commit. Empty disables the lock (tests use private bases).
	LockPath string
	// Agent overrides the fixture agent script (tests inject faults with it).
	Agent func(clawflow string) string
}

// IsolatedResult is the outcome of an attempted run. Evidence is always set when
// the run was attempted; whether it passed follows from CheckEvidence.
type IsolatedResult struct {
	Evidence     *Evidence
	EvidencePath string
	Digest       string
}

// Passed recomputes the verdict from the evidence, never from a stored flag.
func (r IsolatedResult) Passed() bool {
	return r.Evidence != nil && CheckEvidence(*r.Evidence, r.Evidence.Commit, true) == nil
}

// ErrNotAttempted marks a run that could not be started for a reason that says
// nothing about the commit (a busy port, an unusable base directory, a foreign
// directory, a changed stable host). Callers treat it as an infrastructure
// error and retry; they must not report the commit as failed.
var ErrNotAttempted = errors.New("isolated run could not be attempted")

func notAttempted(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrNotAttempted, fmt.Sprintf(format, a...))
}

// RunIsolated builds the pinned commit into a disposable candidate, starts its
// controller and worker lanes, drives one real task through them, stops
// everything, and checks that no process or lock is left. It reuses the
// candidate runtime (prepare, up, down) and adds the task step.
func RunIsolated(ctx context.Context, in IsolatedInput) (res IsolatedResult, err error) {
	if !ValidCommit(in.Commit) {
		return res, notAttempted("commit %q is not a full lowercase SHA", in.Commit)
	}
	if in.Base == "" || !filepath.IsAbs(in.Base) {
		return res, notAttempted("candidate base directory must be absolute")
	}
	if in.LockPath != "" {
		if err = os.MkdirAll(filepath.Dir(in.LockPath), 0o700); err != nil {
			return res, notAttempted("%v", err)
		}
		lock, lockErr := filelock.Wait(ctx, in.LockPath, map[string]any{"pid": os.Getpid(), "commit": in.Commit})
		if lockErr != nil {
			return res, notAttempted("waiting for another isolated run: %v", lockErr)
		}
		defer filelock.Release(lock)
	}
	port, forge, err := pickPorts(in.Commit, in.Stable)
	if err != nil {
		return res, notAttempted("%v", err)
	}
	p, err := NewPlan(in.Commit, in.Base, port, Pin{})
	if err != nil {
		return res, notAttempted("%v", err)
	}
	p = p.WithForge(forge)
	// A fresh run never inherits tasks, locks or state of an earlier one.
	if old, readErr := ReadManifest(p.Root); readErr == nil {
		if _, downErr := old.Down(); downErr != nil {
			return res, notAttempted("previous run of this commit still has processes: %v", downErr)
		}
		if rmErr := old.Remove(); rmErr != nil {
			return res, notAttempted("cannot remove the previous run: %v", rmErr)
		}
	}
	m, err := Prepare(p, in.Stable, FreePort)
	if err != nil {
		return res, notAttempted("%v", err)
	}
	defer func() {
		_, _ = m.Down()
		KillResidual(m)
		if rmErr := m.Remove(); rmErr != nil && err == nil {
			err = notAttempted("cannot remove the candidate after the run: %v", rmErr)
		}
	}()
	if err = p.WriteConfig(p.TaskConfigOptions()); err != nil {
		return res, notAttempted("%v", err)
	}
	if err = SetupTaskFixture(p); err != nil {
		return res, notAttempted("%v", err)
	}
	if in.Agent != nil {
		if err = os.WriteFile(filepath.Join(p.Bin, "claude"), []byte(in.Agent(filepath.Join(p.Bin, "clawflow"))), 0o700); err != nil {
			return res, notAttempted("%v", err)
		}
	}
	before, err := TakeStable(in.Stable, in.Probe)
	if err != nil {
		return res, notAttempted("cannot snapshot the stable host: %v", err)
	}

	ev := NewEvidence(p, "")
	ev.TaskRequired = true
	ev.Layers["environment-ready"] = Passed
	failedRun := func(layer, reason string) {
		ev.Layers[layer] = Failed
		ev.Task = &TaskRun{Commit: p.Commit, RuntimeID: p.ID, Failure: reason, Settled: true,
			Started: time.Now().UTC().Format(time.RFC3339), Finished: time.Now().UTC().Format(time.RFC3339)}
		ev.Task.Clip()
	}

	checkout := filepath.Join(p.Src, "checkout")
	if err = cloneAt(ctx, in.Source, checkout, p.Commit); err != nil {
		return res, notAttempted("%v", err)
	}
	binary, sha, buildErr := BuildBinaryWith(ctx, p, BuildOptions{Checkout: checkout, GoCache: in.GoCache, ModCache: in.ModCache})
	var be *BuildError
	switch {
	case buildErr == nil:
		ev.Environment["binary_sha256"] = sha
		ev.Command("build", []string{"go", "build", "./cmd/clawflow"}, 0, "ok")
		ev.Layers["candidate-loaded"] = Passed
	case errors.As(buildErr, &be) && be.Compile:
		ev.Command("build", []string{"go", "build", "./cmd/clawflow"}, 1, be.Output)
		failedRun("candidate-loaded", "the commit does not compile: "+be.Output)
	default:
		return res, notAttempted("%v", buildErr)
	}

	if buildErr == nil {
		if _, upErr := m.Up(ctx, binary, ""); upErr != nil {
			ev.Command("up", []string{"candidate", "up"}, 1, upErr.Error())
			failedRun("lifecycle", "the candidate controller and workers did not come up: "+upErr.Error())
		} else {
			ev.Layers["lifecycle"] = Passed
			ev.Command("up", []string{"candidate", "up"}, 0, "controller and worker lanes running")
			run, exErr := Exercise(ctx, m, TaskOptions{Timeout: in.TaskTimeout})
			if exErr != nil {
				return res, notAttempted("%v", exErr)
			}
			ev.Task = run
			ev.Command("exercise", []string{"candidate", "exercise"}, 0, "task "+run.TaskID+" state "+run.State)
		}
	}

	stopped, stopErr := m.Down()
	ev.Command("down", []string{"candidate", "down"}, 0, "stopped: "+strings.Join(stopped, ", "))
	if ev.Task != nil && !ev.Task.Settled {
		FinishTask(m, ev.Task, 5*time.Second)
	}
	if stopErr != nil && ev.Task != nil {
		ev.Task.ResidualProcesses = append(ev.Task.ResidualProcesses, "unstoppable: "+stopErr.Error())
	}
	if ev.Task != nil {
		ev.Task.Clip()
	}
	after, err := TakeStable(in.Stable, in.Probe)
	if err != nil {
		return res, notAttempted("cannot snapshot the stable host after the run: %v", err)
	}
	if diff := before.Diff(after); len(diff) > 0 {
		return res, notAttempted("stable host state changed during the run, cause not attributable: %s", clipText(strings.Join(diff, "; "), 300))
	}
	ev.StableIgnored = before.Ignored(after)
	ev.Finish()

	path := filepath.Join(p.Root, "evidence.json")
	if err = ev.Write(path); err != nil {
		return res, notAttempted("%v", err)
	}
	kept, digest, err := keepEvidence(path, in.EvidenceDir, p.ID)
	if err != nil {
		return res, notAttempted("%v", err)
	}
	return IsolatedResult{Evidence: ev, EvidencePath: kept, Digest: digest}, nil
}

// pickPorts starts from the ports derived from the commit and moves on while one
// is taken, so a port that another program happens to hold never makes every
// retry of the same commit fail the same way.
func pickPorts(commit string, stable Stable) (controller, forge int, err error) {
	base, _ := NewPlan(commit, "/", 0, Pin{})
	derived := base.WithForge(0)
	free := func(start int, avoid int) (int, bool) {
		for i := 0; i < 200; i++ {
			port := 20000 + (start-20000+i)%20000
			if port != avoid && !slices.Contains(stable.Ports, port) && FreePort(port) {
				return port, true
			}
		}
		return 0, false
	}
	controller, ok := free(derived.Port, 0)
	if !ok {
		return 0, 0, errors.New("no free loopback port for the candidate controller")
	}
	forge, ok = free(derived.ForgePort, controller)
	if !ok {
		return 0, 0, errors.New("no free loopback port for the fixture forge")
	}
	return controller, forge, nil
}

// cloneAt makes a private clone of source with commit checked out detached.
func cloneAt(ctx context.Context, source, dir, commit string) error {
	if source == "" {
		return errors.New("no source repository to build from")
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"clone", "--quiet", "--local", "--no-checkout", source, dir},
		{"-C", dir, "checkout", "--quiet", "--detach", commit},
	} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", args[0], err, tail(string(out), 300))
		}
	}
	return nil
}

// keepEvidence copies the evidence out of the disposable root and returns its
// SHA-256, so the acceptance record can cite exactly this file.
func keepEvidence(src, dir, id string) (path, digest string, err error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(data)
	digest = hex.EncodeToString(sum[:])
	if dir == "" {
		return src, digest, nil
	}
	target := filepath.Join(dir, id)
	if err = os.MkdirAll(target, 0o700); err != nil {
		return "", "", err
	}
	path = filepath.Join(target, "evidence.json")
	return path, digest, os.WriteFile(path, data, 0o600)
}
