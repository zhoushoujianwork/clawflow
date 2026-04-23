// Pending-score backfill — safety net for runs the inline discover path
// never scored.
//
// The happy path (issue #27 inline) covers >99% of cases: the CLI
// discovers an issue, POSTs /worker/discover, gets `created=true`, and
// immediately scores locally. But real systems drop runs on the floor:
//   - worker crashed mid-score before POSTing /score
//   - /score POST returned 4xx (e.g. the `reason` vs `reasoning` field
//     mismatch bug that produced 8 zombie runs before v0.30.1)
//   - network flake between score Claude finishing and POST landing
//   - /discover succeeded while the scorer was temporarily blocked
//
// SaaS side keeps a `/worker/tasks/pending-score` endpoint that returns
// any run in `pending` state with a NULL confidence. This loop polls it
// on a leisurely 10-minute ticker and scores anything it finds, so the
// UI never shows a permanently-stuck row. Session-scoped dedup prevents
// the same broken run from burning Claude credits on every tick if its
// score keeps failing for some non-transient reason.
package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
)

// pendingScoreInterval keeps the backfill low-pressure. The inline path
// handles fresh discovers within ~90s; backfill only needs to catch the
// tail of genuinely-missed ones and can run every 10 minutes without
// impacting UX.
const pendingScoreInterval = 10 * time.Minute

// pendingScoreInitialDelay: wait a bit after worker boot before the first
// pass so the discover loop's initial burst has a chance to score its
// own runs inline. Avoids racing the happy path on a cold start.
const pendingScoreInitialDelay = 45 * time.Second

// pendingScoreMaxPerPass caps how many scoring calls one backfill tick
// triggers — each is a Claude invocation costing tokens, and the real-
// world cardinality of the pending queue should be tiny. Overflow drains
// across ticks.
const pendingScoreMaxPerPass = 3

// pendingScoreTask matches the SaaS /pending-score response entry.
// Mirrors scoreContext's fields so we can feed it straight into
// scoreNewlyCreatedRun without translation.
type pendingScoreTask struct {
	RunID string `json:"run_id"`
	Repo  struct {
		Platform   string `json:"platform"`
		FullName   string `json:"full_name"`
		BaseBranch string `json:"base_branch"`
		LocalPath  string `json:"local_path"`
	} `json:"repo"`
	IssueNumber int64  `json:"issue_number"`
	IssueTitle  string `json:"issue_title"`
	IssueBody   string `json:"issue_body,omitempty"`
	IssueURL    string `json:"issue_url,omitempty"`
}

// pendingScoreEnvelope accepts both `{tasks: [...]}` and a bare array
// for resilience against future SaaS shape tweaks — same pattern the
// stale-PR endpoint uses.
type pendingScoreEnvelope struct {
	Tasks []pendingScoreTask `json:"tasks"`
}

// backfillAttempted tracks run_ids we already re-scored (successfully or
// not) in this worker session. Keyed on run_id — prevents an unfixably-
// bad run from burning a Claude call every 10 min forever. Reset at
// worker restart, which is the intended recovery path for operator
// intervention.
var backfillAttempted sync.Map // map[string]struct{}

// pendingScoreBackfillLoop runs pendingScoreBackfillPass on a 10-minute
// ticker for the lifetime of the worker.
func pendingScoreBackfillLoop(wc *config.WorkerConfig, stopCh <-chan struct{}) {
	t := time.NewTicker(pendingScoreInterval)
	defer t.Stop()
	initial := time.After(pendingScoreInitialDelay)
	for {
		select {
		case <-stopCh:
			return
		case <-initial:
			pendingScoreBackfillPass(wc)
			initial = nil
		case <-t.C:
			pendingScoreBackfillPass(wc)
		}
	}
}

func pendingScoreBackfillPass(wc *config.WorkerConfig) {
	tasks, err := fetchPendingScoreTasks(wc)
	if err != nil {
		// Silent on fetch failure: this is a safety net. Operators can
		// still see the underlying problem (if any) in /worker/tasks
		// errors, and transient 5xx shouldn't spam the log every 10 min.
		return
	}
	if len(tasks) == 0 {
		return
	}
	fmt.Printf("[score/backfill] SaaS reports %d run(s) need scoring\n", len(tasks))

	done := 0
	for _, t := range tasks {
		if done >= pendingScoreMaxPerPass {
			fmt.Printf("[score/backfill] %d more deferred to next tick\n", len(tasks)-done)
			return
		}
		if _, seen := backfillAttempted.LoadOrStore(t.RunID, struct{}{}); seen {
			// Already tried this session — skip. A worker restart resets
			// the set if an operator has hand-fixed whatever made the
			// previous attempt fail.
			continue
		}
		scoreNewlyCreatedRun(wc, scoreContext{
			RunID:       t.RunID,
			Platform:    t.Repo.Platform,
			FullName:    t.Repo.FullName,
			BaseBranch:  t.Repo.BaseBranch,
			LocalPath:   t.Repo.LocalPath,
			IssueNumber: t.IssueNumber,
			IssueTitle:  t.IssueTitle,
			IssueBody:   t.IssueBody,
			IssueURL:    t.IssueURL,
		})
		done++
	}
}

// fetchPendingScoreTasks polls GET /api/v1/worker/tasks/pending-score.
// Accepts `{tasks:[...]}` or a bare array. 404 is treated as a nil list —
// SaaS deployments that don't implement the endpoint (older dev envs)
// simply get no backfill, not an error spam.
func fetchPendingScoreTasks(wc *config.WorkerConfig) ([]pendingScoreTask, error) {
	req, err := http.NewRequest(http.MethodGet, wc.SaasURL+"/api/v1/worker/tasks/pending-score", nil)
	if err != nil {
		return nil, err
	}
	setWorkerHeaders(req, wc)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var env pendingScoreEnvelope
	if jerr := json.Unmarshal(body, &env); jerr == nil && env.Tasks != nil {
		return env.Tasks, nil
	}
	var bare []pendingScoreTask
	if jerr := json.Unmarshal(body, &bare); jerr != nil {
		return nil, fmt.Errorf("decode: %w", jerr)
	}
	return bare, nil
}
