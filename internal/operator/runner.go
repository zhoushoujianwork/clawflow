package operator

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

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
	defer func() { _ = v.RemoveLabel(opts.Repo, sub.Number, op.LockLabel) }()

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
	if trimmed != "" {
		if err := v.PostIssueComment(opts.Repo, sub.Number, trimmed); err != nil {
			return trimmed, fmt.Errorf("post result comment: %w", err)
		}
	}
	return trimmed, nil
}
