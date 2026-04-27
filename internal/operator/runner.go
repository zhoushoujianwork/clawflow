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

// RunOptions configures a single operator invocation.
type RunOptions struct {
	Repo     string        // full_name, e.g. "owner/repo"
	Workdir  string        // cwd for the claude subprocess
	Timeout  time.Duration // claude subprocess timeout; 0 disables
	Comments []string      // optional comment thread to include in the prompt

	// EventWriter, if non-nil, receives raw stream-json event lines from
	// claude so the dashboard can replay runs post-mortem. Callers typically
	// set it to a file writer pointing at `<run-dir>/events.jsonl`; tests
	// leave it nil.
	EventWriter io.Writer

	// RunFunc executes the claude subprocess. Leave nil to use the real
	// RunClaude; tests inject a fake that returns canned output without
	// spawning a process.
	RunFunc func(ctx context.Context, prompt, workdir string, timeout time.Duration, events io.Writer) (string, error)
}

// Run executes one operator against one subject and returns the operator's
// final stdout text (or the empty string when the run was skipped).
//
// Flow:
//  1. Skip if the subject already has the lock label.
//  2. Add lock label.
//  3. Build prompt and invoke claude; events are teed to opts.EventWriter.
//  4. On success: post output as a comment.
//     On failure: post a failure summary as a comment.
//  5. Always remove the lock label (best-effort).
//
// Label operations on GitHub/GitLab are atomic server-side, but
// check-then-add is not — two concurrent runners can both see "no lock"
// and proceed in parallel. For a single-user self-hosted workflow this is
// acceptable; multi-machine concurrency needs a stronger lock layered
// on top.
func Run(ctx context.Context, op *Operator, sub *Subject, v VCS, opts RunOptions) (string, error) {
	if sub.HasLabel(op.LockLabel) {
		return "", nil
	}
	if err := v.AddLabel(opts.Repo, sub.Number, op.LockLabel); err != nil {
		return "", fmt.Errorf("add lock label: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  ✓ lock label %q added\n", op.LockLabel)
	defer func() {
		if err := v.RemoveLabel(opts.Repo, sub.Number, op.LockLabel); err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠ lock label %q remove failed: %v\n", op.LockLabel, err)
			return
		}
		fmt.Fprintf(os.Stderr, "  ✓ lock label %q removed\n", op.LockLabel)
	}()

	prompt := BuildPrompt(op, sub, opts.Repo, opts.Comments)
	runFunc := opts.RunFunc
	if runFunc == nil {
		runFunc = RunClaude
	}
	output, err := runFunc(ctx, prompt, opts.Workdir, opts.Timeout, opts.EventWriter)
	if err != nil {
		msg := fmt.Sprintf("⚠️ Operator `%s` failed:\n\n```\n%v\n```", op.Name, err)
		_ = v.PostIssueComment(opts.Repo, sub.Number, msg)
		return output, err
	}

	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return trimmed, nil
	}

	// Pull any outcome label declared by the operator, then strip the
	// marker(s) from the comment body so it doesn't leak into the rendered
	// issue thread.
	outcome, body := parseOutcome(trimmed)

	if body != "" {
		if err := v.PostIssueComment(opts.Repo, sub.Number, body); err != nil {
			return body, fmt.Errorf("post result comment: %w", err)
		}
		fmt.Fprintf(os.Stderr, "  ✓ comment posted (%d chars)\n", len(body))
	}

	if outcome != "" {
		if !outcomeAllowed(op, outcome) {
			// Operator emitted a label it didn't declare. Skip the add
			// rather than silently mutate state in unexpected ways. Logged
			// to stderr so the run output surfaces the misuse.
			fmt.Fprintf(os.Stderr,
				"  ⚠ operator %q produced disallowed outcome %q (allowed: %v); skipping label add\n",
				op.Name, outcome, op.Outcomes)
		} else if err := v.AddLabel(opts.Repo, sub.Number, outcome); err != nil {
			return body, fmt.Errorf("add outcome label %q: %w", outcome, err)
		} else {
			fmt.Fprintf(os.Stderr, "  ✓ outcome label %q added\n", outcome)
		}
	}

	return body, nil
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
