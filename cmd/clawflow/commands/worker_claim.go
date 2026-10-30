package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

func prepareWorkerClaim(o workerOptions) (string, any, error) {
	request := map[string]string{"claim_id": execution.NewSessionKey()}
	raw, _ := json.Marshal(request)
	path := filepath.Join(workerJournalRoot(o), "claims", request["claim_id"]+".json")
	return path, request, writeDurableFile(path, raw, 0600)
}

func acceptWorkerClaim(o workerOptions, task *coordinator.Task) error {
	path := filepath.Join(workerJournalRoot(o), task.ID, "journal.json")
	if existing, err := readWorkerJournal(path); err == nil {
		if existing.Task.ID != task.ID {
			return fmt.Errorf("retained claim epoch mismatch")
		}
		if existing.Task.FencingToken != task.FencingToken {
			return retryAcknowledgedWorkerClaim(o, task, path)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return launchWorkerExecutor(o, task)
}

// Infrastructure retries reuse task IDs. Keep the old receipt and checkpoint,
// but never treat an unacknowledged or still-owned execution as replaceable.
func retryAcknowledgedWorkerClaim(o workerOptions, task *coordinator.Task, path string) error {
	lock, err := filelock.Try(path+".lock", map[string]any{"PID": os.Getpid()})
	if err != nil {
		return err
	}
	checkpointLock, err := filelock.Try(filepath.Join(filepath.Dir(path), "execution.json.lock"), map[string]any{"PID": os.Getpid()})
	if err != nil {
		filelock.Release(lock)
		return err
	}
	j, err := func() (*workerJournal, error) {
		existing, err := readWorkerJournal(path)
		if err != nil {
			return nil, err
		}
		old := existing.Task
		if (existing.State != "acknowledged" && existing.State != "released") || old.ID != task.ID || old.FencingToken >= task.FencingToken ||
			old.LeaseOwner != task.LeaseOwner || old.Repo != task.Repo || old.Issue != task.Issue || old.PR != task.PR || old.Kind != task.Kind || old.Operator != task.Operator ||
			!coordinator.DurableTask(task) || task.State != "leased" || task.LeaseExpiresAt == nil || !task.LeaseExpiresAt.Add(-coordinator.DurableReportGrace).After(time.Now()) {
			return nil, fmt.Errorf("retained claim epoch mismatch: previous execution is not an acknowledged retry")
		}

		if existing.State == "released" {
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "execution.json")); !os.IsNotExist(err) {
				return nil, fmt.Errorf("released execution has an unexpected checkpoint")
			}
			if existing.Action != "release" {
				return nil, fmt.Errorf("released execution has no release intent")
			}
		}
		if err := archiveWorkerAttempt(o, path, old.FencingToken); err != nil {
			return nil, err
		}
		// An interrupted fresh retry must not resume the previous attempt's
		// native session before it has created its own checkpoint.
		if err := os.Remove(filepath.Join(filepath.Dir(path), "execution.json")); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		return prepareWorkerExecutor(o, task)
	}()
	filelock.Release(checkpointLock)
	filelock.Release(lock)
	if err != nil {
		return err
	}
	return spawnWorkerExecutor(path, j)
}

func archiveWorkerAttempt(o workerOptions, path string, epoch int64) error {
	dir := filepath.Dir(path)
	parent := filepath.Join(workerJournalRoot(o), "history", filepath.Base(dir))
	archive := filepath.Join(parent, fmt.Sprintf("epoch-%d", epoch))
	if raw, err := os.ReadFile(filepath.Join(archive, "journal.json")); err == nil {
		current, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(raw, current) {
			return fmt.Errorf("retained attempt archive conflicts with journal")
		}
		return nil // A previous manager archived this attempt before exiting.
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	staged, err := os.MkdirTemp(parent, ".attempt-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staged)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".lock") {
			continue // Lock inodes stay at their original, persistent paths.
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unexpected attempt artifact: %s", entry.Name())
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return err
		}
		if err := writeDurableFile(filepath.Join(staged, entry.Name()), raw, 0600); err != nil {
			return err
		}
	}
	if err := os.Rename(staged, archive); err != nil {
		return err
	}
	f, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func recoverWorkerClaims(ctx context.Context, api *workerAPI, o workerOptions) error {
	paths, err := filepath.Glob(filepath.Join(workerJournalRoot(o), "claims", "*.json"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var request map[string]string
		if json.Unmarshal(raw, &request) != nil || request["claim_id"] == "" {
			return fmt.Errorf("invalid pending claim journal")
		}
		var result struct {
			Task *coordinator.Task `json:"task"`
		}
		if err := api.post(ctx, "claim", request, &result); err != nil {
			return err
		}
		if result.Task != nil {
			if err := acceptWorkerClaim(o, result.Task); err != nil {
				return err
			}
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}
