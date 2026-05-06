package operator

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// outcomeRE matches a "<!-- clawflow:outcome=<label> --> " line. The runner
// parses these from the operator's stdout to learn which terminal label to
// add. Word chars + hyphens cover the conventions GitHub/GitLab labels use.
// We eat the trailing newline so stripping the marker doesn't leave a blank
// line at the end of the comment.
var outcomeRE = regexp.MustCompile(`[ \t]*<!--\s*clawflow:outcome=([\w./:-]+)\s*-->[ \t]*\n?`)

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
	return label, strings.TrimSpace(cleaned)
}

// VCS is the subset of vcs.Client the runner needs. Kept intentionally
// small so the operator package stays testable in isolation.
type VCS interface {
	AddLabel(repo string, issueNumber int, labels ...string) error
	RemoveLabel(repo string, issueNumber int, labels ...string) error
	PostIssueComment(repo string, issueNumber int, body string) error
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

	// Model is forwarded as `--model <model>` to the claude subprocess.
	// Empty falls back to claude's own default (the user's
	// ~/.claude/settings.json), which is rarely what we want for an
	// operator run — the runner fills this in from credentials.yaml
	// before invoking RunFunc, picking the eval or operator model
	// based on the operator name.
	Model string

	// EventWriter, if non-nil, receives raw stream-json event lines from
	// claude so the dashboard can replay runs post-mortem. Callers typically
	// set it to a file writer pointing at `<run-dir>/events.jsonl`; tests
	// leave it nil.
	EventWriter io.Writer

	// RunFunc executes the claude subprocess. Leave nil to use the real
	// RunClaude; tests inject a fake that returns canned output without
	// spawning a process.
	RunFunc func(ctx context.Context, prompt, workdir string, timeout time.Duration, events io.Writer, model string, systemPrompt ...string) (string, error)
}

// Run executes one operator against one subject and returns the operator's
// final stdout text, the outcome label (empty if none), or an error.
func Run(ctx context.Context, op *Operator, sub *Subject, v VCS, opts RunOptions) (string, string, error) {
	systemPrompt := BuildSystemPrompt(op, opts.Repo)
	userMessage := BuildUserMessage(sub, opts.Repo, opts.Comments)
	runFunc := opts.RunFunc
	if runFunc == nil {
		runFunc = RunClaude
	}
	output, err := runFunc(ctx, userMessage, opts.Workdir, opts.Timeout, opts.EventWriter, opts.Model, systemPrompt)
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

	outcome, body := parseOutcome(trimmed)

	// Guard: if the operator produced output but no outcome marker, the model
	// likely violated the output contract by posting the result via a VCS tool
	// call (e.g. `gh issue comment`) instead of emitting it to stdout. In that
	// case `trimmed` contains only a short summary line with no marker.
	//
	// Posting this summary as a comment would accumulate duplicate meta-comments
	// on every run (the trigger label is still present, so the operator fires
	// again). Instead, log a warning and skip the comment post. The lock label
	// will still be removed by the caller, so the issue is left in a clean state
	// for the next run (which may succeed once the prompt hardening takes effect).
	if outcome == "" {
		fmt.Fprintf(os.Stderr,
			"  ⚠ operator %q stdout has no outcome marker — operator may have self-posted via a tool call; skipping comment post to prevent duplicate accumulation\n",
			op.Name)
		fmt.Fprintf(os.Stderr, "  ⚠ stdout was: %s\n", trimmed)
		return trimmed, "", nil
	}

	if body != "" {
		if err := v.PostIssueComment(opts.Repo, sub.Number, body); err != nil {
			return body, outcome, fmt.Errorf("post result comment: %w", err)
		}
		fmt.Fprintf(os.Stderr, "  ✓ comment posted (%d chars)\n", len(body))
	}

	if outcome != "" {
		if !outcomeAllowed(op, outcome) {
			fmt.Fprintf(os.Stderr,
				"  ⚠ operator %q produced disallowed outcome %q (allowed: %v); skipping label add\n",
				op.Name, outcome, op.Outcomes)
		} else if err := v.AddLabel(opts.Repo, sub.Number, outcome); err != nil {
			return body, outcome, fmt.Errorf("add outcome label %q: %w", outcome, err)
		} else {
			fmt.Fprintf(os.Stderr, "  ✓ outcome label %q added\n", outcome)
			if len(op.Trigger.LabelsRequired) > 0 {
				if err := v.RemoveLabel(opts.Repo, sub.Number, op.Trigger.LabelsRequired...); err != nil {
					fmt.Fprintf(os.Stderr, "  ⚠ trigger label cleanup failed: %v\n", err)
				} else {
					fmt.Fprintf(os.Stderr, "  ✓ trigger labels removed: %v\n", op.Trigger.LabelsRequired)
				}
			}
		}
	}

	return body, outcome, nil
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
