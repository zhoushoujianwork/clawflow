// Inline feasibility scoring — invoked by the discover loop immediately
// after SaaS confirms a fresh pipeline_run was created.
//
// Design rationale (vs. the original plan in issue #27):
//   The issue originally spec'd a second, standalone 30s ticker polling
//   GET /worker/tasks/pending-score. That extra round-trip only exists so
//   SaaS can find out "which runs still need scoring" — but the CLI is the
//   one that just created the run (via /worker/discover), so it already
//   knows. Folding scoring into the discover path means:
//     - one code path, one place to debug
//     - no race between "run created" and "scoring loop picks it up"
//     - no SaaS-side pending-score queue to maintain
//     - scoring happens exactly once per fresh discovery (SaaS returns
//       `created=false` on duplicates, so we never re-score)
//
// What's kept here: the prompt, the SCORE:-line parser, and the POST /score
// helper. The discover loop (worker_discover.go) calls scoreNewlyCreatedRun
// right after pushDiscoveredIssue reports `created=true`.
package commands

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/vcs"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

// scoringThresholdDefault mirrors the SaaS-side default of 50 — used only
// for log lines when SaaS omits `threshold` from its response.
const scoringThresholdDefault = 50

// scoreResponse is SaaS's acknowledgment of a score post. `skipped=true`
// means the confidence fell below `threshold` and SaaS has routed the run
// to `agent-skipped` instead of enqueueing it.
type scoreResponse struct {
	Status    string `json:"status"`
	Skipped   bool   `json:"skipped"`
	Threshold int    `json:"threshold"`
}

// scoreContext is the minimum data the scorer needs to build a useful
// prompt for one run. Populated from whatever the discover path has on
// hand (config.yaml for local path, discover response for run_id).
type scoreContext struct {
	RunID       string
	Platform    string
	FullName    string
	BaseBranch  string
	LocalPath   string
	IssueNumber int64
	IssueTitle  string
	IssueBody   string // optional — may be empty from discover
	IssueURL    string // optional
}

// scoreNewlyCreatedRun runs the whole score-and-report cycle for one run
// that `/worker/discover` just reported `created=true` for. Never returns
// an error: every failure mode ends in either a reported 0-score or a
// logged warning, so a bad scoring pass can't block the discover loop.
func scoreNewlyCreatedRun(wc *config.WorkerConfig, ctx scoreContext) {
	start := time.Now()
	fmt.Printf("[score %s] %s#%d — %s\n", ctx.RunID, ctx.FullName, ctx.IssueNumber, ctx.IssueTitle)

	// Best-effort: run Claude inside the checkout so it can read README,
	// CLAUDE.md, recent commits, etc. If the path is missing (repo not
	// cloned yet on this worker), fall back to inheriting cwd — the prompt
	// still carries the issue title/body, enough for a rough score.
	workdir := expandHomeStr(ctx.LocalPath)
	if workdir != "" {
		if _, err := os.Stat(workdir); err != nil {
			fmt.Printf("[score %s] local_path %q inaccessible — scoring without repo context\n", ctx.RunID, workdir)
			workdir = ""
		}
	}

	prompt := buildScoringPrompt(ctx)
	tracePath := filepath.Join("/tmp", "score-"+sanitizeRunID(ctx.RunID)+".log")

	// Stream to SaaS so the dashboard shows a live trace of the scoring
	// run alongside the eventual execution run. stream.Send is a no-op
	// when the WS isn't wired here.
	stream := dialLogStream(wc, ctx.RunID)
	defer stream.Close()

	// Scoring is a cheap rubric-based decision; don't let Claude fan out
	// into codebase exploration. --tools "" disables all built-in tools
	// (grep/read/etc.), forcing the model to answer from the issue body
	// alone; --effort low drops expensive reasoning. Measured impact on
	// one run before vs after: ~$0.36 @ 3m51s → target <$0.01 @ <30s.
	res, rerr := runClaude(prompt, nil, workdir, stream, tracePath,
		"--tools", "", "--effort", "low")
	if rerr != nil {
		fmt.Printf("[score %s] claude exit: %v\n", ctx.RunID, rerr)
	}
	confidence, reasoning, ok := parseScoreOutput(res)
	if !ok {
		reasoning = "scorer did not emit a SCORE: line — defaulting to 0 so SaaS doesn't leave the run unscored"
		confidence = 0
	}
	if confidence < 0 {
		confidence = 0
	} else if confidence > 100 {
		confidence = 100
	}

	var usagePtr *UsageReport
	if res.Usage.HasCost() {
		u := res.Usage
		usagePtr = &u
	}
	resp, err := postScore(wc, ctx.RunID, confidence, reasoning, usagePtr)
	dur := time.Since(start).Round(time.Second)
	if err != nil {
		fmt.Printf("[score %s] post failed in %s: %v\n", ctx.RunID, dur, err)
		return
	}
	verdict := "accepted"
	if resp.Skipped {
		thr := resp.Threshold
		if thr == 0 {
			thr = scoringThresholdDefault
		}
		verdict = fmt.Sprintf("skipped (below %d)", thr)
	}
	fmt.Printf("[score %s] score=%d %s in %s\n", ctx.RunID, confidence, verdict, dur)

	// Commit to the scored-runs audit log FIRST. This is the ground-truth
	// record used by auditScoredAtBoot to rescue runs whose comment didn't
	// land for any reason. Once this line is written, we consider the
	// comment side-effect our responsibility forever, even across restarts.
	appendScoredLog(scoredRec{
		RunID:       ctx.RunID,
		Platform:    ctx.Platform,
		FullName:    ctx.FullName,
		IssueNumber: ctx.IssueNumber,
		IssueTitle:  ctx.IssueTitle,
		Confidence:  confidence,
		Reason:      reasoning,
		Skipped:     resp.Skipped,
		Threshold:   resp.Threshold,
	})

	// Durable queue for the VCS-comment side effect. Stash BEFORE posting
	// so a crash or network failure between here and the post leaves a
	// record the backfill loop can rescue on the next tick. On successful
	// post, postScoringComment clears the record.
	stashPendingComment(pendingCommentRec{
		RunID:       ctx.RunID,
		Platform:    ctx.Platform,
		FullName:    ctx.FullName,
		IssueNumber: ctx.IssueNumber,
		IssueTitle:  ctx.IssueTitle,
		Confidence:  confidence,
		Reason:      reasoning,
		Skipped:     resp.Skipped,
		Threshold:   resp.Threshold,
	})
	postScoringComment(wc, ctx, confidence, reasoning, resp)
}

// postScoringComment writes the scoring verdict back to the VCS issue as
// a comment. Posted on BOTH accepted and skipped outcomes so users see a
// clear signal either way:
//
//   - accepted → "confidence NN/100, add ready-for-agent to execute"
//   - skipped  → "confidence NN/100 below threshold T, needs human judgement"
//
// Silent on:
//   - network / permission failure (best-effort; log only)
//   - unknown platform (future VCS)
//   - empty reasoning (shouldn't happen but guard anyway)
//
// The opening HTML comment marker lets future revisions detect "already
// commented" via ListIssueComments and avoid duplicates; current version
// just posts unconditionally per successful score.
func postScoringComment(wc *config.WorkerConfig, ctx scoreContext, confidence int, reasoning string, resp scoreResponse) {
	body := buildScoringCommentBody(ctx, confidence, reasoning, resp)
	client, err := vcsClientForScoring(wc, ctx)
	if err != nil {
		fmt.Printf("[score %s] comment skipped: %v — queued for backfill\n", ctx.RunID, err)
		// Intentionally leave the stashed pending-comment record in
		// place; the backfill loop retries every 10 min.
		return
	}
	if err := client.PostIssueComment(ctx.FullName, int(ctx.IssueNumber), body); err != nil {
		fmt.Printf("[score %s] comment post failed: %v — queued for backfill\n", ctx.RunID, err)
		return
	}
	fmt.Printf("[score %s] posted verdict comment on %s#%d\n", ctx.RunID, ctx.FullName, ctx.IssueNumber)
	// Tag the issue with its scoring verdict so the next discover pass
	// (and terminalLabels filter inside Channel B) recognises it as
	// already-processed — primary dedup for "no duplicate scoring
	// comments even across unrelated worker restarts".
	applyScoringLabel(client, ctx.FullName, int(ctx.IssueNumber), resp.Skipped)
	// Success → drop the stash entry so the backfill ignores this run.
	clearPendingComment(ctx.RunID)
}

// applyScoringLabel tags an issue with its scoring verdict label. Best-
// effort: a failed label add (missing label def on the repo, permission
// denied, transient 5xx) logs once but doesn't roll back the comment or
// affect return path. The `<!-- clawflow-scoring -->` HTML marker is
// still written by the comment body, so dedup still works if label
// addition silently fails.
//
//   accepted (skipped=false) → agent-evaluated
//   skipped  (skipped=true)  → agent-skipped
//
// Both labels are pre-declared in internal/vcs/interface.go's
// ClawFlowLabels and sit inside the terminalLabels filter the discover
// loop already consults, so adding them here closes the loop that was
// previously a producer-side gap.
func applyScoringLabel(client vcs.Client, repo string, issueNumber int, skipped bool) {
	label := "agent-evaluated"
	if skipped {
		label = "agent-skipped"
	}
	if err := client.AddLabel(repo, issueNumber, label); err != nil {
		fmt.Printf("[score] add %s on %s#%d: %v (dedup still covered by HTML marker)\n",
			label, repo, issueNumber, err)
	}
}

// vcsClientForScoring picks the right VCS client for a scored run. GitHub
// uses getGitHubToken so App-backed repos use SaaS-minted installation
// tokens; GitLab uses the locally-configured PAT (no App story there).
func vcsClientForScoring(wc *config.WorkerConfig, ctx scoreContext) (vcs.Client, error) {
	switch ctx.Platform {
	case "github", "":
		tok, _, err := getGitHubToken(wc, ctx.FullName)
		if err != nil {
			return nil, fmt.Errorf("no github token: %w", err)
		}
		return github.New(tok, ""), nil
	case "gitlab":
		creds, _ := config.LoadCredentials()
		if creds == nil || creds.GitLabToken == "" {
			return nil, fmt.Errorf("no gitlab token in credentials.yaml")
		}
		// BaseURL needs to come from the repo config — scoring context
		// doesn't carry it today. Fall back to Load+lookup so GitLab
		// self-hosted instances route to the right host.
		baseURL := ""
		if cfg, err := config.Load(); err == nil {
			if r, ok := cfg.Repos[ctx.FullName]; ok {
				baseURL = r.BaseURL
			}
		}
		return gitlab.New(creds.GitLabToken, baseURL), nil
	default:
		return nil, fmt.Errorf("unknown platform %q", ctx.Platform)
	}
}

// buildScoringCommentBody renders the markdown the bot posts back on the
// issue. Includes a machine-readable HTML comment marker up top so a
// future "skip if already scored this run" check has something to grep.
func buildScoringCommentBody(ctx scoreContext, confidence int, reasoning string, resp scoreResponse) string {
	reasoning = strings.TrimSpace(reasoning)
	if reasoning == "" {
		reasoning = "_(scorer produced no reasoning line)_"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<!-- clawflow-scoring v1 run=%s -->\n", ctx.RunID)
	b.WriteString("🤖 **ClawFlow feasibility scoring**\n\n")

	if resp.Skipped {
		thr := resp.Threshold
		if thr == 0 {
			thr = scoringThresholdDefault
		}
		fmt.Fprintf(&b, "Confidence: **%d / 100** — below threshold `%d`, skipped.\n\n", confidence, thr)
		b.WriteString("> ")
		b.WriteString(reasoning)
		b.WriteString("\n\n")
		b.WriteString("_This issue likely needs human judgement before an automated fix can be attempted. Adding `ready-for-agent` anyway will queue it, but low-confidence runs often stall._\n")
	} else {
		fmt.Fprintf(&b, "Confidence: **%d / 100** — accepted.\n\n", confidence)
		b.WriteString("> ")
		b.WriteString(reasoning)
		b.WriteString("\n\n")
		b.WriteString("_To have ClawFlow attempt a fix, add the `ready-for-agent` label to this issue. The worker will then claim it and open a pull/merge request._\n")
	}
	return b.String()
}

// postScore fires the final POST /worker/tasks/:run_id/score with the
// CLI's verdict. Usage is attached so SaaS can bill the scoring pass
// against the same run as the eventual execution.
func postScore(wc *config.WorkerConfig, runID string, confidence int, reasoning string, usage *UsageReport) (scoreResponse, error) {
	// SaaS deployed schema uses `reason` (see 422 "missing field `reason`"
	// from end-to-end test on 2026-04-24). Issue #27 prose said "reasoning",
	// but the deployed contract is `reason` — this is the wire field.
	payload := map[string]interface{}{
		"confidence": confidence,
		"reason":     reasoning,
	}
	if usage != nil && usage.HasCost() {
		payload["usage"] = usage
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, wc.SaasURL+"/api/v1/worker/tasks/"+runID+"/score", bytes.NewReader(body))
	if err != nil {
		return scoreResponse{}, err
	}
	setWorkerHeaders(req, wc)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return scoreResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return scoreResponse{}, fmt.Errorf("status %d: %s", resp.StatusCode, b)
	}
	var out scoreResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

func buildScoringPrompt(ctx scoreContext) string {
	// Deliberately terse: scoring runs with --tools "" so Claude has no
	// way to explore anyway; spending tokens inviting it to is wasteful.
	// The model decides from the title + body + repo name alone.
	var b strings.Builder
	b.WriteString("Feasibility scoring for an autonomous coding agent. ")
	b.WriteString("Rate 0-100 how confidently the agent can fix this issue end-to-end without human judgment.\n\n")
	b.WriteString("Rubric:\n")
	b.WriteString("  70-100 — clear bug/repro, simple feature, dep bump, small-scope change\n")
	b.WriteString("  40-69  — vague description, moderate complexity, multiple files\n")
	b.WriteString("   0-39  — architecture refactor, multi-repo, needs product judgment\n\n")
	fmt.Fprintf(&b, "Repo: %s\n", ctx.FullName)
	fmt.Fprintf(&b, "Issue #%d: %s\n", ctx.IssueNumber, ctx.IssueTitle)
	if strings.TrimSpace(ctx.IssueBody) != "" {
		b.WriteString("\n")
		b.WriteString(ctx.IssueBody)
		b.WriteString("\n")
	}
	b.WriteString("\nOutput exactly one line, nothing else on that line:\n")
	b.WriteString("SCORE: <int 0-100> | <one-sentence reasoning>\n")
	return b.String()
}

// scoreLineRe matches the "SCORE: <int> | <reason>" sentinel our prompt
// asks for. Case-insensitive and tolerant of missing pipe so a slightly
// loose model still parses. Capture group 1 = score, 2 = reasoning.
var scoreLineRe = regexp.MustCompile(`(?i)SCORE:\s*(-?\d+)\s*\|?\s*(.*)`)

// parseScoreOutput walks the log entries in reverse — the model's final
// line is what we want. Earlier SCORE mentions (rubric echo, self-
// correction) are noise.
func parseScoreOutput(res PipelineResult) (confidence int, reasoning string, ok bool) {
	for i := len(res.Logs) - 1; i >= 0; i-- {
		msg := res.Logs[i].Message
		lines := strings.Split(msg, "\n")
		for j := len(lines) - 1; j >= 0; j-- {
			m := scoreLineRe.FindStringSubmatch(lines[j])
			if m == nil {
				continue
			}
			n, err := strconv.Atoi(strings.TrimSpace(m[1]))
			if err != nil {
				continue
			}
			return n, strings.TrimSpace(m[2]), true
		}
	}
	return 0, "", false
}
