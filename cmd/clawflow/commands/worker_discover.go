// Fallback issue-discovery loop for private VCS instances whose webhook
// deliveries don't reach our SaaS (corp firewalls, self-hosted GitLab on an
// intranet, etc.). The worker periodically polls each configured repo for
// issues carrying the trigger label and forwards any fresh matches to
// `POST {saas_url}/api/v1/worker/discover` — SaaS then creates a pending
// pipeline_run exactly like the webhook path does, and the existing
// task-fetch loop picks it up on its next poll.
//
// This runs in the same worker daemon as the fetch/heartbeat loops; no extra
// process, no extra configuration beyond what the user already has
// (`~/.clawflow/config/config.yaml` + `credentials.yaml`).
package commands

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
)

// discoverInterval bounds how often we hit each GitLab instance for issue
// updates. 90s is fast enough that users feel the trigger is "fairly quick"
// without hammering self-hosted GitLab instances that might be on modest
// hardware. The fetch-tasks loop still runs at its own cadence underneath.
const discoverInterval = 90 * time.Second

// Default trigger label if the repo config omits `labels.trigger`. Must match
// the SaaS-side webhook handler's filter (ready-for-agent).
const defaultTriggerLabel = "ready-for-agent"

type gitlabIssue struct {
	IID    int64    `json:"iid"`
	Title  string   `json:"title"`
	Labels []string `json:"labels"`
}

// evaluateMaxPerPass caps how many issues we push to SaaS per repo per pass
// when `auto_evaluate_all_issues=true`. Hard stop against runaway cost on a
// fresh install where a repo might have hundreds of open issues; next pass
// (90s later) drains another batch, and the session dedup cache keeps us
// from re-POSTing ones SaaS already acknowledged.
const evaluateMaxPerPass = 10

// Terminal labels — a repo already carrying any of these is considered
// already handled by an agent run and shouldn't auto-evaluate again.
// Mirrors SKILL.md's `to_evaluate` filter ("no agent labels"). Note: post
// issue #28, SaaS no longer writes `agent-skipped` to the VCS for
// low-confidence runs — but legacy labels from older versions still live
// on the issues, so the filter stays useful.
var terminalLabels = []string{
	"agent-evaluated",
	"agent-failed",
	"agent-skipped",
	"blocked",
}

// discoverSeen is the in-memory index of (platform, fullName, issue#) tuples
// the CLI has already POSTed to /worker/discover. Advisory only: SaaS remains
// the source of truth for dedup — a miss costs at most one idempotent
// duplicate POST (SaaS returns created=false, the scorer then skips).
//
// Backed by an append-only log at ~/.clawflow/state/discovered.log so the
// dedup survives worker restarts. Without persistence, every restart
// re-POSTs everything to /worker/discover — which normally is harmless
// (SaaS dedups) but combined with SaaS side's own imperfect dedup can
// spawn duplicate evaluate-only runs and therefore duplicate scoring
// comments (observed 2026-04-24 with issue #51-#54 scored twice).
var (
	discoverSeen       sync.Map // map[string]struct{}
	discoverSeenLoaded sync.Once
	discoverSeenWrite  sync.Mutex
)

func discoverSeenKey(platform, fullName string, issueNumber int64) string {
	return fmt.Sprintf("%s:%s#%d", platform, fullName, issueNumber)
}

func discoverSeenPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".clawflow", "state", "discovered.log")
}

// ensureDiscoverSeenLoaded reads the on-disk log once per process and
// hydrates the in-memory sync.Map. Safe to call from any goroutine.
func ensureDiscoverSeenLoaded() {
	discoverSeenLoaded.Do(func() {
		f, err := os.Open(discoverSeenPath())
		if err != nil {
			return // missing file = fresh worker, fine
		}
		defer f.Close()
		s := bufio.NewScanner(f)
		s.Buffer(make([]byte, 64*1024), 1024*1024)
		count := 0
		for s.Scan() {
			key := strings.TrimSpace(s.Text())
			if key == "" {
				continue
			}
			discoverSeen.Store(key, struct{}{})
			count++
		}
		if count > 0 {
			fmt.Printf("[discover] restored %d seen (repo, issue) pairs from %s\n", count, discoverSeenPath())
		}
	})
}

// persistDiscoverSeen appends one key to the on-disk log. Append-only
// keeps writes O(1) and survives a mid-write crash (at worst the
// partial line is silently dropped on next boot by bufio.Scanner).
func persistDiscoverSeen(key string) {
	discoverSeenWrite.Lock()
	defer discoverSeenWrite.Unlock()
	path := discoverSeenPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(key + "\n")
}

// markDiscoverSeen records a (repo, issue) as already POSTed. Persists to
// disk so restarts don't re-trigger scoring on the same (repo, issue).
func markDiscoverSeen(platform, fullName string, issueNumber int64) {
	ensureDiscoverSeenLoaded()
	key := discoverSeenKey(platform, fullName, issueNumber)
	if _, already := discoverSeen.LoadOrStore(key, struct{}{}); already {
		return // no need to re-persist a key we already have on disk
	}
	persistDiscoverSeen(key)
}

// alreadyDiscovered reports whether we've POSTed this (repo, issue) in a
// past or current worker session.
func alreadyDiscovered(platform, fullName string, issueNumber int64) bool {
	ensureDiscoverSeenLoaded()
	_, ok := discoverSeen.Load(discoverSeenKey(platform, fullName, issueNumber))
	return ok
}

func hasAnyLabel(labels []string, needles []string) bool {
	for _, l := range labels {
		for _, n := range needles {
			if l == n {
				return true
			}
		}
	}
	return false
}

// discoverLoop runs for the lifetime of the worker. Single goroutine, no
// parallelism across repos — the per-repo GitLab fetch is short, and
// serializing keeps token rate-limit pressure predictable.
func discoverLoop(wc *config.WorkerConfig, stopCh <-chan struct{}) {
	discoverLoopWithWS(wc, nil, stopCh)
}

func discoverLoopWithWS(wc *config.WorkerConfig, ws *wsChannel, stopCh <-chan struct{}) {
	t := time.NewTicker(discoverInterval)
	defer t.Stop()
	// First pass happens a few seconds after boot so users see their
	// already-labeled issues picked up without waiting a full interval.
	initial := time.After(10 * time.Second)
	for {
		select {
		case <-stopCh:
			return
		case <-initial:
			runDiscoverPass(wc, ws)
			initial = nil // one-shot
		case <-t.C:
			runDiscoverPass(wc, ws)
		}
	}
}

func runDiscoverPass(wc *config.WorkerConfig, ws *wsChannel) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("[discover] config load failed: %v\n", err)
		return
	}
	creds, _ := config.LoadCredentials()

	for fullName, repo := range cfg.EnabledRepos() {
		switch repo.Platform {
		case "gitlab":
			discoverGitLabRepo(wc, ws, creds, fullName, repo)
		case "github", "":
			// "" defaults to github for configs predating the Platform field.
			discoverGitHubRepo(wc, ws, creds, fullName, repo)
		default:
			fmt.Printf("[discover] %s: unknown platform %q — skipping\n", fullName, repo.Platform)
		}
	}
}

// triggerLabel returns the configured "please fix this" label for a repo,
// falling back to the package default. Used to decide execution_requested.
func triggerLabel(repo config.Repo) string {
	if repo.Labels != nil {
		if v, ok := repo.Labels["trigger"]; ok && v != "" {
			return v
		}
	}
	return defaultTriggerLabel
}

// discoverGitLabRepo polls one self-hosted GitLab project and pushes both
// labeled (execute) and unlabeled (evaluate-only) issues per issue #29's
// split. Callers filter by `repo.Platform == "gitlab"`.
func discoverGitLabRepo(wc *config.WorkerConfig, ws *wsChannel, creds *config.Credentials, fullName string, repo config.Repo) {
	if repo.BaseURL == "" {
		return // gitlab.com has reliable webhook delivery; only poll self-hosted instances
	}
	tok := creds.GitLabToken
	if tok == "" {
		// Per-repo skip rather than per-pass abort: a user with mixed
		// GitLab + GitHub repos should still get GitHub discovery even if
		// the GitLab PAT is missing.
		fmt.Printf("[discover] %s: no GitLab PAT — skipping repo\n", fullName)
		return
	}
	label := triggerLabel(repo)

	// Channel A (execute): labeled issues → execution_requested=true.
	// Always runs regardless of auto_evaluate_all_issues — an explicit
	// label is the strongest signal of intent.
	if issues, err := fetchOpenLabeledIssues(repo.BaseURL, tok, fullName, label); err != nil {
		fmt.Printf("[discover] %s (execute): %v\n", fullName, err)
	} else {
		for _, is := range issues {
			if alreadyDiscovered("gitlab", fullName, is.IID) {
				continue
			}
			ack, err := pushDiscoveredIssue(wc, ws, "gitlab", fullName, is.IID, is.Title, true)
			if err != nil {
				fmt.Printf("[discover] %s #%d: push failed: %v\n", fullName, is.IID, err)
				continue
			}
			markDiscoverSeen("gitlab", fullName, is.IID)
			maybeScoreAfterDiscover(wc, ack, repo, "gitlab", fullName, is.IID, is.Title)
		}
	}

	// Channel B (evaluate-only): when the repo opts in, fetch every open
	// issue and enqueue with execution_requested=false. SaaS scores them
	// but won't claim them for a fix run until the user adds the trigger
	// label (which flips execution_requested to true on that run).
	if !repo.AutoEvaluateAllIssues {
		return
	}
	fresh, err := fetchAllOpenIssues(repo.BaseURL, tok, fullName)
	if err != nil {
		fmt.Printf("[discover] %s (evaluate-all): %v\n", fullName, err)
		return
	}
	pushed := 0
	for _, is := range fresh {
		if pushed >= evaluateMaxPerPass {
			break
		}
		if alreadyDiscovered("gitlab", fullName, is.IID) {
			continue
		}
		if hasAnyLabel(is.Labels, terminalLabels) {
			markDiscoverSeen("gitlab", fullName, is.IID)
			continue // pre-existing legacy label → already processed
		}
		// Carries the trigger label? Channel A already handled it with
		// execution_requested=true and marked it seen. We wouldn't reach
		// here, but belt-and-suspenders in case the labeled fetch raced.
		execReq := hasAnyLabel(is.Labels, []string{label})
		ack, err := pushDiscoveredIssue(wc, ws, "gitlab", fullName, is.IID, is.Title, execReq)
		if err != nil {
			fmt.Printf("[discover] %s #%d: push failed: %v\n", fullName, is.IID, err)
			continue
		}
		markDiscoverSeen("gitlab", fullName, is.IID)
		maybeScoreAfterDiscover(wc, ack, repo, "gitlab", fullName, is.IID, is.Title)
		pushed++
	}
}

// discoverGitHubRepo is the GitHub equivalent of discoverGitLabRepo (issue
// #29). Before v0.28 this required a local PAT; now the token is resolved
// via getGitHubToken which prefers a SaaS-minted App installation token
// and falls back to the PAT only when the repo isn't App-backed (issue
// #30). `creds` is accepted for signature symmetry with the GitLab variant
// and kept available in case a future change reads non-GitHub creds here.
func discoverGitHubRepo(wc *config.WorkerConfig, ws *wsChannel, creds *config.Credentials, fullName string, repo config.Repo) {
	_ = creds // see comment above
	tok, _, err := getGitHubToken(wc, fullName)
	if err != nil {
		fmt.Printf("[discover] %s: %v — skipping repo\n", fullName, err)
		return
	}
	gh := github.New(tok, repo.BaseURL) // empty baseURL defaults to api.github.com
	label := triggerLabel(repo)

	// Channel A: labeled for execution.
	if issues, err := gh.ListIssues(fullName, "open", []string{label}); err != nil {
		fmt.Printf("[discover] %s (execute): %v\n", fullName, err)
	} else {
		for _, is := range issues {
			n := int64(is.Number)
			if alreadyDiscovered("github", fullName, n) {
				continue
			}
			ack, err := pushDiscoveredIssue(wc, ws, "github", fullName, n, is.Title, true)
			if err != nil {
				fmt.Printf("[discover] %s #%d: push failed: %v\n", fullName, n, err)
				continue
			}
			markDiscoverSeen("github", fullName, n)
			maybeScoreAfterDiscover(wc, ack, repo, "github", fullName, n, is.Title)
		}
	}

	if !repo.AutoEvaluateAllIssues {
		return
	}

	// Channel B: every open issue (PRs filtered out by the github client).
	fresh, err := gh.ListOpenIssues(fullName)
	if err != nil {
		fmt.Printf("[discover] %s (evaluate-all): %v\n", fullName, err)
		return
	}
	pushed := 0
	for _, is := range fresh {
		if pushed >= evaluateMaxPerPass {
			break
		}
		n := int64(is.Number)
		if alreadyDiscovered("github", fullName, n) {
			continue
		}
		if vcsHasAnyLabel(is, terminalLabels) {
			markDiscoverSeen("github", fullName, n)
			continue
		}
		execReq := is.HasLabel(label)
		ack, err := pushDiscoveredIssue(wc, ws, "github", fullName, n, is.Title, execReq)
		if err != nil {
			fmt.Printf("[discover] %s #%d: push failed: %v\n", fullName, n, err)
			continue
		}
		markDiscoverSeen("github", fullName, n)
		maybeScoreAfterDiscover(wc, ack, repo, "github", fullName, n, is.Title)
		pushed++
	}
}

// vcsHasAnyLabel is the vcs.Issue-shaped counterpart to hasAnyLabel (which
// operates on raw []string from the GitLab API). Kept small and local so
// the two discover branches stay symmetric.
func vcsHasAnyLabel(is vcs.Issue, needles []string) bool {
	for _, n := range needles {
		if is.HasLabel(n) {
			return true
		}
	}
	return false
}

// maybeScoreAfterDiscover fires the inline feasibility-scoring pass
// for the run that pushDiscoveredIssue just enqueued — but only when SaaS
// confirms a fresh run was created. On duplicates (`Created=false`) we
// stay silent: the run was scored on a previous pass, or is already in
// flight. Synchronous so the per-pass budget (evaluateMaxPerPass) caps
// how many Claude invocations one discover tick can trigger.
func maybeScoreAfterDiscover(
	wc *config.WorkerConfig,
	ack discoverAck,
	repo config.Repo,
	platform, fullName string,
	issueNumber int64,
	issueTitle string,
) {
	if !ack.Created || ack.RunID == "" {
		return
	}
	scoreNewlyCreatedRun(wc, scoreContext{
		RunID:       ack.RunID,
		Platform:    platform,
		FullName:    fullName,
		BaseBranch:  repo.BaseBranch,
		LocalPath:   repo.LocalPath,
		IssueNumber: issueNumber,
		IssueTitle:  issueTitle,
	})
}

// fetchAllOpenIssues lists every opened issue on the project, with no label
// filter and no lookback window. Page size is capped at 50 so a giant
// backlog doesn't arrive as a single 500-entry response — the discover loop
// caps how many it POSTs per pass anyway (evaluateMaxPerPass), and the
// session dedup cache keeps the next tick from re-examining the same ones.
// Only called when `auto_evaluate_all_issues=true` for the repo (issue #28).
func fetchAllOpenIssues(baseURL, token, fullName string) ([]gitlabIssue, error) {
	host := strings.TrimRight(baseURL, "/")
	encoded := url.PathEscape(fullName)
	target := fmt.Sprintf(
		"%s/api/v4/projects/%s/issues?state=opened&per_page=50",
		host, encoded,
	)
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitlab returned %d", resp.StatusCode)
	}
	var out []gitlabIssue
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return out, nil
}

// fetchOpenLabeledIssues lists open issues on the given GitLab project that
// carry the trigger label. Project path is URL-encoded to the `%2F` form
// GitLab expects; the label query is comma-separated (single label here).
func fetchOpenLabeledIssues(baseURL, token, fullName, label string) ([]gitlabIssue, error) {
	host := strings.TrimRight(baseURL, "/")
	encoded := url.PathEscape(fullName) // converts / to %2F
	target := fmt.Sprintf(
		"%s/api/v4/projects/%s/issues?labels=%s&state=opened&per_page=50",
		host, encoded, url.QueryEscape(label),
	)
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gitlab returned %d", resp.StatusCode)
	}
	var out []gitlabIssue
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return out, nil
}

// discoverAck is the shape returned by POST /worker/discover. `Created`
// tells us whether this POST was the one that actually enqueued a new
// pipeline_run (vs. a dedupe on an existing one) — the scorer keys off
// this to fire exactly once per issue rather than once per discover pass.
type discoverAck struct {
	RunID   string `json:"run_id"`
	Created bool   `json:"created"`
	Skipped bool   `json:"skipped"`
}

// pushDiscoveredIssue hands an issue to SaaS via HTTP and returns the ack
// so the caller can decide whether to kick off inline scoring. Idempotent
// on the server side — safe to call every pass; duplicates come back with
// `Created=false`. We intentionally bypass the WS fast-path here because
// WS send is fire-and-forget and we need the ack to know when to score.
//
// executionRequested (issue #29) splits the two roles the `ready-for-agent`
// label used to conflate:
//   - true  — the user explicitly labeled this issue for execution; SaaS
//             enqueues it for a fix run once scoring clears the threshold.
//   - false — evaluate-only discovery (repo has auto_evaluate_all_issues
//             on and the issue carries no trigger label). SaaS records the
//             confidence score but won't claim it into a worker until the
//             user later adds the label, which flips execution_requested
//             on that run to true.
func pushDiscoveredIssue(wc *config.WorkerConfig, ws *wsChannel, platform, fullName string, issueNumber int64, title string, executionRequested bool) (discoverAck, error) {
	_ = ws // reserved for future use; see comment above re: needing the ack
	body, _ := json.Marshal(map[string]any{
		"platform":            platform,
		"full_name":           fullName,
		"issue_number":        issueNumber,
		"issue_title":         title,
		"execution_requested": executionRequested,
	})
	req, _ := http.NewRequest(http.MethodPost, wc.SaasURL+"/api/v1/worker/discover", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	setWorkerHeaders(req, wc)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return discoverAck{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := readAll(resp.Body)
		return discoverAck{}, fmt.Errorf("saas returned %d: %s", resp.StatusCode, string(b))
	}
	var ack discoverAck
	_ = json.NewDecoder(resp.Body).Decode(&ack)
	if ack.Created {
		fmt.Printf("[discover] %s #%d → enqueued run %s\n", fullName, issueNumber, ack.RunID)
	}
	return ack, nil
}

// readAll reads up to 4 KiB of a response body for error-context logging.
// Avoids pulling in io.ReadAll to preserve the tight import set.
func readAll(r interface{ Read(p []byte) (int, error) }) ([]byte, error) {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 512)
	for len(buf) < 4096 {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			return buf, nil
		}
	}
	return buf, nil
}
