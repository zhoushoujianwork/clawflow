package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

var errHarvestBusy = errors.New("acceptance capacity or PR already busy")
var launchHarvestFn = launchHarvest
var harvestCommand = exec.Command

type reservedAgentSlot struct{}

// Slot files use kernel ownership, shared by schedulers and their child processes.
func tryAgentSlot(limit int) (*os.File, error) {
	if limit <= 0 {
		limit = 4
	}
	return tryHarvestSlot("agents", limit)
}

func tryHarvestSlot(namespace string, limit int) (*os.File, error) {
	lockDir := snapshot.LockDir()
	if namespace == "agents" {
		lockDir = filepath.Join(filepath.Dir(filepath.Dir(config.ConfigPath())), "locks")
	}
	for i := 0; i < limit; i++ {
		f, err := filelock.Try(filepath.Join(lockDir, "capacity", namespace, strconv.Itoa(i)+".lock"), map[string]any{"pid": os.Getpid()})
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, filelock.ErrHeld) {
			return nil, err
		}
	}
	return nil, errHarvestBusy
}

func configuredAgentSlot(ctx context.Context) (func(), error) {
	if ctx.Value(reservedAgentSlot{}) != nil {
		return func() {}, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	f, err := tryAgentSlot(cfg.Settings.MaxConcurrentAgents)
	if err != nil {
		return nil, err
	}
	return func() { filelock.Release(f) }, nil
}

// The child inherits the reservations before Start returns. Closing the parent's
// descriptors (without LOCK_UN) leaves ownership in the child, even if this
// scheduler exits. A crash releases all reservations automatically.
func launchHarvest(ctx context.Context, task harvestTask, limit int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if snapshot.IsLocked("acceptance-"+acceptanceHash(task.repo), task.pr.Number) {
		return errHarvestBusy
	}
	prLock, err := filelock.Try(filepath.Join(snapshot.LockDir(), "acceptance-launch", acceptanceHash(task.repo), strconv.Itoa(task.pr.Number)+".lock"), map[string]any{"pid": os.Getpid()})
	if errors.Is(err, filelock.ErrHeld) {
		return errHarvestBusy
	}
	if err != nil {
		return err
	}
	defer prLock.Close()
	repoSlot, err := tryHarvestSlot("acceptance-"+acceptanceHash(task.repo), task.rc.AcceptanceParallelLimit())
	if err != nil {
		return err
	}
	defer repoSlot.Close()
	slot, err := tryAgentSlot(limit)
	if err != nil {
		return err
	}
	defer slot.Close()
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(clog.LogsDir(), 0700); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(clog.LogsDir(), "acceptance-background.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	child := harvestCommand(binary, "acceptance", "harvest-review", "--repo", task.repo, "--pr", strconv.Itoa(task.pr.Number), "--head", task.pr.HeadSHA, "--timeout", task.timeout.String())
	child.ExtraFiles = []*os.File{prLock, repoSlot, slot}
	child.Stdout, child.Stderr = log, log
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return err
	}
	go func() { _ = child.Wait() }()
	runLog.Info("run/acceptance_launched", "repo", task.repo, "pr", task.pr.Number, "pid", child.Process.Pid)
	return nil
}

// Reviews deliberately do not merge. The next sweep holds the repository run
// lock and rechecks each verdict against the then-current head and base.
func newHarvestReviewCmd() *cobra.Command {
	var repo, head string
	var pr int
	var timeout time.Duration
	cmd := &cobra.Command{Use: "harvest-review", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if pr <= 0 || repo == "" || head == "" || timeout <= 0 || timeout > 24*time.Hour {
			return errors.New("invalid background acceptance arguments")
		}
		for fd := 3; fd <= 5; fd++ {
			f := os.NewFile(uintptr(fd), "acceptance-reservation")
			if f == nil {
				return errors.New("missing acceptance reservation")
			}
			defer f.Close()
			if _, err := f.Stat(); err != nil {
				return fmt.Errorf("missing acceptance reservation: %w", err)
			}
		}
		ctx, cancel := context.WithTimeout(context.WithValue(cmd.Context(), reservedAgentSlot{}, true), timeout)
		defer cancel()
		rc, _, err := acceptanceConfiguration(repo)
		if err != nil {
			return err
		}
		enrolled, err := acceptanceEnrollment(repo)
		if err != nil {
			return err
		}
		if !enrolled || !rc.AutoMerge {
			return nil
		}
		client, err := acceptanceClientFactory(rc)
		if err != nil {
			return err
		}
		if contextual, ok := client.(interface{ SetRequestContext(context.Context) }); ok {
			contextual.SetRequestContext(ctx)
		}
		current, err := client.GetPR(repo, pr)
		if err != nil {
			return err
		}
		if current.HeadSHA != head {
			return nil
		}
		lg, err := clog.Open("run")
		if err != nil {
			return err
		}
		defer lg.Close()
		runLog = lg
		rc.AutoMerge = false
		setCtx := func(c context.Context) {
			if contextual, ok := client.(interface{ SetRequestContext(context.Context) }); ok {
				contextual.SetRequestContext(c)
			}
		}
		if failure := harvestRun(ctx, client, setCtx, repo, rc, current, extractClosesIssue(current.Body), timeout); failure != nil {
			return errors.New("background acceptance failed; see run.log")
		}
		return nil
	}}
	cmd.Flags().StringVar(&repo, "repo", "", "Repository")
	cmd.Flags().StringVar(&head, "head", "", "Submitted PR head")
	cmd.Flags().IntVar(&pr, "pr", 0, "PR number")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "Review timeout")
	return cmd
}

// mergeRepoLock serializes the final identity recheck and merge across PRs.
func mergeRepoLock(repo string) (func(), error) {
	f, err := filelock.Try(filepath.Join(snapshot.LockDir(), "merge", acceptanceHash(repo)+".lock"), map[string]any{"pid": os.Getpid()})
	if err != nil {
		return nil, err
	}
	return func() { filelock.Release(f) }, nil
}
