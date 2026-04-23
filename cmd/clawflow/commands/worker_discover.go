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
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
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

// evaluateLookbackDays bounds how far back we'll auto-evaluate unlabeled
// issues. Keeps the first pass on a fresh install from enqueueing 500 old
// issues and blowing through credits.
const evaluateLookbackDays = 7

// evaluateMaxPerPass caps how many fresh (unlabeled) issues we push per
// repo per pass. Hard stop against runaway cost; next pass (90s later)
// will pick up the rest.
const evaluateMaxPerPass = 10

// Terminal labels — a repo already carrying any of these is considered
// already handled by an agent run and shouldn't auto-evaluate again.
// Mirrors SKILL.md's `to_evaluate` filter ("no agent labels").
var terminalLabels = []string{
	"agent-evaluated",
	"agent-failed",
	"agent-skipped",
	"blocked",
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
		if repo.Platform != "gitlab" {
			continue // GitHub path stays on webhooks — no private-network story there
		}
		if repo.BaseURL == "" {
			continue // gitlab.com has reliable webhook delivery; skip polling
		}
		tok := creds.GitLabToken
		if tok == "" {
			// No token configured: we can't call the GitLab API. Log once per
			// pass and move on — CLI user will see the hint in worker.log.
			fmt.Println("[discover] skipped: no GitLab PAT in credentials.yaml")
			return
		}
		label := defaultTriggerLabel
		if repo.Labels != nil {
			if v, ok := repo.Labels["trigger"]; ok && v != "" {
				label = v
			}
		}
		// Channel A (execute): issues the user labeled `ready-for-agent`.
		// Pushed unconditionally — agent runs evaluation first if needed,
		// execution after. SaaS enqueue is idempotent on (repo, issue).
		if issues, err := fetchOpenLabeledIssues(repo.BaseURL, tok, fullName, label); err != nil {
			fmt.Printf("[discover] %s (execute): %v\n", fullName, err)
		} else {
			for _, is := range issues {
				ack, err := pushDiscoveredIssue(wc, ws, "gitlab", fullName, is.IID, is.Title)
				if err != nil {
					fmt.Printf("[discover] %s #%d: push failed: %v\n", fullName, is.IID, err)
					continue
				}
				maybeScoreAfterDiscover(wc, ack, repo, "gitlab", fullName, is.IID, is.Title)
			}
		}

		// Channel B (evaluate): fresh issues the user hasn't touched yet.
		// SKILL's harvest normally does this by hand; auto-picking recent
		// ones gets us closer to "label-free" UX. Guarded by lookback
		// window + per-pass cap + client-side terminal-label filter so a
		// stale repo can't burn credits on ancient backlog.
		if fresh, err := fetchUnlabeledRecentIssues(repo.BaseURL, tok, fullName, evaluateLookbackDays); err != nil {
			fmt.Printf("[discover] %s (evaluate): %v\n", fullName, err)
		} else {
			pushed := 0
			for _, is := range fresh {
				if pushed >= evaluateMaxPerPass {
					break
				}
				if hasAnyLabel(is.Labels, terminalLabels) {
					continue // already processed — SKILL harvest would skip it too
				}
				ack, err := pushDiscoveredIssue(wc, ws, "gitlab", fullName, is.IID, is.Title)
				if err != nil {
					fmt.Printf("[discover] %s #%d: push failed: %v\n", fullName, is.IID, err)
					continue
				}
				maybeScoreAfterDiscover(wc, ack, repo, "gitlab", fullName, is.IID, is.Title)
				pushed++
			}
		}
	}
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

// fetchUnlabeledRecentIssues lists opened issues created within the last
// `lookbackDays`. Client-side filter to terminal-label-free comes afterwards
// — GitLab 11 doesn't support `not[labels]=…` query arg cleanly.
func fetchUnlabeledRecentIssues(baseURL, token, fullName string, lookbackDays int) ([]gitlabIssue, error) {
	host := strings.TrimRight(baseURL, "/")
	encoded := url.PathEscape(fullName)
	createdAfter := time.Now().AddDate(0, 0, -lookbackDays).Format("2006-01-02")
	target := fmt.Sprintf(
		"%s/api/v4/projects/%s/issues?state=opened&created_after=%s&per_page=50",
		host, encoded, createdAfter,
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
func pushDiscoveredIssue(wc *config.WorkerConfig, ws *wsChannel, platform, fullName string, issueNumber int64, title string) (discoverAck, error) {
	_ = ws // reserved for future use; see comment above re: needing the ack
	body, _ := json.Marshal(map[string]any{
		"platform":     platform,
		"full_name":    fullName,
		"issue_number": issueNumber,
		"issue_title":  title,
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
