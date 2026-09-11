package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/branch"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	clog "github.com/zhoushoujianwork/clawflow/internal/log"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// baseBranchNoticeInterval is how often the same unchanged verdict for the
// same repo+base is allowed back into run.log. Every `clawflow run` is a
// fresh process (api.TriggerRun execs the binary), so dedup state has to
// live on disk — an in-process map would dedup nothing across the 2-minute
// auto-run cycle that produced 99 identical WARN lines in a single day
// (issue #315).
var baseBranchNoticeInterval = time.Hour

// baseBranchNotice is the last logged verdict for one repo.
type baseBranchNotice struct {
	Base     string    `json:"base"`
	LoggedAt time.Time `json:"logged_at"`
}

func baseBranchNoticePath() string {
	return filepath.Join(snapshot.DataDir(), "base-branch-notices.json")
}

func loadBaseBranchNotices() map[string]baseBranchNotice {
	m := map[string]baseBranchNotice{}
	data, err := os.ReadFile(baseBranchNoticePath())
	if err != nil {
		return m
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return map[string]baseBranchNotice{}
	}
	return m
}

func saveBaseBranchNotices(m map[string]baseBranchNotice) {
	path := baseBranchNoticePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	// Best-effort: losing this file only costs a duplicate log line.
	_ = os.WriteFile(path, data, 0o644)
}

// shouldLogBaseBranchNotice reports whether the verdict for fullName/base is
// worth another log line, and records the decision when it is. A changed base
// always logs (state transition), an unchanged one at most once per
// baseBranchNoticeInterval.
func shouldLogBaseBranchNotice(fullName, base string, now time.Time) bool {
	notices := loadBaseBranchNotices()
	prev, seen := notices[fullName]
	if seen && prev.Base == base && now.Sub(prev.LoggedAt) < baseBranchNoticeInterval {
		return false
	}
	notices[fullName] = baseBranchNotice{Base: base, LoggedAt: now}
	saveBaseBranchNotices(notices)
	return true
}

// clearBaseBranchNotice drops the dedup record for fullName so the next bad
// verdict logs immediately instead of being swallowed by a stale timestamp.
func clearBaseBranchNotice(fullName string) {
	notices := loadBaseBranchNotices()
	if _, ok := notices[fullName]; !ok {
		return
	}
	delete(notices, fullName)
	saveBaseBranchNotices(notices)
}

// checkBaseBranch validates a repo's configured base_branch against its local
// clone once per scan and returns the verdict so the caller can act on it.
//
// Cost is one local ref read in the healthy case; the ls-remote probe only
// runs when the remote-tracking ref is missing.
//
// Logging is split by strength of signal:
//   - proven invalid (remote answered, has no such ref): logged as
//     run/base_branch_skip, deduped per repo+base. The caller drops the repo
//     from operator dispatch — every analysis operator would otherwise fetch
//     origin/<base>, fail with exit 128 and stamp agent-failed on the issue,
//     round after round (issue #315).
//   - unproven (offline / no credentials / probe timed out): nothing is
//     logged and nothing is blocked, preserving issue #300's guarantee that
//     an offline machine or a fresh clone never stalls a scan.
func checkBaseBranch(lg *clog.Logger, fullName string, repoCfg config.Repo) branch.BaseValidation {
	if !repoCfg.Enabled || repoCfg.LocalPath == "" {
		return branch.BaseValidation{}
	}
	v := branch.ValidateBase(repoCfg.LocalPath, repoCfg.BaseBranch)
	if !v.ProvenInvalid() {
		// Healthy (or unprovable) again: forget any earlier verdict so a
		// future regression is reported at once.
		clearBaseBranchNotice(fullName)
		return v
	}
	if shouldLogBaseBranchNotice(fullName, v.Base, time.Now()) {
		fmt.Fprintf(os.Stderr, "  ⚠ %s: operators skipped — %s\n", fullName, v.Hint())
		lg.Warn("run/base_branch_skip",
			"repo", fullName,
			"base", v.Base,
			"remote_default", v.RemoteDefault,
			"reason", "base_branch does not exist on origin; operators skipped until config is fixed",
		)
	}
	return v
}
