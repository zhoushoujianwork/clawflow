// Pending-comment safety net — the "兜底" layer behind v0.32.0's inline
// comment post.
//
// Each successful POST /worker/tasks/:run_id/score stashes a record onto
// disk before attempting the VCS comment. If the inline comment post
// succeeds, the record is cleared. If it fails (network hiccup, token
// expiry, worker crashed between POST and comment), the record survives
// on disk and a periodic backfill loop picks it up later.
//
// Idempotence is defended two ways:
//   1. On-disk record is keyed by run_id, so a retry doesn't multiply.
//   2. Before posting, the backfill calls ListIssueComments and greps for
//      the `<!-- clawflow-scoring v1 run=<uuid> -->` marker; if already
//      present (e.g. a prior worker commented but couldn't clear the
//      stash), the backfill clears without posting.
//
// Store: ~/.clawflow/state/pending-comments.json — JSON object keyed by
// run_id. One mutex serialises read-modify-write; the worker is single-
// process so the coverage is sufficient.
package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// pendingCommentInterval: how often the backfill loop drains stuck
// records. 10 minutes mirrors the score backfill — both are safety nets
// for things the happy path missed and don't need to be snappy.
const pendingCommentInterval = 10 * time.Minute

// pendingCommentInitialDelay: small grace window after boot so the
// inline path has first shot at the comments it's actively producing.
const pendingCommentInitialDelay = 60 * time.Second

// pendingCommentMaxPerPass caps retries per tick. Each attempt is one
// VCS list-comments call plus one potential post-comment call; cheap
// but still worth capping for cost predictability.
const pendingCommentMaxPerPass = 10

// pendingCommentRec is the full set of fields the backfill needs to
// rebuild the comment body without calling SaaS. Self-contained on
// purpose — if SaaS loses state, the local queue can still complete.
type pendingCommentRec struct {
	RunID       string    `json:"run_id"`
	Platform    string    `json:"platform"`
	FullName    string    `json:"full_name"`
	IssueNumber int64     `json:"issue_number"`
	IssueTitle  string    `json:"issue_title,omitempty"`
	Confidence  int       `json:"confidence"`
	Reason      string    `json:"reason"`
	Skipped     bool      `json:"skipped"`
	Threshold   int       `json:"threshold"`
	StashedAt   time.Time `json:"stashed_at"`
	Attempts    int       `json:"attempts,omitempty"`
}

// pendingCommentStore serialises disk read-modify-write so concurrent
// stash/clear calls from different goroutines can't corrupt the file.
var pendingCommentStore sync.Mutex

func pendingCommentsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "state", "pending-comments.json")
}

func loadPendingCommentsLocked() map[string]pendingCommentRec {
	path := pendingCommentsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]pendingCommentRec{}
	}
	m := map[string]pendingCommentRec{}
	_ = json.Unmarshal(data, &m)
	if m == nil {
		m = map[string]pendingCommentRec{}
	}
	return m
}

func savePendingCommentsLocked(m map[string]pendingCommentRec) error {
	path := pendingCommentsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// Write-then-rename for crash safety: a half-written JSON file would
	// wipe the queue on next load.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// stashPendingComment records the intent to post a comment for a
// just-scored run. Called from scoreNewlyCreatedRun BEFORE the inline
// comment attempt so even a crash between /score and the comment post
// is recoverable.
//
// Empty platform/full_name is treated as programmer-error and refused:
// stashing a record we can't address later would only put a permanent
// undeliverable entry in the queue (exactly what v0.33.0 shipped
// against prod pending-score's flat JSON shape). Log + skip so the
// underlying upstream data bug is visible in the log.
func stashPendingComment(rec pendingCommentRec) {
	if rec.RunID == "" {
		return
	}
	if rec.Platform == "" || rec.FullName == "" {
		fmt.Printf("[comment/bf] refusing to stash %s: empty platform/full_name (scoring source didn't populate them)\n", rec.RunID)
		return
	}
	if rec.StashedAt.IsZero() {
		rec.StashedAt = time.Now().UTC()
	}
	pendingCommentStore.Lock()
	defer pendingCommentStore.Unlock()
	m := loadPendingCommentsLocked()
	m[rec.RunID] = rec
	if err := savePendingCommentsLocked(m); err != nil {
		fmt.Printf("[comment/bf] stash failed for %s: %v\n", rec.RunID, err)
	}
}

// clearPendingComment removes a record once a comment has been
// successfully posted (or confirmed already present via marker check).
func clearPendingComment(runID string) {
	if runID == "" {
		return
	}
	pendingCommentStore.Lock()
	defer pendingCommentStore.Unlock()
	m := loadPendingCommentsLocked()
	if _, ok := m[runID]; !ok {
		return
	}
	delete(m, runID)
	if err := savePendingCommentsLocked(m); err != nil {
		fmt.Printf("[comment/bf] clear failed for %s: %v\n", runID, err)
	}
}

// snapshotPendingComments returns a copy of all queued records for the
// backfill loop to iterate. Copy-on-read so the loop can release the
// lock before making VCS calls that could take seconds.
func snapshotPendingComments() []pendingCommentRec {
	pendingCommentStore.Lock()
	defer pendingCommentStore.Unlock()
	m := loadPendingCommentsLocked()
	out := make([]pendingCommentRec, 0, len(m))
	for _, r := range m {
		out = append(out, r)
	}
	return out
}

// bumpAttempts rewrites the attempt counter for observability. Not a
// retry limit — a truly unfixable record just sits in the queue until
// the operator clears it, because "unposted comment" is not worse than
// "lost comment" and we prefer durability to automatic deletion.
func bumpAttempts(runID string) {
	pendingCommentStore.Lock()
	defer pendingCommentStore.Unlock()
	m := loadPendingCommentsLocked()
	if rec, ok := m[runID]; ok {
		rec.Attempts++
		m[runID] = rec
		_ = savePendingCommentsLocked(m)
	}
}

// pendingCommentMarker is the per-run HTML comment the bot writes at the
// top of its scoring comment. Used by the backfill to grep existing
// issue comments and short-circuit if the target already exists.
func pendingCommentMarker(runID string) string {
	return fmt.Sprintf("<!-- clawflow-scoring v1 run=%s -->", runID)
}

// pendingCommentBackfillLoop runs every 10 minutes for the lifetime of
// the worker.
func pendingCommentBackfillLoop(wc *config.WorkerConfig, stopCh <-chan struct{}) {
	t := time.NewTicker(pendingCommentInterval)
	defer t.Stop()
	initial := time.After(pendingCommentInitialDelay)
	for {
		select {
		case <-stopCh:
			return
		case <-initial:
			pendingCommentBackfillPass(wc)
			initial = nil
		case <-t.C:
			pendingCommentBackfillPass(wc)
		}
	}
}

func pendingCommentBackfillPass(wc *config.WorkerConfig) {
	queue := snapshotPendingComments()
	if len(queue) == 0 {
		return
	}
	fmt.Printf("[comment/bf] %d pending comment(s) queued\n", len(queue))

	done := 0
	for _, rec := range queue {
		if done >= pendingCommentMaxPerPass {
			fmt.Printf("[comment/bf] %d more deferred to next tick\n", len(queue)-done)
			return
		}
		tryPostPendingComment(wc, rec)
		done++
	}
}

// tryPostPendingComment runs the backfill attempt for one record:
//   1. Resolve the right VCS client (GitHub App token or GitLab PAT).
//   2. Check whether the marker is already on the issue; if yes, clear.
//   3. Otherwise post the comment; on success, clear.
//   4. On failure, bump attempts and leave it in the queue.
func tryPostPendingComment(wc *config.WorkerConfig, rec pendingCommentRec) {
	ctx := scoreContextFromRec(rec)
	client, err := vcsClientForScoring(wc, ctx)
	if err != nil {
		fmt.Printf("[comment/bf %s] skip: %v\n", rec.RunID, err)
		bumpAttempts(rec.RunID)
		return
	}

	// Defensive marker check — avoids double-posting if a prior worker
	// successfully commented but couldn't persist the clear.
	if existing, lerr := client.ListIssueComments(ctx.FullName, int(ctx.IssueNumber)); lerr == nil {
		marker := pendingCommentMarker(rec.RunID)
		for _, body := range existing {
			if strings.Contains(body, marker) {
				fmt.Printf("[comment/bf %s] marker already present — clearing stash\n", rec.RunID)
				clearPendingComment(rec.RunID)
				return
			}
		}
	}
	// Listing failed (token issue, rate limit, etc.): fall through and
	// attempt the post anyway — a duplicate is better than a missed one.

	body := buildScoringCommentBody(ctx, rec.Confidence, rec.Reason, scoreResponse{
		Skipped:   rec.Skipped,
		Threshold: rec.Threshold,
	})
	if err := client.PostIssueComment(ctx.FullName, int(ctx.IssueNumber), body); err != nil {
		fmt.Printf("[comment/bf %s] post failed: %v\n", rec.RunID, err)
		bumpAttempts(rec.RunID)
		return
	}
	fmt.Printf("[comment/bf %s] posted on %s#%d (backfill after %d earlier attempt(s))\n",
		rec.RunID, rec.FullName, rec.IssueNumber, rec.Attempts)
	clearPendingComment(rec.RunID)
}

// scoreContextFromRec rehydrates a scoreContext from the stashed record.
// LocalPath / BaseBranch aren't persisted in the queue (we don't need
// them for comment posting), but vcsClientForScoring falls back to
// config.Load for the BaseURL it does need.
func scoreContextFromRec(rec pendingCommentRec) scoreContext {
	return scoreContext{
		RunID:       rec.RunID,
		Platform:    rec.Platform,
		FullName:    rec.FullName,
		IssueNumber: rec.IssueNumber,
		IssueTitle:  rec.IssueTitle,
	}
}
