package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/operator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

func replayRepoWriteBacks(client operator.VCS, repo string, onlyIssue int) map[int]bool {
	pending := map[int]bool{}
	root := filepath.Join(snapshot.DataDir(), "runs", strings.ReplaceAll(repo, "/", "__"))
	paths, err := filepath.Glob(filepath.Join(root, "issue-*", "*", "writeback.json"))
	if err != nil {
		return pending
	}
	for _, path := range paths {
		dir := filepath.Dir(path)
		data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
		if err != nil {
			continue
		}
		var meta snapshot.RunMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		if onlyIssue != 0 && meta.IssueNumber != onlyIssue {
			continue
		}
		if meta.Status != "success" && meta.Status != "finalizing" && meta.WritebackError == "" {
			continue
		}
		if err := snapshot.AcquireLock(repo, meta.IssueNumber, meta.Operator); err != nil {
			pending[meta.IssueNumber] = true
			continue
		}
		err = operator.ReplayWriteBack(client, dir)
		snapshot.ReleaseLock(repo, meta.IssueNumber)
		if err != nil {
			pending[meta.IssueNumber] = true
			runLog.Warn("run/writeback_retry", "repo", repo, "issue", meta.IssueNumber, "status", "pending")
			continue
		}
		meta.WritebackError = ""
		if err := snapshot.WriteRunMeta(dir, meta); err != nil {
			continue
		}
	}
	return pending
}
