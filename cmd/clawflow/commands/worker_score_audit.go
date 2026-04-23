// Append-only audit log of every run the CLI has successfully scored.
//
// Motivation: v0.32.0 added inline comment posting after /score success
// but didn't persist anything. v0.33.0 added the pending-comments queue
// covering the window between /score and comment post. That still leaves
// runs scored by pre-v0.33 workers (observed: zhoushoujianwork/clawflow#32
// scored by an earlier build of this worker; no stash record ever written,
// no comment on the issue).
//
// The scored.jsonl log captures the ground truth "this run has a
// committed SaaS-side score, and the CLI is responsible for its comment"
// at the earliest moment we know it — right after the /score POST returns
// 200. On every worker boot auditScoredAtBoot replays the log and ensures
// any run not yet commented is back in the pending-comments queue.
//
// Design: append-only JSONL. One object per line. Each line carries the
// full comment-building context so a replay never needs to hit SaaS to
// reconstruct the verdict.
package commands

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// scoredRec is one entry in scored.jsonl. Mirrors pendingCommentRec on
// purpose — the audit log is a superset of the pending queue.
type scoredRec struct {
	RunID       string    `json:"run_id"`
	Platform    string    `json:"platform"`
	FullName    string    `json:"full_name"`
	IssueNumber int64     `json:"issue_number"`
	IssueTitle  string    `json:"issue_title,omitempty"`
	Confidence  int       `json:"confidence"`
	Reason      string    `json:"reason"`
	Skipped     bool      `json:"skipped"`
	Threshold   int       `json:"threshold"`
	ScoredAt    time.Time `json:"scored_at"`
}

func scoredLogPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "state", "scored.jsonl")
}

var scoredLogWrite sync.Mutex

// appendScoredLog commits a scoredRec to the append-only audit log.
// Called ONCE per run, immediately after /score returns 200 — before
// the inline comment attempt. If the worker crashes between this call
// and posting the comment, auditScoredAtBoot recovers on next start.
func appendScoredLog(rec scoredRec) {
	if rec.RunID == "" || rec.Platform == "" || rec.FullName == "" {
		return
	}
	if rec.ScoredAt.IsZero() {
		rec.ScoredAt = time.Now().UTC()
	}
	scoredLogWrite.Lock()
	defer scoredLogWrite.Unlock()
	path := scoredLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(line)
	_, _ = f.WriteString("\n")
}

// readScoredLog returns every entry in scored.jsonl (read newest-last).
// Best-effort: corrupt lines are skipped rather than aborting the scan.
func readScoredLog() []scoredRec {
	f, err := os.Open(scoredLogPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	out := []scoredRec{}
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for s.Scan() {
		line := s.Bytes()
		if len(line) == 0 {
			continue
		}
		var r scoredRec
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

// auditScoredAtBoot runs once during worker startup, synchronously, before
// the comment-backfill loop has had a chance to tick. For each entry in
// scored.jsonl, checks whether the corresponding VCS issue already has
// the scoring-comment marker; if not, re-stashes into the pending queue
// so the comment-backfill loop will catch it.
//
// Best-effort: any failure to resolve a VCS client, list comments, or
// reach SaaS leaves the entry unchanged. The next boot retries.
func auditScoredAtBoot(wc *config.WorkerConfig) {
	log := readScoredLog()
	if len(log) == 0 {
		return
	}
	// Short-circuit: if pending-comments already has every run_id in the
	// audit log, there's nothing to reconcile right now.
	pending := snapshotPendingComments()
	pendingSet := map[string]struct{}{}
	for _, p := range pending {
		pendingSet[p.RunID] = struct{}{}
	}

	// Dedup: the audit log is append-only, so a given run may appear
	// more than once (scoring retries, backfill reruns). Keep only the
	// latest scored_at per run_id.
	latest := map[string]scoredRec{}
	for _, r := range log {
		if prev, ok := latest[r.RunID]; !ok || r.ScoredAt.After(prev.ScoredAt) {
			latest[r.RunID] = r
		}
	}

	checked := 0
	restashed := 0
	for runID, rec := range latest {
		if _, already := pendingSet[runID]; already {
			continue
		}
		// Short VCS lookup: has this issue already been commented?
		client, err := vcsClientForScoring(wc, scoreContext{
			Platform: rec.Platform,
			FullName: rec.FullName,
		})
		if err != nil {
			continue // token missing; leave for retry next boot
		}
		comments, err := client.ListIssueComments(rec.FullName, int(rec.IssueNumber))
		if err != nil {
			continue
		}
		marker := pendingCommentMarker(runID)
		seen := false
		for _, body := range comments {
			if strings.Contains(body, marker) {
				seen = true
				break
			}
		}
		checked++
		if seen {
			continue
		}
		stashPendingComment(pendingCommentRec{
			RunID:       rec.RunID,
			Platform:    rec.Platform,
			FullName:    rec.FullName,
			IssueNumber: rec.IssueNumber,
			IssueTitle:  rec.IssueTitle,
			Confidence:  rec.Confidence,
			Reason:      rec.Reason,
			Skipped:     rec.Skipped,
			Threshold:   rec.Threshold,
		})
		restashed++
	}
	if restashed > 0 {
		fmt.Printf("[audit] re-stashed %d run(s) missing comment markers (checked %d, skipped %d pending)\n",
			restashed, checked, len(pending))
	}
}
