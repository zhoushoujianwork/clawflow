package operator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Lifecycle stages emitted via RunOptions.StageFunc as Run progresses. These
// are fine-grained, observable phases WITHIN the runner's coarse "running"
// status — they let the dashboard surface intermediate progress instead of a
// frozen start-of-run snapshot (issue #199). Kept as exported consts so the
// caller (cmd/clawflow/commands/run.go) and tests share one source of truth.
const (
	StageClaudeStarted  = "claude-started"
	StageParsingOutcome = "parsing-outcome"
	StagePostingComment = "posting-comment"
	StageApplyingLabel  = "applying-label"
)

// ErrNoOutcomeMarker is returned by Run when claude produced non-empty output
// but the output contained no <!-- clawflow:outcome=… --> marker. This is
// treated as a recoverable failure (not a permanent one) so the circuit
// breaker upstream can count consecutive occurrences and eventually add
// agent-failed to stop the retry loop. It is distinct from ErrRateLimit so
// the caller can handle each case appropriately.
var ErrNoOutcomeMarker = errors.New("operator produced no outcome marker")

// repoURL is the canonical ClawFlow open-source repository URL. Extracted as a
// package-level constant so the promo footer (and any future references) share
// one source of truth instead of hardcoding the literal in multiple places.
const repoURL = "https://github.com/zhoushoujianwork/clawflow"

// promoFooter is appended to the end of every operator-generated comment as a
// low-noise promotion of the ClawFlow open-source repo (issue #290). It uses a
// markdown inline link so the raw URL is hidden behind readable copy. Applied
// centrally in runWriteBack so it covers all comment-producing operators
// without touching each SKILL.md prompt.
const promoFooter = "\n\n---\n\n由 [ClawFlow](" + repoURL + ") 自动生成"

// appendPromoFooter appends the promo footer to a non-empty comment body. It is
// idempotent-safe against re-appending: if the body already ends with the
// footer it is returned unchanged. Empty bodies are returned as-is so the empty
// guard in runWriteBack still skips the comment post.
func appendPromoFooter(body string) string {
	if body == "" {
		return body
	}
	if strings.HasSuffix(body, promoFooter) {
		return body
	}
	return body + promoFooter
}

// outcomeRE matches a "<!-- clawflow:outcome=<label> --> " line. The runner
// parses these from the operator's stdout to learn which terminal label to
// add. Word chars + hyphens cover the conventions GitHub/GitLab labels use.
// We eat the trailing newline so stripping the marker doesn't leave a blank
// line at the end of the comment.
var outcomeRE = regexp.MustCompile(`[ \t]*<!--\s*clawflow:outcome=([\w./:-]+)\s*-->[ \t]*\n?`)

// thinkingRE strips literal <thinking>…</thinking> blocks that some Claude
// variants emit as plain text inside an assistant turn. This is distinct from
// API-level thinking blocks (content[].type == "thinking"), which claude.go
// already filters by content-block type. The (?s) flag makes . match newlines
// so multi-paragraph reasoning blocks are fully removed. Non-greedy .*? means
// multiple blocks in one response are each stripped independently rather than
// one giant match swallowing content between the first <thinking> and the last
// </thinking>. An unclosed tag (model truncation) is left as-is — the regex
// simply won't match and the raw tag will appear in the comment, which is
// preferable to silently swallowing the rest of the output.
var thinkingRE = regexp.MustCompile(`(?s)<thinking>.*?</thinking>\s*`)

// parseOutcome scans `body` for outcome markers, returning the label of the
// LAST marker (so a model that emits multiple drafts has its final pick
// honored) and a copy of `body` with every marker line removed.
//
// Returns ("", body) when no marker is found — preserves back-compat for
// older skills that don't use the marker contract.
func parseOutcome(body string) (label, cleaned string) {
	matches := outcomeRE.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return "", body
	}
	label = matches[len(matches)-1][1]
	cleaned = outcomeRE.ReplaceAllString(body, "")
	cleaned = thinkingRE.ReplaceAllString(cleaned, "")
	return label, strings.TrimSpace(cleaned)
}

// VCS is the subset of vcs.Client the runner needs. Kept intentionally
// small so the operator package stays testable in isolation.
type VCS interface {
	AddLabel(repo string, issueNumber int, labels ...string) error
	RemoveLabel(repo string, issueNumber int, labels ...string) error
	PostIssueComment(repo string, issueNumber int, body string) error
	// PostIssueCommentIdempotent posts a comment with dedup: if a comment
	// carrying the given runID marker already exists it is a no-op. Falls
	// back to a plain PostIssueComment when runID is empty.
	PostIssueCommentIdempotent(repo string, issueNumber int, body, runID string) error
	CloseIssue(repo string, issueNumber int) error
}

// Concurrency model: cross-process locking uses local lockfiles
// (~/.clawflow/locks/) — see snapshot.AcquireLock. The in-process
// per-issue mutex in cmd/clawflow/commands/run.go gates dispatch within
// a single process. Callers running operators outside the standard
// `clawflow run` loop should use snapshot.AcquireLock themselves.

// RunOptions configures a single operator invocation.
type RunOptions struct {
	Repo     string        // full_name, e.g. "owner/repo"
	Workdir  string        // cwd for the claude subprocess
	Timeout  time.Duration // claude subprocess timeout; 0 disables
	Comments []string      // optional comment thread to include in the prompt

	// Role selects which per-provider model slot RunClaude should read
	// when spawning claude ("chat" / "eval" / "operator"). The failover
	// loop resolves the actual model string per provider on each attempt,
	// so different providers can have different per-role model IDs. An
	// empty Role falls back to the operator slot (safest for unknown
	// user-supplied skills).
	Role string

	// EventWriter, if non-nil, receives raw stream-json event lines from
	// claude so the dashboard can replay runs post-mortem. Callers typically
	// set it to a file writer pointing at `<run-dir>/events.jsonl`; tests
	// leave it nil.
	EventWriter io.Writer

	// ResumeContext, if non-empty, is a markdown section describing partial
	// work from a previous interrupted run that exists in the worktree.
	// Injected into the user message so the operator knows to continue
	// rather than start from scratch.
	ResumeContext string

	// Language is the preferred output language from Settings.Language
	// ("zh", "en", or "" for auto). Passed to BuildSystemPrompt so all
	// operator output (comments, verdicts) uses the configured language.
	Language string

	// RunFunc executes the claude subprocess. Leave nil to use the real
	// RunClaude; tests inject a fake that returns canned output without
	// spawning a process.
	RunFunc func(ctx context.Context, prompt, workdir string, timeout time.Duration, events io.Writer, role string, systemPrompt ...string) (string, error)

	// StageFunc, if non-nil, is invoked at each lifecycle transition with a
	// Stage* constant so the caller can persist intermediate progress (e.g.
	// rewrite meta.json + runs.json for the dashboard). It runs synchronously
	// on the runner's goroutine, so callers should keep it cheap and must not
	// block. Nil is the common case for tests and non-dashboard callers.
	StageFunc func(stage string)

	// RunID is a unique identifier for this operator invocation (typically the
	// run directory timestamp slug). When non-empty it is embedded in the
	// result comment as <!-- clawflow:run-id=<RunID> --> so that retried
	// PostIssueComment calls can detect and skip duplicate deliveries.
	RunID string

	// CommentSaveDir, if non-empty, is a directory path where the operator's
	// result comment body will be written as comment.md before the VCS
	// write-back. This ensures the output survives even if all write-back
	// retries are exhausted — a human (or future recovery logic) can read the
	// file and re-post manually.
	CommentSaveDir string
}

// emitStage safely invokes a (possibly nil) stage callback. Centralizing the
// nil-check keeps the call sites in Run/runWriteBack uncluttered.
func emitStage(fn func(string), stage string) {
	if fn != nil {
		fn(stage)
	}
}

// writeBackTimeout is the maximum time allowed for the post-claude VCS
// write-back phase (post comment + apply outcome label + remove trigger labels).
// Without this bound, a stalled GitHub/GitLab API call parks the runner
// indefinitely until an external process kill (e.g. cron's 60-min SIGKILL)
// terminates it, losing the claude output and leaving the issue half-processed.
// See issue #117 for the observed failure mode.
//
// Declared as a var (not const) so tests can override it without spawning a
// real 5-minute wait.
var writeBackTimeout = 5 * time.Minute

// Run executes one operator against one subject and returns the operator's
// final stdout text, the outcome label (empty if none), or an error.
func Run(ctx context.Context, op *Operator, sub *Subject, v VCS, opts RunOptions) (string, string, error) {
	systemPrompt := BuildSystemPrompt(op, opts.Repo, opts.Language)
	userMessage := BuildUserMessage(sub, opts.Repo, opts.Comments)
	if opts.ResumeContext != "" {
		userMessage += "\n---\n\n" + opts.ResumeContext
	}
	runFunc := opts.RunFunc
	if runFunc == nil {
		runFunc = RunClaude
	}
	emitStage(opts.StageFunc, StageClaudeStarted)
	output, err := runFunc(ctx, userMessage, opts.Workdir, opts.Timeout, opts.EventWriter, opts.Role, systemPrompt)
	if err != nil {
		// Failure path: do NOT pollute the issue with a comment. The
		// failure is already recorded in events.jsonl + the dashboard
		// run timeline; the circuit breaker upstream will eventually
		// add `agent-failed` once consecutive failures exceed the
		// configured threshold. Keeping the issue thread clean lets a
		// human (or PM during patrol) make the recovery decision
		// without scrolling through error tracebacks.
		fmt.Fprintf(os.Stderr, "  ✗ operator %q failed: %v\n", op.Name, err)
		return output, "", err
	}

	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return trimmed, "", nil
	}

	emitStage(opts.StageFunc, StageParsingOutcome)
	outcome, body := parseOutcome(trimmed)

	// Guard: if the operator produced output but no outcome marker, the model
	// likely violated the output contract by posting the result via a VCS tool
	// call (e.g. `gh issue comment`) instead of emitting it to stdout. In that
	// case `trimmed` contains only a short summary line with no marker.
	//
	// Posting this summary as a comment would accumulate duplicate meta-comments
	// on every run (the trigger label is still present, so the operator fires
	// again). Instead, return ErrNoOutcomeMarker so the caller records this as
	// a failure and the circuit breaker can count consecutive occurrences.
	// Without this, the issue stays unlabeled and re-fires on every subsequent
	// pass until claude happens to produce a valid marker — potentially looping
	// indefinitely (see issue #143).
	if outcome == "" {
		fmt.Fprintf(os.Stderr,
			"  ⚠ operator %q stdout has no outcome marker — operator may have self-posted via a tool call; recording as failure to prevent infinite retry loop\n",
			op.Name)
		fmt.Fprintf(os.Stderr, "  ⚠ stdout was: %s\n", trimmed)
		return trimmed, "", ErrNoOutcomeMarker
	}

	// Write-back phase: post comment, apply outcome label, remove trigger labels.
	// Run under a deadline so a stalled GitHub/GitLab API call cannot block the
	// runner indefinitely. Without this, a TCP read that never completes parks
	// the process until an external 60-min kill fires, losing the claude output.
	// The goroutine may outlive the timeout if the underlying HTTP call is truly
	// stuck, but the runner returns promptly and ReconcileStaleRuns handles
	// recovery on the next scan.
	writeBackCtx, cancel := context.WithTimeout(ctx, writeBackTimeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- runWriteBack(v, op, sub, opts, body, outcome)
	}()

	// Note: the write-back stages (posting-comment / applying-label) are
	// emitted from inside runWriteBack so they reflect the actual VCS calls.

	select {
	case err := <-done:
		if err != nil {
			return body, outcome, err
		}
	case <-writeBackCtx.Done():
		return body, outcome, fmt.Errorf("write-back timed out after %v (GitHub/GitLab API stall): %w", writeBackTimeout, writeBackCtx.Err())
	}

	return body, outcome, nil
}

// runWriteBack performs the VCS side-effects after claude completes:
// post the result comment, apply the outcome label, remove trigger labels,
// and optionally close the issue. Extracted so it can be run under a timeout
// via a goroutine in Run.
func runWriteBack(v VCS, op *Operator, sub *Subject, opts RunOptions, body, outcome string) error {
	if body != "" {
		// Append the ClawFlow promo footer before persisting/posting so the
		// on-disk comment.md matches what actually lands on the issue, and so
		// every comment-producing operator picks it up centrally (issue #290).
		body = appendPromoFooter(body)

		// Persist the comment body to disk before attempting VCS write-back.
		// If all retries are exhausted the output is not lost — it can be
		// recovered from comment.md in the run directory (issue #278).
		if opts.CommentSaveDir != "" {
			savePath := filepath.Join(opts.CommentSaveDir, "comment.md")
			if err := os.WriteFile(savePath, []byte(body), 0o644); err != nil {
				// Non-fatal: log and continue. Losing the file is bad but
				// should not prevent the VCS write-back attempt.
				fmt.Fprintf(os.Stderr, "  ⚠ could not save comment to disk: %v\n", err)
			}
		}

		emitStage(opts.StageFunc, StagePostingComment)
		if err := v.PostIssueCommentIdempotent(opts.Repo, sub.Number, body, opts.RunID); err != nil {
			// Downgrade a comment-post failure to a WARN instead of aborting
			// write-back. Previously a POST timeout (context deadline exceeded)
			// returned here and the outcome-label write below never ran, so a
			// PR that was already opened by `implement` never got its
			// `agent-implemented` label — the issue stayed `ready-for-agent`
			// and the next scan re-triggered the operator, opening a duplicate
			// PR and burning tokens again (issue #293). The comment body is
			// already persisted to comment.md above and can be recovered, so
			// the outcome label (the thing that actually gates re-triggering)
			// must take priority over the cosmetic comment.
			fmt.Fprintf(os.Stderr, "  ⚠ post result comment failed (continuing to label): %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  ✓ comment posted (%d chars)\n", len(body))
		}
	}

	if outcome != "" {
		emitStage(opts.StageFunc, StageApplyingLabel)
		if !outcomeAllowed(op, outcome) {
			fmt.Fprintf(os.Stderr,
				"  ⚠ operator %q produced disallowed outcome %q (allowed: %v); skipping label add\n",
				op.Name, outcome, op.Outcomes)
		} else if err := v.AddLabel(opts.Repo, sub.Number, outcome); err != nil {
			return fmt.Errorf("add outcome label %q: %w", outcome, err)
		} else {
			fmt.Fprintf(os.Stderr, "  ✓ outcome label %q added\n", outcome)
			// Only remove labels the operator explicitly declares as one-shot
			// flow markers (LabelsConsumed). Persistent classification labels
			// used as triggers (e.g. "bug"/"feat") stay put so type labels and
			// flow-status labels remain independent (issue #292).
			if len(op.Trigger.LabelsConsumed) > 0 {
				if err := v.RemoveLabel(opts.Repo, sub.Number, op.Trigger.LabelsConsumed...); err != nil {
					fmt.Fprintf(os.Stderr, "  ⚠ consumed label cleanup failed: %v\n", err)
				} else {
					fmt.Fprintf(os.Stderr, "  ✓ consumed labels removed: %v\n", op.Trigger.LabelsConsumed)
				}
			}
		}
	}

	// Auto-close: when the outcome is "agent-closed" (e.g. track-progress
	// determined all sub-issues are done), close the issue to complete the
	// lifecycle loop.
	if outcome == "agent-closed" {
		if err := v.CloseIssue(opts.Repo, sub.Number); err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ auto-close failed: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  ✓ issue #%d closed (all sub-issues complete)\n", sub.Number)
		}
	}

	return nil
}

// outcomeAllowed reports whether `label` is in the operator's declared
// Outcomes whitelist. An empty whitelist is treated as "anything goes" for
// back-compat with older skills that don't enumerate outcomes.
func outcomeAllowed(op *Operator, label string) bool {
	if len(op.Outcomes) == 0 {
		return true
	}
	return slices.Contains(op.Outcomes, label)
}
