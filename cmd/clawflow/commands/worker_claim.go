package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
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
		if existing.Task.ID != task.ID || existing.Task.FencingToken != task.FencingToken {
			return fmt.Errorf("retained claim epoch mismatch")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return launchWorkerExecutor(o, task)
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
