package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordination"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
	"github.com/zhoushoujianwork/clawflow/internal/preflight"
	"github.com/zhoushoujianwork/clawflow/internal/queue"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

var dispatchPreflightPlan = prepareImplementationPlan
var dispatchPreflightNow = time.Now

func dispatchPreflightStore(j *runJob) preflight.Store {
	return preflight.Store{Root: snapshot.DataDir(), Repo: j.repo, Issue: j.sub.Number}
}

// prepareDispatch keeps coordination and planning in one durable failure round.
// Unknown interrupted calls stay pending until their identity is reconciled.
func prepareDispatch(ctx context.Context, j *runJob, tasks coordinationTasks) (allowed bool, failure error) {
	if j.op.Name != "implement" && j.op.Name != "repair-ci" {
		return true, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	store := dispatchPreflightStore(j)
	fence, err := filelock.Try(store.Path()+".execution.lock", map[string]int{"PID": os.Getpid()})
	if err != nil {
		return false, err
	}
	defer filelock.Release(fence)
	if err = reconcileDispatchPreflight(ctx, j, store); err != nil {
		return false, errors.Join(err, publishPreflightWait(j, store, err))
	}
	id, err := store.Begin("dispatch", dispatchPreflightNow())
	if err != nil {
		return false, errors.Join(err, publishPreflightWait(j, store, err))
	}
	root := filepath.Join(store.Path()+".attempts", id)
	coordinationPath := filepath.Join(root, "coordination.json")
	planPath := filepath.Join(root, "plan.json")
	stage := "coordination"
	defer func() {
		// An inherited lock can outlive the scheduler. Never settle/reassign it.
		if filelock.Held(coordinationPath+".lock") || filelock.Held(planPath+".lock") {
			allowed = false
			failure = errors.Join(failure, preflight.ErrPending, publishPreflightWait(j, store, preflight.ErrPending))
			return
		}
		outcome, reason := "waiting", "normal-wait"
		if failure != nil {
			outcome, reason = "failure", stage+"-failed"
		}
		if errors.Is(failure, filelock.ErrHeld) {
			outcome, reason = "waiting", "resource-busy"
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			outcome, reason = "waiting", "scan-cancelled"
		}
		if allowed && failure == nil {
			outcome, reason = "success", ""
		}
		if err := store.Finish(id, outcome, reason, dispatchPreflightNow()); err != nil {
			allowed = false
			failure = errors.Join(failure, err)
		}
		if failure != nil {
			failure = errors.Join(failure, publishPreflightWait(j, store, failure))
		}
	}()
	if err := writePreflightOwner(root, id, store); err != nil {
		return false, err
	}
	if err := recordPreflightStage(root, "coordination", "started"); err != nil {
		return false, err
	}
	allowed, failure = coordinateDispatch(execution.WithObservationCheckpoint(ctx, coordinationPath), j, tasks)
	if err := recordPreflightStage(root, "coordination", "returned"); err != nil {
		return false, errors.Join(failure, err)
	}
	if failure != nil || !allowed {
		return false, failure
	}
	allowed = false
	stage = "plan"
	if err := recordPreflightStage(root, "plan", "started"); err != nil {
		return false, err
	}
	plan, err := dispatchPreflightPlan(execution.WithObservationCheckpoint(ctx, planPath), j)
	if recordErr := recordPreflightStage(root, "plan", "returned"); recordErr != nil {
		return false, errors.Join(err, recordErr)
	}
	if err != nil {
		return false, err
	}
	j.acceptancePlan = plan
	stage = "fresh-evidence"
	if j.coordination != nil {
		fresh, err := collectCoordination(ctx, j, tasks, "")
		if err != nil {
			return false, err
		}
		if coordination.Digest(fresh) != coordination.Digest(*j.coordination) {
			return false, errCoordinationChanged
		}
	}
	return true, nil
}

func publishPreflightWait(j *runJob, store preflight.Store, cause error) error {
	state, reason := "blocked", "预检记录不可用，需要检查本地诊断；未启动新调用"
	if l, err := store.Load(); err == nil {
		state = "waiting"
		st, err := l.Status()
		if err != nil {
			return err
		}
		reason = fmt.Sprintf("预检服务失败 %d/%d；下次重试 %s", st.Failures, preflight.MaxFailures, st.NotBefore.Format(time.RFC3339))
		if st.PendingID != "" {
			state = "blocked"
			reason = "预检调用尚未核对终态；保留原调用，需核对执行身份"
		} else if st.Failures >= preflight.MaxFailures {
			state = "blocked"
			reason = fmt.Sprintf("预检服务失败 %d/%d，自动重试已停止；需维护者处理服务故障并执行带原因的恢复", st.Failures, preflight.MaxFailures)
		}
	}
	if errors.Is(cause, filelock.ErrHeld) {
		state = "waiting"
		reason = "预检账本正在使用，等待原调用"
	}
	st, err := queue.LoadState(j.repo)
	if err != nil {
		return err
	}
	found := false
	for i := range st.Entries {
		if st.Entries[i].Number == j.sub.Number {
			st.Entries[i].State = state
			st.Entries[i].Reason = reason
			found = true
			break
		}
	}
	if !found {
		tier, _ := queue.PriorityFromLabels(j.sub.Labels)
		st.Entries = append(st.Entries, queue.EntryRecord{Number: j.sub.Number, Title: j.sub.Title, Priority: tier.String(), Rank: -1, State: state, Reason: reason})
	}
	return queue.SaveState(st)
}
