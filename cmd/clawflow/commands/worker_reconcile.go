package commands

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

// One admissions loop serializes retained claims with new claims. Detached
// executors provide parallelism; the coordinator enforces shared capacity.
func runDurableWorker(ctx context.Context, api *workerAPI, o workerOptions, poll time.Duration) error {
	o.receiptCache = make(map[string][32]byte)
	for ctx.Err() == nil {
		if err := recoverWorkerClaims(ctx, api, o); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if err := recoverWorkerExecutors(o); err != nil {
			return err
		}
		path, request, err := prepareWorkerClaim(o)
		if err != nil {
			return err
		}
		var result struct {
			Task *coordinator.Task `json:"task"`
		}
		err = api.post(ctx, "claim", request, &result)
		if err == nil && result.Task != nil {
			err = reconcileWorkerClaim(ctx, api, o, result.Task)
		}
		if workerFatal(err) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			err = os.Remove(path)
		}
		if diagnosticErr := recordWorkerRecovery(o, path, err); diagnosticErr != nil {
			return diagnosticErr
		}
		// Fill capacity promptly, but bound retries for blocked claims.
		if err == nil && result.Task != nil {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(poll):
		}
	}
	return nil
}

// Diagnostics never replace the corrupt or ambiguous source. Repeated scans
// retain the first observation until its cause changes or reconciliation succeeds.
func recordWorkerRecovery(o workerOptions, source string, cause error) error {
	path := filepath.Join(workerJournalRoot(o), "recovery", fmt.Sprintf("%x.json", sha256.Sum256([]byte(source))))
	if cause == nil {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var prior struct {
		Error string `json:"error"`
	}
	raw, _ := os.ReadFile(path)
	if json.Unmarshal(raw, &prior) == nil && prior.Error == cause.Error() {
		return nil
	}
	raw, err := json.Marshal(map[string]any{"state": "recovery_blocked", "source": source, "error": cause.Error(), "updated_at": time.Now().UTC()})
	if err != nil {
		return err
	}
	slog.Warn("worker/recovery-blocked", "source", source, "err", cause)
	return writeDurableFile(path, raw, 0600)
}

func expiredWorkerAuthority(t *coordinator.Task) bool {
	return coordinator.DurableTask(t) && t.LeaseExpiresAt != nil && !t.LeaseExpiresAt.Add(-coordinator.DurableReportGrace).After(time.Now())
}

func reconcileWorkerClaim(ctx context.Context, api *workerAPI, o workerOptions, task *coordinator.Task) error {
	if expiredWorkerAuthority(task) {
		return reconcileExpiredWorkerClaim(ctx, api, o, task)
	}
	return acceptWorkerClaim(o, task)
}

type workerReceipt struct {
	State     string `json:"state"`
	Committed bool   `json:"report_committed"`
}

func inspectWorkerReceipt(ctx context.Context, api *workerAPI, t *coordinator.Task) (workerReceipt, error) {
	var receipt workerReceipt
	err := api.postOnce(ctx, "tasks/"+t.ID+"/inspect", map[string]any{"fencing_token": t.FencingToken}, &receipt)
	return receipt, err
}

func reconcileExpiredWorkerClaim(ctx context.Context, api *workerAPI, o workerOptions, task *coordinator.Task) error {
	if !expiredWorkerAuthority(task) || task.State != "leased" || task.LeaseOwner == "" || task.FencingToken < 1 {
		return fmt.Errorf("expired claim has no durable leased authority")
	}
	path := filepath.Join(workerJournalRoot(o), task.ID, "journal.json")
	lock, err := filelock.Try(path+".lock", map[string]any{"PID": os.Getpid()})
	if err != nil {
		return err
	}
	defer filelock.Release(lock)
	checkpoint := filepath.Join(filepath.Dir(path), "execution.json")
	sessionLock, err := filelock.Try(checkpoint+".lock", map[string]any{"PID": os.Getpid()})
	if err != nil {
		return err
	}
	defer filelock.Release(sessionLock)
	receipt, err := inspectWorkerReceipt(ctx, api, task)
	if err != nil {
		return err
	}
	if receipt.Committed {
		return nil
	}
	if receipt.State != "leased" {
		return fmt.Errorf("expired claim is no longer leased")
	}
	existing, err := readWorkerJournal(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if existing != nil {
		old := existing.Task
		same := old.ID == task.ID && old.LeaseOwner == task.LeaseOwner && old.Repo == task.Repo && old.Issue == task.Issue && old.PR == task.PR && old.Kind == task.Kind && old.Operator == task.Operator
		if !same {
			return fmt.Errorf("expired claim identity mismatch")
		}
		if old.FencingToken < task.FencingToken && existing.State == "acknowledged" {
			if err := archiveWorkerAttempt(o, path, old.FencingToken); err != nil {
				return err
			}
			if err := os.Remove(checkpoint); err != nil && !os.IsNotExist(err) {
				return err
			}
		} else if old.FencingToken == task.FencingToken && existing.State == "pending_sync" {
			return syncWorkerReportOnce(ctx, api, path, existing)
		} else if old.FencingToken != task.FencingToken || existing.State != "prepared" || existing.Resume || len(existing.Report) > 0 && string(existing.Report) != "null" {
			return fmt.Errorf("expired claim may have executed; retained evidence requires verified recovery")
		}
	}
	if _, err := os.Stat(checkpoint); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return fmt.Errorf("expired claim has a retained execution checkpoint")
	}
	report, err := json.Marshal(map[string]any{"fencing_token": task.FencingToken, "result": map[string]any{"status": "failed", "infra_kind": "launch", "error": "Durable claim was not started before its execution authority expired"}})
	if err != nil {
		return err
	}
	j := &workerJournal{Version: 1, Task: *task, Options: o, Executable: o.Executable, State: "pending_sync", Action: "fail", Report: report}
	if err := writeWorkerJournal(path, j); err != nil {
		return err
	}
	return syncWorkerReportOnce(ctx, api, path, j)
}

func reconcileRetainedWorkerJournal(o workerOptions, path string) error {
	api, err := newWorkerAPI(o)
	if err != nil {
		return err
	}
	defer api.client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return reconcileWorkerReceipt(ctx, api, o, path)
}

func reconcileWorkerReceipt(ctx context.Context, api *workerAPI, o workerOptions, path string) error {
	j, err := readWorkerJournal(path)
	if err != nil {
		return err
	}
	if j.State == "prepared" {
		return reconcileExpiredWorkerClaim(ctx, api, o, &j.Task)
	}
	lock, err := filelock.Try(path+".lock", map[string]any{"PID": os.Getpid()})
	if errors.Is(err, filelock.ErrHeld) {
		return nil
	}
	if err != nil {
		return err
	}
	defer filelock.Release(lock)
	sessionLock, err := filelock.Try(filepath.Join(filepath.Dir(path), "execution.json.lock"), map[string]any{"PID": os.Getpid()})
	if errors.Is(err, filelock.ErrHeld) {
		return nil
	}
	if err != nil {
		return err
	}
	defer filelock.Release(sessionLock)
	j, err = readWorkerJournal(path)
	if err != nil {
		return err
	}
	if j.State != "acknowledged" {
		return nil
	}
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	if prior, ok := o.receiptCache[path]; ok && prior == digest {
		return nil
	}
	verified := func() {
		if o.receiptCache != nil {
			o.receiptCache[path] = digest
		}
	}
	receipt, err := inspectWorkerReceipt(ctx, api, &j.Task)
	if errors.Is(err, workerHTTPError(409)) {
		verified()
		return nil
	} // Historical, superseded receipt.
	if err != nil {
		return err
	}
	if receipt.Committed {
		verified()
		return nil
	}
	if receipt.State != "leased" {
		return fmt.Errorf("acknowledged report has no committed controller result")
	}
	return syncWorkerReportOnce(ctx, api, path, j)
}

func syncWorkerReportOnce(ctx context.Context, api *workerAPI, path string, j *workerJournal) error {
	var report struct {
		FencingToken int64           `json:"fencing_token"`
		Result       json.RawMessage `json:"result"`
	}
	if json.Unmarshal(j.Report, &report) != nil || report.FencingToken != j.Task.FencingToken || len(report.Result) == 0 || string(report.Result) == "null" || (j.Action != "complete" && j.Action != "fail") {
		return fmt.Errorf("retained report does not match its execution epoch")
	}
	j.State = "pending_sync"
	if err := writeWorkerJournal(path, j); err != nil {
		return err
	}
	err := api.postOnce(ctx, "tasks/"+j.Task.ID+"/"+j.Action, j.Report, nil)
	if err != nil {
		return err
	}
	j.State, j.Error = "acknowledged", ""
	return writeWorkerJournal(path, j)
}
