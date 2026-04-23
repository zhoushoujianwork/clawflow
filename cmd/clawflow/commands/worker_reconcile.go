// Stale-PR reconciliation flow (v0.24.0).
//
// When a ClawFlow pipeline_run lands a PR but nobody merges it within the
// SaaS-configured stale window, SaaS flips the run into `reconciling` and
// hands it to a worker for a one-shot review pass:
//
//   GET  /worker/tasks/stale-prs                           — list candidates
//   POST /worker/tasks/:run_id/reconcile/claim             — lock for ~30 min
//   POST /worker/tasks/:run_id/reconcile/report            — final verdict
//
// The worker then:
//   1. fetches + rebases the PR head onto the current base
//   2. runs the repo's test_command (one claude retry per failing step)
//   3. reads PR reviews directly with the user's repo token (not via SaaS)
//   4. honours auto_merge_enabled: if true and everything's green, the CLI
//      merges via PUT /pulls/:n/merge using the repo token; if false, the
//      run is deferred back for human attention.
//
// SaaS Phase 1 unconditionally downgrades action=merge to defer on its side
// (billing / safety), but the real merge has already happened in GitHub by
// the time we report — that's deliberate: the CLI owns the VCS side.
//
// GitLab is skipped: SaaS returns a "phase 1 GitHub-only" error for MR
// introspection and we can't safely reconcile without it.
package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
)

// ── Contract: request/response shapes ─────────────────────────────────────

// reconcileRepo mirrors the `repo` object inside each stale-PR task.
type reconcileRepo struct {
	Platform    string `json:"platform"`
	FullName    string `json:"full_name"`
	BaseBranch  string `json:"base_branch"`
	LocalPath   string `json:"local_path"`
	TestCommand string `json:"test_command"`
}

// reconcileTask is one entry returned by GET /worker/tasks/stale-prs.
type reconcileTask struct {
	RunID              string        `json:"run_id"`
	Repo               reconcileRepo `json:"repo"`
	IssueNumber        int           `json:"issue_number"`
	IssueTitle         string        `json:"issue_title"`
	PRNumber           int           `json:"pr_number"`
	PRURL              string        `json:"pr_url"`
	OriginalFinishedAt string        `json:"original_finished_at"`
	StaleSince         string        `json:"stale_since"`
	AutoMergeEnabled   bool          `json:"auto_merge_enabled"`
}

type staleTasksEnvelope struct {
	Tasks []reconcileTask `json:"tasks"`
}

type reconcileClaimResponse struct {
	Claimed       bool   `json:"claimed"`
	RunID         string `json:"run_id,omitempty"`
	Deadline      string `json:"deadline,omitempty"`
	CurrentStatus string `json:"current_status,omitempty"`
}

// reconcileVCSAction describes what the CLI actually did on the VCS.
// Populated only when action=merge|close; nil/none otherwise.
type reconcileVCSAction struct {
	Type        string `json:"type"` // "merged" | "closed" | "none"
	PerformedAt string `json:"performed_at,omitempty"`
	CommitSHA   string `json:"commit_sha,omitempty"`
}

// reconcileReport is the body of POST /worker/tasks/:run_id/reconcile/report.
type reconcileReport struct {
	Action          string              `json:"action"` // "merge" | "close" | "defer"
	Reason          string              `json:"reason"`
	CurrentBaseSHA  string              `json:"current_base_sha"`
	RebaseResult    string              `json:"rebase_result"`
	TestResult      string              `json:"test_result"`
	ReviewState     string              `json:"review_state"`
	Usage           *UsageReport        `json:"usage,omitempty"`
	VCSAction       *reconcileVCSAction `json:"vcs_action,omitempty"`
}

// ── Command wiring ───────────────────────────────────────────────────────

func newWorkerReconcileCmd() *cobra.Command {
	var limit int
	var repoFilter string
	c := &cobra.Command{
		Use:   "reconcile",
		Short: "Run one stale-PR reconciliation pass now",
		Long: `Pulls the current stale-PR queue from SaaS and runs the decision flow
on each claimable run exactly once, then exits. Called automatically on a
15-minute ticker by 'worker start'; this command is for manual triggers
and debugging.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			wc, err := config.LoadWorkerConfig()
			if err != nil {
				return fmt.Errorf("load worker config: %w", err)
			}
			if wc.SaasURL == "" || wc.WorkerToken == "" {
				return fmt.Errorf("worker not configured — run: clawflow login")
			}
			return reconcileOnce(wc, limit, repoFilter)
		},
		Example: "  clawflow worker reconcile\n  clawflow worker reconcile --limit 5 --repo owner/name",
	}
	c.Flags().IntVar(&limit, "limit", 0, "Max tasks to pull from SaaS (0 = server default)")
	c.Flags().StringVar(&repoFilter, "repo", "", "Only reconcile this owner/name")
	return c
}

// reconcileOnce is the body of both the manual command and the ticker.
func reconcileOnce(wc *config.WorkerConfig, limit int, repoFilter string) error {
	tasks, err := fetchStalePRs(wc, limit, repoFilter)
	if err != nil {
		return fmt.Errorf("fetch stale PRs: %w", err)
	}
	if len(tasks) == 0 {
		fmt.Println("reconcile: no stale PRs in queue")
		return nil
	}
	fmt.Printf("reconcile: %d candidate(s)\n", len(tasks))
	for _, t := range tasks {
		handleReconcileTask(wc, t)
	}
	return nil
}

// ── HTTP helpers ─────────────────────────────────────────────────────────

func fetchStalePRs(wc *config.WorkerConfig, limit int, repoFilter string) ([]reconcileTask, error) {
	u := wc.SaasURL + "/api/v1/worker/tasks/stale-prs"
	q := make([]string, 0, 2)
	if limit > 0 {
		q = append(q, fmt.Sprintf("limit=%d", limit))
	}
	if repoFilter != "" {
		q = append(q, "repo_full_name="+repoFilter)
	}
	if len(q) > 0 {
		u += "?" + strings.Join(q, "&")
	}
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	setWorkerHeaders(req, wc)
	resp, err := http.DefaultClient.Do(req)
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
	var env staleTasksEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, err
	}
	return env.Tasks, nil
}

func claimReconcile(wc *config.WorkerConfig, runID string) (reconcileClaimResponse, int, error) {
	req, err := http.NewRequest("POST", wc.SaasURL+"/api/v1/worker/tasks/"+runID+"/reconcile/claim", nil)
	if err != nil {
		return reconcileClaimResponse{}, 0, err
	}
	setWorkerHeaders(req, wc)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return reconcileClaimResponse{}, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var parsed reconcileClaimResponse
	_ = json.Unmarshal(body, &parsed)
	return parsed, resp.StatusCode, nil
}

func reportReconcile(wc *config.WorkerConfig, runID string, rep reconcileReport) error {
	body, _ := json.Marshal(rep)
	req, err := http.NewRequest("POST", wc.SaasURL+"/api/v1/worker/tasks/"+runID+"/reconcile/report", bytes.NewReader(body))
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
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("report status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// ── Decision flow ────────────────────────────────────────────────────────

const (
	rebaseClean         = "clean"
	rebaseResolved      = "conflicts_resolved"
	rebaseUnresolvable  = "conflicts_unresolvable"
	rebaseNotAttempted  = "not_attempted"

	testPassed        = "passed"
	testFailed        = "failed"
	testSkipped       = "skipped"
	testNotAttempted  = "not_attempted"

	reviewApproved         = "approved"
	reviewChangesRequested = "changes_requested"
	reviewCommentsOnly     = "comments_only"
	reviewNone             = "no_review"

	actionMerge  = "merge"
	actionClose  = "close"
	actionDefer  = "defer"

	reasonMaxLen = 500
)

// handleReconcileTask is the full lifecycle for one stale PR. Never returns
// an error to the caller — every failure path ends in either a `report`
// call (best-effort) or a logged warning; the next ticker pass will retry.
func handleReconcileTask(wc *config.WorkerConfig, t reconcileTask) {
	start := time.Now()
	fmt.Printf("[reconcile %s] %s#%d (PR #%d) — %s\n",
		t.RunID, t.Repo.FullName, t.IssueNumber, t.PRNumber, t.PRURL)

	if strings.EqualFold(t.Repo.Platform, "gitlab") {
		fmt.Printf("[reconcile %s] skip: gitlab not supported in phase 1\n", t.RunID)
		return
	}

	claim, status, err := claimReconcile(wc, t.RunID)
	switch {
	case err != nil:
		fmt.Printf("[reconcile %s] claim error: %v\n", t.RunID, err)
		return
	case status == http.StatusConflict, !claim.Claimed:
		fmt.Printf("[reconcile %s] skip: claim refused (status=%d current=%q)\n",
			t.RunID, status, claim.CurrentStatus)
		return
	case status != http.StatusOK:
		fmt.Printf("[reconcile %s] claim unexpected status %d\n", t.RunID, status)
		return
	}
	fmt.Printf("[reconcile %s] claimed until %s\n", t.RunID, claim.Deadline)

	stream := dialLogStream(wc, t.RunID)
	defer stream.Close()

	report := runReconcileFlow(wc, t, stream)
	report.Reason = clampReason(report.Reason)

	// Apply the close/merge contract guards locally before sending — it saves
	// a SaaS 400 round-trip when we forgot to set test_result.
	if report.Action == actionClose &&
		(report.TestResult == testSkipped || report.TestResult == testNotAttempted) {
		fmt.Printf("[reconcile %s] guard: action=close but test_result=%q — downgrading to defer\n",
			t.RunID, report.TestResult)
		report.Action = actionDefer
		report.Reason = clampReason("guard_downgrade: " + report.Reason)
	}

	if err := reportReconcile(wc, t.RunID, report); err != nil {
		fmt.Printf("[reconcile %s] report error: %v\n", t.RunID, err)
		return
	}
	fmt.Printf("[reconcile %s] action=%s rebase=%s test=%s review=%s in %s\n",
		t.RunID, report.Action, report.RebaseResult, report.TestResult, report.ReviewState,
		time.Since(start).Round(time.Second))
}

// runReconcileFlow is the pure decision logic; returns a report ready to send.
// It never calls `report` itself — that's the caller's job. `wc` is needed
// only to mint/fetch the GitHub token via getGitHubToken (issue #30).
func runReconcileFlow(wc *config.WorkerConfig, t reconcileTask, stream *logStreamer) reconcileReport {
	rep := reconcileReport{
		Action:       actionDefer,
		RebaseResult: rebaseNotAttempted,
		TestResult:   testNotAttempted,
		ReviewState:  reviewNone,
	}

	// --- 1. GitHub client — prefer SaaS-minted App installation token
	// (issue #30), fall back to local PAT. `wc` is intentionally visible
	// here because reconcile is always called from worker context.
	tok, _, terr := getGitHubToken(wc, t.Repo.FullName)
	if terr != nil {
		rep.Reason = fmt.Sprintf("no github token: %v", terr)
		return rep
	}
	gh := github.New(tok, "")

	// --- 2. fetch current PR state + head branch --------------------------
	pr, err := gh.GetPR(t.Repo.FullName, t.PRNumber)
	if err != nil {
		rep.Reason = fmt.Sprintf("github get PR: %v", err)
		return rep
	}
	if pr.IsMerged() || pr.State == "closed" {
		rep.Reason = "pr_already_resolved"
		stream.Send("info", "PR already "+pr.State+"/merged — deferring", nil)
		return rep
	}

	// --- 3. resolve local repo path ---------------------------------------
	localPath := expandHomeStr(t.Repo.LocalPath)
	if localPath == "" {
		rep.Reason = "repo.local_path not provided by SaaS"
		return rep
	}
	if _, err := os.Stat(localPath); err != nil {
		rep.Reason = fmt.Sprintf("local_path %q not accessible: %v", localPath, err)
		return rep
	}

	// --- 4. worktree: fetch + check out PR head forced to origin/<head> ---
	worktreePath := filepath.Join("/tmp", "reconcile-"+sanitizeRunID(t.RunID))
	_ = cleanupWorktree(localPath, worktreePath, pr.HeadBranch) // best-effort from a prior crash
	defer cleanupWorktree(localPath, worktreePath, pr.HeadBranch)

	base := t.Repo.BaseBranch
	if base == "" {
		base = "main"
	}

	if err := runGit(localPath, "fetch", "origin", base, pr.HeadBranch); err != nil {
		rep.Reason = fmt.Sprintf("git fetch: %v", err)
		stream.Send("error", rep.Reason, nil)
		return rep
	}

	// Capture current origin/<base> sha — required by the report.
	if sha, err := runGitOutput(localPath, "rev-parse", "origin/"+base); err == nil {
		rep.CurrentBaseSHA = strings.TrimSpace(sha)
	}

	if err := runGit(localPath,
		"worktree", "add", "-B", pr.HeadBranch, worktreePath, "origin/"+pr.HeadBranch); err != nil {
		rep.Reason = fmt.Sprintf("git worktree add: %v", err)
		stream.Send("error", rep.Reason, nil)
		return rep
	}

	// --- 5. rebase onto origin/<base> -------------------------------------
	rebaseErr := runGit(worktreePath, "rebase", "origin/"+base)
	if rebaseErr == nil {
		rep.RebaseResult = rebaseClean
		stream.Send("info", "rebase clean onto origin/"+base, nil)
	} else {
		stream.Send("warn", "rebase conflicts — attempting claude resolve", nil)
		// Let claude try to resolve; it must end with the rebase finished
		// (either `git rebase --continue` or `git rebase --abort`).
		prompt := fmt.Sprintf(
			"You are in a git worktree at %s rebasing onto origin/%s. "+
				"`git rebase` failed with conflicts. Inspect `git status`, resolve every "+
				"conflict while preserving BOTH intent of the PR fix and the newer base, "+
				"stage the files, and run `git rebase --continue` until the rebase is "+
				"complete. If resolution is genuinely impossible, run `git rebase --abort` "+
				"and explain why. Do NOT push, do NOT touch any other branch.",
			worktreePath, base,
		)
		res, rerr := runClaude(prompt, nil, worktreePath, stream,
			filepath.Join("/tmp", "reconcile-claude-"+sanitizeRunID(t.RunID)+".log"))
		mergeUsage(&rep.Usage, res.Usage)
		// Whether claude "succeeded" or not, the real test is: is a rebase still in progress?
		if rerr != nil {
			stream.Send("warn", fmt.Sprintf("claude exited: %v", rerr), nil)
		}
		if inRebase(worktreePath) {
			// Still mid-rebase means claude didn't finish — abort and bail.
			_ = runGit(worktreePath, "rebase", "--abort")
			rep.RebaseResult = rebaseUnresolvable
			rep.Action = actionClose
			rep.TestResult = testSkipped
			rep.Reason = "rebase_conflicts_unresolvable"
			// Guard will downgrade to defer (see handleReconcileTask) because
			// close requires a real test result — that's correct: if we couldn't
			// even rebase, a human should look.
			return rep
		}
		rep.RebaseResult = rebaseResolved
	}

	// --- 6. test_command --------------------------------------------------
	if strings.TrimSpace(t.Repo.TestCommand) == "" {
		rep.TestResult = testSkipped
	} else {
		if runTestCommand(worktreePath, t.Repo.TestCommand, stream) == nil {
			rep.TestResult = testPassed
			stream.Send("info", "tests passed", nil)
		} else {
			stream.Send("warn", "tests failed — attempting claude fix", nil)
			prompt := fmt.Sprintf(
				"You are in git worktree %s. The repo's test command `%s` just failed. "+
					"Diagnose the failure from the preceding output, make the smallest fix "+
					"that keeps the PR's original intent intact, stage and amend it into the "+
					"last commit with `git commit --amend --no-edit`, then re-run the tests "+
					"to confirm. Do not push. If you can't fix it in one pass, stop and say so.",
				worktreePath, t.Repo.TestCommand,
			)
			res, rerr := runClaude(prompt, nil, worktreePath, stream,
				filepath.Join("/tmp", "reconcile-claude-"+sanitizeRunID(t.RunID)+"-tests.log"))
			mergeUsage(&rep.Usage, res.Usage)
			if rerr != nil {
				stream.Send("warn", fmt.Sprintf("claude exited: %v", rerr), nil)
			}
			if runTestCommand(worktreePath, t.Repo.TestCommand, stream) == nil {
				rep.TestResult = testPassed
			} else {
				rep.TestResult = testFailed
				rep.Action = actionClose
				rep.Reason = "tests_failed_after_one_fix_attempt"
				return rep
			}
		}
	}

	// --- 7. reviews ------------------------------------------------------
	reviews, err := gh.ListPRReviews(t.Repo.FullName, t.PRNumber)
	if err != nil {
		// Don't hard-fail — this isn't critical enough to close a PR over.
		stream.Send("warn", fmt.Sprintf("list reviews: %v", err), nil)
		rep.ReviewState = reviewNone
	} else {
		rep.ReviewState = foldReviewState(reviews)
		if hasRecentChangesRequested(reviews, t.OriginalFinishedAt) {
			rep.Action = actionDefer
			rep.Reason = "unresolved_review"
			return rep
		}
	}

	// --- 8. re-poll PR (race: someone merged / closed while we ran) ------
	if pr2, err := gh.GetPR(t.Repo.FullName, t.PRNumber); err == nil {
		if pr2.IsMerged() || pr2.State == "closed" {
			rep.Action = actionDefer
			rep.Reason = "pr_already_resolved"
			return rep
		}
	}

	// --- 9. auto_merge decision ------------------------------------------
	if !t.AutoMergeEnabled {
		rep.Action = actionDefer
		rep.Reason = "auto_merge_disabled_but_ready"
		return rep
	}

	// Push the (possibly rebased) head so the merge operates on the same
	// commits we just tested. Safe: --force-with-lease rejects if someone
	// raced a push onto the head branch.
	if rep.RebaseResult == rebaseResolved {
		if err := runGit(worktreePath, "push", "--force-with-lease", "origin", pr.HeadBranch); err != nil {
			rep.Action = actionDefer
			rep.Reason = fmt.Sprintf("push after rebase failed: %v", err)
			stream.Send("error", rep.Reason, nil)
			return rep
		}
	}

	sha, err := gh.MergePRDetailed(t.Repo.FullName, t.PRNumber)
	if err != nil {
		rep.Action = actionDefer
		rep.Reason = fmt.Sprintf("merge failed: %v", err)
		stream.Send("error", rep.Reason, nil)
		return rep
	}
	rep.Action = actionMerge
	rep.Reason = "auto_merged_after_reconcile"
	rep.VCSAction = &reconcileVCSAction{
		Type:        "merged",
		PerformedAt: time.Now().UTC().Format(time.RFC3339),
		CommitSHA:   sha,
	}
	stream.Send("info", "merged: "+sha, nil)
	return rep
}

// ── Helpers ──────────────────────────────────────────────────────────────

// foldReviewState returns the aggregated review state per spec:
// any-latest-per-user changes_requested > approved > comments_only > no_review.
func foldReviewState(reviews []github.PRReview) string {
	type stamped struct {
		state string
		ts    string
	}
	latest := map[string]stamped{}
	for _, r := range reviews {
		if r.State == "DISMISSED" || r.State == "PENDING" {
			continue
		}
		prev, ok := latest[r.User]
		if !ok || r.SubmittedAt > prev.ts {
			latest[r.User] = stamped{state: r.State, ts: r.SubmittedAt}
		}
	}
	hasApproved, hasCommented := false, false
	for _, s := range latest {
		switch s.state {
		case "CHANGES_REQUESTED":
			return reviewChangesRequested
		case "APPROVED":
			hasApproved = true
		case "COMMENTED":
			hasCommented = true
		}
	}
	switch {
	case hasApproved:
		return reviewApproved
	case hasCommented:
		return reviewCommentsOnly
	default:
		return reviewNone
	}
}

// hasRecentChangesRequested reports whether any CHANGES_REQUESTED review was
// submitted strictly after the agent's original finish time — that's a real
// human signal we must defer for.
func hasRecentChangesRequested(reviews []github.PRReview, originalFinishedAt string) bool {
	if originalFinishedAt == "" {
		// Fallback: any outstanding changes request counts.
		for _, r := range reviews {
			if r.State == "CHANGES_REQUESTED" {
				return true
			}
		}
		return false
	}
	for _, r := range reviews {
		if r.State != "CHANGES_REQUESTED" {
			continue
		}
		if r.SubmittedAt > originalFinishedAt {
			return true
		}
	}
	return false
}

// inRebase checks whether a git rebase is still in progress inside `dir`.
// A clean rebase end removes .git/rebase-merge and .git/rebase-apply; a
// still-present directory means claude bailed mid-way.
func inRebase(dir string) bool {
	gitDirBytes, err := runGitOutput(dir, "rev-parse", "--git-dir")
	if err != nil {
		return false
	}
	gitDir := strings.TrimSpace(gitDirBytes)
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, name)); err == nil {
			return true
		}
	}
	return false
}

// runTestCommand executes the repo's test_command via `sh -c`. Output is
// streamed to the log channel so the SaaS dashboard shows test progress.
func runTestCommand(workdir, cmdline string, stream *logStreamer) error {
	c := exec.Command("sh", "-c", cmdline)
	c.Dir = workdir
	stdout, _ := c.StdoutPipe()
	stderr, _ := c.StderrPipe()
	if err := c.Start(); err != nil {
		return err
	}
	done := make(chan struct{}, 2)
	pump := func(r io.Reader, level string) {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				chunk := strings.TrimRight(string(buf[:n]), "\n")
				if chunk != "" {
					stream.Send(level, oneLine(chunk), nil)
				}
			}
			if err != nil {
				return
			}
		}
	}
	go pump(stdout, "info")
	go pump(stderr, "warn")
	<-done
	<-done
	return c.Wait()
}

// runGitOutput is runGit but returns stdout instead of discarding it.
func runGitOutput(dir string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, out)
	}
	return string(out), nil
}

// cleanupWorktree is best-effort: ignored errors are normal when nothing's there.
func cleanupWorktree(localPath, worktreePath, headBranch string) error {
	_ = runGit(localPath, "worktree", "remove", worktreePath, "--force")
	_ = runGit(localPath, "worktree", "prune")
	if _, err := os.Stat(worktreePath); err == nil {
		_ = os.RemoveAll(worktreePath)
	}
	// Don't delete the head branch from the local repo: after a rebase+push
	// that's the up-to-date state, and deleting it just triggers another
	// fetch next round. -B on the next worktree add will overwrite it safely.
	_ = headBranch
	return nil
}

// sanitizeRunID keeps filesystem paths tidy for UUID-ish ids.
func sanitizeRunID(id string) string {
	out := make([]byte, 0, len(id))
	for i := 0; i < len(id); i++ {
		b := id[i]
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '-', b == '_':
			out = append(out, b)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

func clampReason(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "no_reason_provided"
	}
	if len(s) > reasonMaxLen {
		return s[:reasonMaxLen-1] + "…"
	}
	return s
}

// mergeUsage folds a fresh usage report into an accumulator pointer. Used so
// the reconcile report can bill for both the rebase-fix and the test-fix
// claude retries in a single `usage` field.
func mergeUsage(acc **UsageReport, fresh UsageReport) {
	if !fresh.HasCost() {
		return
	}
	if *acc == nil {
		copied := fresh
		*acc = &copied
		return
	}
	(*acc).InputTokens += fresh.InputTokens
	(*acc).OutputTokens += fresh.OutputTokens
	(*acc).CacheCreationTokens += fresh.CacheCreationTokens
	(*acc).CacheReadTokens += fresh.CacheReadTokens
	if fresh.TotalCostUSD != nil {
		if (*acc).TotalCostUSD == nil {
			c := *fresh.TotalCostUSD
			(*acc).TotalCostUSD = &c
		} else {
			sum := *(*acc).TotalCostUSD + *fresh.TotalCostUSD
			(*acc).TotalCostUSD = &sum
		}
	}
	if (*acc).Model == "" {
		(*acc).Model = fresh.Model
	}
}
