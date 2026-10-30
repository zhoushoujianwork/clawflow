package commands

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

// One immutable executable is retained per task. The manager may exit or be
// upgraded; its detached executor owns heartbeat, work and durable delivery.
type workerJournal struct {
	Resume         bool
	Version        int
	Task           coordinator.Task
	Options        workerOptions
	OffloadCommand string
	Executable     string
	State          string
	Action         string
	Report         json.RawMessage
	Error          string
	UpdatedAt      time.Time
}

type workerJournalKey struct{}

func writeWorkerJournal(path string, value *workerJournal) error {
	value.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeDurableFile(path, raw, 0600)
}

func writeDurableFile(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".journal-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func readWorkerJournal(path string) (*workerJournal, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var j workerJournal
	decoder := json.NewDecoder(io.LimitReader(f, coordinator.MaxReportBytes+coordinator.MaxRequestBytes))
	if err := decoder.Decode(&j); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("trailing or oversized worker journal")
	}
	if j.Version != 1 || j.Task.ID == "" || j.Executable == "" {
		return nil, fmt.Errorf("invalid worker journal")
	}
	return &j, nil
}

func workerJournalRoot(o workerOptions) string {
	root := o.StateDir
	if root == "" {
		root = config.StateDir()
	}
	// Token pathname isolates logical worker journals without persisting the secret.
	return filepath.Join(root, "worker-executions", workerIdentity(o))
}

func launchWorkerExecutor(o workerOptions, t *coordinator.Task) error {
	if !coordinator.DurableTask(t) {
		return fmt.Errorf("controller did not grant durable execution; upgrade controller first")
	}
	root := workerJournalRoot(o)
	path := filepath.Join(root, t.ID, "journal.json")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("execution journal already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	pinned := o.Executable
	if pinned == "" {
		return fmt.Errorf("worker generation executable was not pinned")
	}
	if _, err := os.Stat(pinned); err != nil {
		return err
	}
	ec, err := execution.LoadConfig()
	if err != nil {
		return err
	}
	offload, err := exec.LookPath(ec.Command)
	if err != nil {
		return err
	}
	native, err := os.ReadFile(offload)
	if err != nil {
		return err
	}
	runtime := filepath.Join(root, "runtimes", fmt.Sprintf("%x", sha256.Sum256(native)), "offload")
	if _, err := os.Stat(runtime); os.IsNotExist(err) {
		if err := writeDurableFile(runtime, native, 0700); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	j := &workerJournal{Version: 1, Task: *t, Options: o, OffloadCommand: runtime, Executable: pinned, State: "prepared"}
	if err := writeWorkerJournal(path, j); err != nil {
		return err
	}
	return spawnWorkerExecutor(path, j)
}

func spawnWorkerExecutor(path string, j *workerJournal) error {
	log, err := os.OpenFile(filepath.Join(filepath.Dir(path), "executor.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(j.Executable, "worker", "execute", "--journal", path)
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Env = os.Environ()
	cmd.SysProcAttr = detachedWorkerAttributes()
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// Recovery never guesses that an interrupted execution is safe to repeat.
// Pending reports and not-yet-started executions are restartable automatically.
func recoverWorkerExecutors(o workerOptions) error {
	paths, err := filepath.Glob(filepath.Join(workerJournalRoot(o), "*", "journal.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if filelock.Held(path + ".lock") {
			continue
		}
		j, err := readWorkerJournal(path)
		if err != nil {
			return err
		}
		switch j.State {
		case "prepared", "pending_sync":
			if err := spawnWorkerExecutor(path, j); err != nil {
				return err
			}
		case "needs_recovery":
			if j.Task.LeaseExpiresAt != nil && !j.Task.LeaseExpiresAt.After(time.Now()) {
				api, err := newWorkerAPI(o)
				if err != nil {
					return err
				}
				recoveryCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				err = prepareWorkerRecovery(recoveryCtx, api, path)
				cancel()
				api.client.CloseIdleConnections()
				if err == nil {
					recovered, readErr := readWorkerJournal(path)
					if readErr != nil {
						return readErr
					}
					if err := spawnWorkerExecutor(path, recovered); err != nil {
						return err
					}
				}
			}
		case "running":
			// Serialize against an executor that is still entering its critical section.
			lock, err := filelock.Try(path+".lock", map[string]any{"PID": os.Getpid()})
			if errors.Is(err, filelock.ErrHeld) {
				continue
			}
			if err != nil {
				return err
			}
			current, err := readWorkerJournal(path)
			if err == nil && current.State == "running" {
				current.State, current.Error = "needs_recovery", "Executor interrupted; retained worktree/session requires verified recovery"
				err = writeWorkerJournal(path, current)
			}
			filelock.Release(lock)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func newWorkerExecuteCmd() *cobra.Command {
	var path string
	cmd := &cobra.Command{Use: "execute", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		lock, err := filelock.Try(path+".lock", map[string]any{"PID": os.Getpid()})
		if errors.Is(err, filelock.ErrHeld) {
			return nil
		}
		if err != nil {
			return err
		}
		defer filelock.Release(lock)
		j, err := readWorkerJournal(path)
		if err != nil {
			return err
		}
		if j.State == "acknowledged" || j.State == "rejected" {
			return nil
		}
		if j.Options.StateDir != "" {
			if err := os.Setenv("CLAWFLOW_STATE_DIR", j.Options.StateDir); err != nil {
				return err
			}
		}
		if j.OffloadCommand != "" {
			if err := os.Setenv("CLAWFLOW_OFFLOAD_COMMAND", j.OffloadCommand); err != nil {
				return err
			}
		}
		api, err := newWorkerAPI(j.Options)
		if err != nil {
			return err
		}
		defer api.client.CloseIdleConnections()
		ctx := context.WithValue(context.Background(), workerJournalKey{}, path)
		ctx = execution.WithCheckpoint(ctx, filepath.Join(filepath.Dir(path), "execution.json"), j.Resume)
		if j.State == "pending_sync" {
			return deliverWorkerReport(ctx, api, path, j)
		}
		if j.State != "prepared" {
			return fmt.Errorf("execution needs verified recovery, not replay")
		}
		if j.Task.LeaseExpiresAt == nil {
			return fmt.Errorf("missing execution authority")
		}
		deadline := j.Task.LeaseExpiresAt.Add(-coordinator.DurableReportGrace)
		if !deadline.After(time.Now()) {
			return fmt.Errorf("execution authority expired")
		}
		ctx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		j.State = "running"
		if err := writeWorkerJournal(path, j); err != nil {
			return err
		}
		reg, err := loadRegistry()
		if err != nil {
			return err
		}
		return runWorkerTask(ctx, api, j.Options, &j.Task, 30*time.Second, func(ctx context.Context, t *coordinator.Task) (*runJob, time.Duration, error) {
			return buildWorkerJob(ctx, t, reg)
		})
	}}
	cmd.Flags().StringVar(&path, "journal", "", "Private execution journal")
	_ = cmd.MarkFlagRequired("journal")
	return cmd
}

func persistWorkerReport(ctx context.Context, api *workerAPI, t *coordinator.Task, action string, result any) error {
	path, _ := ctx.Value(workerJournalKey{}).(string)
	body := map[string]any{"fencing_token": t.FencingToken, "result": result}
	if path == "" {
		return api.post(ctx, "tasks/"+t.ID+"/"+action, body, nil)
	}
	j, err := readWorkerJournal(path)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if len(raw) > coordinator.MaxReportBytes {
		return fmt.Errorf("durable report exceeds limit")
	}
	j.Report, j.Action, j.State = raw, action, "pending_sync"
	if err := writeWorkerJournal(path, j); err != nil {
		return err
	}
	return deliverWorkerReport(context.WithoutCancel(ctx), api, path, j)
}

func deliverWorkerReport(ctx context.Context, api *workerAPI, path string, j *workerJournal) error {
	err := api.post(ctx, "tasks/"+j.Task.ID+"/"+j.Action, j.Report, nil)
	if err != nil {
		var status workerHTTPError
		if errors.As(err, &status) && status == 409 {
			j.State = "rejected"
			j.Error = "Ownership changed; result retained for reconciliation"
			_ = writeWorkerJournal(path, j)
		}
		return err
	}
	j.State, j.Error = "acknowledged", ""
	return writeWorkerJournal(path, j)
}

func detachedWorkerAttributes() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
func workerIdentity(o workerOptions) string {
	token, _ := filepath.Abs(o.TokenFile)
	return fmt.Sprintf("%x", sha256.Sum256([]byte(o.Coordinator+"\n"+token)))[:24]
}
