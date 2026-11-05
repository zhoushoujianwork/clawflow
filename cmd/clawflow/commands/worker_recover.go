package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

func prepareWorkerRecovery(ctx context.Context, api *workerAPI, path string) error {
	lock, err := filelock.Try(path+".lock", map[string]any{"PID": os.Getpid()})
	if err != nil {
		return err
	}
	defer filelock.Release(lock)
	j, err := readWorkerJournal(path)
	if err != nil {
		return err
	}
	var report struct {
		FencingToken int64           `json:"fencing_token"`
		Result       json.RawMessage `json:"result"`
	}
	acknowledged := j.State == "acknowledged"
	if acknowledged && (json.Unmarshal(j.Report, &report) != nil || report.FencingToken != j.Task.FencingToken || j.Action != "fail" || !coordinator.InterruptedResult(report.Result)) {
		return fmt.Errorf("acknowledged report is not an execution interruption")
	}
	if !acknowledged && j.State != "needs_recovery" && j.State != "running" {
		return fmt.Errorf("journal is not an interrupted execution")
	}
	if j.Task.Kind != "implement" && j.Task.Kind != "repair" {
		return fmt.Errorf("native session recovery supports development and repair; other task kinds require a fresh reviewed retry")
	}
	checkpoint := filepath.Join(filepath.Dir(path), "execution.json")
	executionLock, err := filelock.Try(checkpoint+".lock", map[string]any{"PID": os.Getpid()})
	if err != nil {
		return fmt.Errorf("original Offload execution still owns recovery lock: %w", err)
	}
	defer filelock.Release(executionLock)
	cp, err := execution.ReadCheckpoint(checkpoint)
	if err != nil {
		return err
	}
	cpEngine := cp.Engine
	if cpEngine == "" {
		cpEngine = execution.EngineCLI
	}
	journalEngine := workerExecutionEngine(j)
	if cpEngine != journalEngine || configuredDevelopmentProfile(roleForOperator(j.Task.Operator)) != cp.Profile {
		return fmt.Errorf("execution profile or pinned runtime changed")
	}
	if cpEngine == execution.EngineCLI {
		if cp.Command != j.OffloadCommand {
			return fmt.Errorf("execution profile or pinned runtime changed")
		}
		if _, err := os.Stat(cp.Command); err != nil {
			return fmt.Errorf("pinned Offload runtime unavailable")
		}
	}
	if cp.ArtifactDir != "" {
		for _, name := range []string{"stdout.json", "result.json"} {
			raw, err := os.ReadFile(filepath.Join(cp.ArtifactDir, name))
			if name == "result.json" && err == nil {
				return fmt.Errorf("completed native result retained; reconcile output before resuming")
			}
			var final struct {
				Status string `json:"status"`
			}
			if err == nil && json.Unmarshal(raw, &final) == nil && final.Status == "ok" {
				return fmt.Errorf("completed native result retained; reconcile output before resuming")
			}
		}
	}
	if cp.Completed || cp.Session == "" {
		return fmt.Errorf("session is missing or already completed; manual result reconciliation required")
	}
	if cpEngine == execution.EngineLibrary {
		if !executionRunID(cp.RunID) {
			return fmt.Errorf("library execution run ID is missing; orphan state is unknown and recovery is postponed")
		}
		state, abandonErr := abandonOrphanedRun(ctx, cp.RunID)
		if abandonErr != nil {
			return fmt.Errorf("library execution orphan state is unknown; recovery is postponed: %w", abandonErr)
		}
		if state != AbandonEnded && state != AbandonAlreadyEnded {
			return fmt.Errorf("library execution orphan state is unknown; recovery is postponed")
		}
	}
	if acknowledged {
		var r struct {
			Provenance struct {
				Session string `json:"session_id"`
			} `json:"provenance"`
		}
		if json.Unmarshal(report.Result, &r) != nil || r.Provenance.Session != cp.Session {
			return fmt.Errorf("checkpoint session differs from acknowledged interruption")
		}
	}
	if !filepath.IsAbs(cp.Workspace) {
		return fmt.Errorf("invalid recorded worktree")
	}
	canonical, err := filepath.EvalSymlinks(cp.Workspace)
	if err != nil || canonical != cp.Workspace {
		return fmt.Errorf("worktree identity changed")
	}
	origin, err := sessionGit(ctx, cp.Workspace, "config", "--get", "remote.origin.url")
	if err != nil || !strings.EqualFold(extractOwnerRepo(origin), j.Task.Repo) {
		return fmt.Errorf("worktree repository mismatch")
	}
	if _, err := sessionGit(ctx, cp.Workspace, "rev-parse", "HEAD"); err != nil {
		return err
	}
	// Task+issue locks remain authoritative for worktree ownership during launch.
	if j.Task.LeaseExpiresAt == nil || j.Task.LeaseExpiresAt.After(time.Now()) {
		return fmt.Errorf("previous execution authority has not expired; recovery postponed")
	}
	if acknowledged {
		// Archive before requesting authority; a lost response can be retried
		// against the unchanged journal without losing the original receipt.
		if err := archiveWorkerAttempt(j.Options, path, j.Task.FencingToken); err != nil {
			return err
		}
	}
	var recovered struct {
		Task *coordinator.Task `json:"task"`
	}
	if err := api.postOnce(ctx, "tasks/"+j.Task.ID+"/recover", map[string]any{"fencing_token": j.Task.FencingToken}, &recovered); err != nil {
		return err
	}
	if recovered.Task == nil {
		return fmt.Errorf("controller did not authorize recovery")
	}
	// The new epoch is durable before another executor can start.
	j.Task = *recovered.Task
	j.Resume = true
	j.State = "prepared"
	j.Error = ""
	j.Report, j.Action = nil, ""
	return writeWorkerJournal(path, j)
}

func executionRunID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	for i, r := range id {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || i > 0 && (r == '_' || r == '.' || r == ':' || r == '-') {
			continue
		}
		return false
	}
	return true
}

func recoverAcknowledgedInterruption(o workerOptions, path string, j *workerJournal) error {
	if j.Task.LeaseExpiresAt == nil || j.Task.LeaseExpiresAt.After(time.Now()) {
		return nil
	}
	api, err := newWorkerAPI(o)
	if err != nil {
		return err
	}
	defer api.client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err = prepareWorkerRecovery(ctx, api, path); err != nil {
		// Old/superseded receipts must never be revived. Retain the journal.
		if errors.Is(err, workerHTTPError(409)) {
			return nil
		}
		return err
	}
	j, err = readWorkerJournal(path)
	if err != nil {
		return err
	}
	return spawnWorkerExecutor(path, j)
}

func newWorkerRecoveryCommands() []*cobra.Command {
	var path string
	recover := &cobra.Command{Use: "recover", Short: "Resume a retained implementation worktree/session after authority reconciliation", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		j, err := readWorkerJournal(path)
		if err != nil {
			return err
		}
		api, err := newWorkerAPI(j.Options)
		if err != nil {
			return err
		}
		defer api.client.CloseIdleConnections()
		if err := prepareWorkerRecovery(cmd.Context(), api, path); err != nil {
			return err
		}
		j, err = readWorkerJournal(path)
		if err != nil {
			return err
		}
		return spawnWorkerExecutor(path, j)
	}}
	recover.Flags().StringVar(&path, "journal", "", "Private retained task journal")
	_ = recover.MarkFlagRequired("journal")
	var root string
	status := &cobra.Command{Use: "executions", Short: "List durable local executions and pending recovery", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		paths, err := filepath.Glob(filepath.Join(root, "*", "journal.json"))
		if err != nil {
			return err
		}
		rows := []map[string]any{}
		for _, path := range paths {
			j, err := readWorkerJournal(path)
			if err != nil {
				rows = append(rows, map[string]any{"state": "recovery_blocked", "journal": path, "error": err.Error()})
				continue
			}
			rows = append(rows, map[string]any{"task": j.Task.ID, "repo": j.Task.Repo, "issue": j.Task.Issue, "attempt": j.Task.Attempt + 1, "state": j.State, "executor_live": filelock.Held(path + ".lock"), "error": j.Error, "journal": path, "updated_at": j.UpdatedAt})
		}
		diagnostics, err := filepath.Glob(filepath.Join(root, "recovery", "*.json"))
		if err != nil {
			return err
		}
		for _, path := range diagnostics {
			raw, err := os.ReadFile(path)
			if err != nil {
				rows = append(rows, map[string]any{"state": "recovery_blocked", "source": path, "error": err.Error()})
				continue
			}
			var row map[string]any
			if err := json.Unmarshal(raw, &row); err != nil {
				rows = append(rows, map[string]any{"state": "recovery_blocked", "source": path, "error": err.Error()})
				continue
			}
			rows = append(rows, row)
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
	}}
	status.Flags().StringVar(&root, "root", "", "Worker execution root (from generation descriptor location)")
	_ = status.MarkFlagRequired("root")
	return []*cobra.Command{recover, status}
}
