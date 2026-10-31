package commands

import (
	"context"
	"encoding/json"
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
	if j.State != "needs_recovery" && j.State != "running" {
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
	if cp.Command != j.OffloadCommand || configuredDevelopmentProfile(roleForOperator(j.Task.Operator)) != cp.Profile {
		return fmt.Errorf("execution profile or pinned runtime changed")
	}
	if _, err := os.Stat(cp.Command); err != nil {
		return fmt.Errorf("pinned Offload runtime unavailable")
	}
	if cp.ArtifactDir != "" {
		raw, err := os.ReadFile(filepath.Join(cp.ArtifactDir, "stdout.json"))
		var final struct {
			Status string `json:"status"`
		}
		if err == nil && json.Unmarshal(raw, &final) == nil && final.Status == "ok" {
			return fmt.Errorf("completed native result retained; reconcile output before resuming")
		}
	}
	if cp.Completed || cp.Session == "" {
		return fmt.Errorf("session is missing or already completed; manual result reconciliation required")
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
	return writeWorkerJournal(path, j)
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
