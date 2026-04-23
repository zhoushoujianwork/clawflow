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

	res, rerr := runClaude(prompt, nil, workdir, stream, tracePath)
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
}

// postScore fires the final POST /worker/tasks/:run_id/score with the
// CLI's verdict. Usage is attached so SaaS can bill the scoring pass
// against the same run as the eventual execution.
func postScore(wc *config.WorkerConfig, runID string, confidence int, reasoning string, usage *UsageReport) (scoreResponse, error) {
	payload := map[string]interface{}{
		"confidence": confidence,
		"reasoning":  reasoning,
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
	var b strings.Builder
	b.WriteString("You are the ClawFlow feasibility scorer. Decide how confidently an autonomous ")
	b.WriteString("agent can fix this issue end-to-end without human judgment. Consult README, ")
	b.WriteString("CLAUDE.md, recent commits, and any code the issue references.\n\n")
	b.WriteString("Rubric:\n")
	b.WriteString("  70-100 — clear bug reproduction, simple feature, dependency bump, small-scope change\n")
	b.WriteString("  40-69  — vague description, moderate complexity, multiple files\n")
	b.WriteString("   0-39  — architecture refactor, multi-repo change, needs product judgment\n\n")
	fmt.Fprintf(&b, "Repo: %s (platform=%s, base=%s)\n", ctx.FullName, ctx.Platform, ctx.BaseBranch)
	if ctx.IssueURL != "" {
		fmt.Fprintf(&b, "Issue URL: %s\n", ctx.IssueURL)
	}
	fmt.Fprintf(&b, "Issue #%d: %s\n\n", ctx.IssueNumber, ctx.IssueTitle)
	if strings.TrimSpace(ctx.IssueBody) != "" {
		b.WriteString("--- issue body ---\n")
		b.WriteString(ctx.IssueBody)
		b.WriteString("\n--- end body ---\n\n")
	}
	b.WriteString("Finish your reply with a single line in EXACTLY this format (nothing else on that line):\n")
	b.WriteString("SCORE: <integer 0-100> | <one-sentence reasoning>\n")
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
