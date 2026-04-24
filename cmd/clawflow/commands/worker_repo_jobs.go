// Repo-level background jobs pushed by SaaS. First use case: label_init.
//
// When a user adds a repo through the SaaS UI (http://clawflow.daboluo.cc
// / dashboard), SaaS has no credentials for the user's VCS — especially
// relevant for private-network GitLab like git.patsnap.com that the SaaS
// host can't even reach. Instead of trying to proxy the InitLabels call
// through the user's worker synchronously, SaaS just enqueues a job in
// the `repo_jobs` table; this loop picks it up on the next tick and runs
// `client.InitLabels(…)` locally using the credentials the CLI already
// has on disk.
//
// Contract: GET /api/v1/worker/repo-jobs → POST /claim → execute →
// POST /complete | /fail. See SaaS clawflow-saas/CLAUDE.md § Endpoints.
package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
)

// repoJobsInterval: 60s. Label init is idempotent and urgent for UX
// (user clicks "add repo" in UI, expects labels to appear soon), so poll
// faster than /sync/config (5 min) but slower than task polling (pollSecs).
const repoJobsInterval = 60 * time.Second

type repoJob struct {
	ID        string    `json:"id"`
	RepoID    string    `json:"repo_id"`
	Kind      string    `json:"kind"`
	Platform  string    `json:"platform"`
	FullName  string    `json:"full_name"`
	Attempts  int       `json:"attempts"`
	CreatedAt time.Time `json:"created_at"`
}

type listRepoJobsResponse struct {
	Jobs []repoJob `json:"jobs"`
}

func repoJobsLoop(wc *config.WorkerConfig, stopCh <-chan struct{}) {
	t := time.NewTicker(repoJobsInterval)
	defer t.Stop()
	// Short warmup so a fresh worker doesn't hammer SaaS before the WS
	// channel / config sync have even connected.
	initial := time.After(25 * time.Second)
	for {
		select {
		case <-stopCh:
			return
		case <-initial:
			runRepoJobsPass(wc)
			initial = nil
		case <-t.C:
			runRepoJobsPass(wc)
		}
	}
}

func runRepoJobsPass(wc *config.WorkerConfig) {
	jobs, err := fetchRepoJobs(wc)
	if err != nil {
		// Transient — next tick retries. Only log so users see repeated
		// connectivity issues without drowning the log on every idle loop.
		fmt.Printf("[repo-jobs] fetch failed: %v\n", err)
		return
	}
	if len(jobs) == 0 {
		return
	}
	fmt.Printf("[repo-jobs] got %d pending job(s)\n", len(jobs))
	for _, job := range jobs {
		if err := processRepoJob(wc, job); err != nil {
			// Per-repo failure is logged and skipped; the row stays in
			// running (until claim lease times out server-side) or has
			// already been /fail-reported.
			fmt.Printf("[repo-jobs] %s %s (%s): %v\n", job.Kind, job.FullName, job.ID, err)
		}
	}
}

func fetchRepoJobs(wc *config.WorkerConfig) ([]repoJob, error) {
	req, err := http.NewRequest(http.MethodGet, wc.SaasURL+"/api/v1/worker/repo-jobs", nil)
	if err != nil {
		return nil, err
	}
	setWorkerHeaders(req, wc)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 404 means the endpoint isn't deployed yet (old SaaS). Treat as
	// "nothing to do" so a CLI built against newer SaaS still runs
	// cleanly against production if this ships first.
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}

	var out listRepoJobsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Jobs, nil
}

func claimRepoJob(wc *config.WorkerConfig, jobID string) error {
	req, err := http.NewRequest(http.MethodPost, wc.SaasURL+"/api/v1/worker/repo-jobs/"+jobID+"/claim", nil)
	if err != nil {
		return err
	}
	setWorkerHeaders(req, wc)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("claim status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func completeRepoJob(wc *config.WorkerConfig, jobID string) error {
	req, err := http.NewRequest(http.MethodPost, wc.SaasURL+"/api/v1/worker/repo-jobs/"+jobID+"/complete", nil)
	if err != nil {
		return err
	}
	setWorkerHeaders(req, wc)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("complete status %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func failRepoJob(wc *config.WorkerConfig, jobID, reason string) error {
	body, _ := json.Marshal(map[string]string{"reason": reason})
	req, err := http.NewRequest(http.MethodPost, wc.SaasURL+"/api/v1/worker/repo-jobs/"+jobID+"/fail", bytes.NewReader(body))
	if err != nil {
		return err
	}
	setWorkerHeaders(req, wc)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// /fail is advisory — we log but don't care about the server response
	// beyond the error so a second failure doesn't mask the first.
	return nil
}

// processRepoJob: claim → run → complete | fail. Caller already logs the
// outer error; here we only return errors that leaked past /fail reporting.
func processRepoJob(wc *config.WorkerConfig, job repoJob) error {
	if err := claimRepoJob(wc, job.ID); err != nil {
		// 409 means someone else claimed it — not our problem.
		return err
	}

	var runErr error
	switch job.Kind {
	case "label_init":
		runErr = runLabelInitJob(job)
	default:
		runErr = fmt.Errorf("unsupported kind %q", job.Kind)
	}

	if runErr != nil {
		// Record the reason so the SaaS operator can see *why* via the
		// admin view (and so the UI can surface it later). Keep the
		// reason short — server truncates to 500 chars regardless.
		_ = failRepoJob(wc, job.ID, runErr.Error())
		return runErr
	}
	return completeRepoJob(wc, job.ID)
}

// runLabelInitJob creates the ClawFlow label set on the given repo using
// the CLI's local credentials. Same call `clawflow repo add` and
// `clawflow label init` make — factored through newVCSClientForRepo so
// the job path picks up per-repo BaseURL / platform overrides from
// worker.yaml the same way.
func runLabelInitJob(job repoJob) error {
	client, _, err := newVCSClientForRepo(job.FullName)
	if err != nil {
		// Local config missing this repo. Happens when /sync/config
		// hasn't pulled it yet; next tick will likely succeed. Report
		// the reason so operators can tell "user hasn't synced" apart
		// from "VCS auth broken".
		return fmt.Errorf("local config lookup: %w", err)
	}
	if err := client.InitLabels(job.FullName, vcs.ClawFlowLabels); err != nil {
		return fmt.Errorf("init labels: %w", err)
	}
	fmt.Printf("[repo-jobs] label_init ok: %s\n", job.FullName)
	return nil
}
