package operator

import (
	"context"
	"fmt"
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
}

// Run executes one operator against one subject.
//
// Flow:
//  1. Skip if the subject already has the lock label (defensive against a
//     previous crash or concurrent runner).
//  2. Add lock label.
//  3. Build prompt and invoke claude.
//  4. On success: post claude's output as a comment.
//     On failure: post a failure summary as a comment.
//  5. Always remove the lock label (best-effort).
//
// Note on concurrency: label operations on GitHub/GitLab are atomic on the
// server side, but "check-then-add" is not. Two concurrent runners can both
// see "no lock" and both add the same label (AddLabel is idempotent) and
// proceed in parallel. For a single-user self-hosted workflow this is
// acceptable; if multi-machine concurrency matters, layer a stronger lock
// (etag / updated_at optimistic check) on top of this.
func Run(ctx context.Context, op *Operator, sub *Subject, v VCS, opts RunOptions) error {
	if sub.HasLabel(op.LockLabel) {
		return nil
	}
	if err := v.AddLabel(opts.Repo, sub.Number, op.LockLabel); err != nil {
		return fmt.Errorf("add lock label: %w", err)
	}
	defer func() { _ = v.RemoveLabel(opts.Repo, sub.Number, op.LockLabel) }()

	prompt := BuildPrompt(op, sub, opts.Repo, opts.Comments)
	output, err := RunClaude(ctx, prompt, opts.Workdir, opts.Timeout)
	if err != nil {
		msg := fmt.Sprintf("⚠️ Operator `%s` failed:\n\n```\n%v\n```", op.Name, err)
		_ = v.PostIssueComment(opts.Repo, sub.Number, msg)
		return err
	}

	// The operator's stdout becomes the comment body. Ops that want richer
	// side effects (create PRs, add specific labels) do them inside the
	// claude subprocess via `clawflow pr create` / `clawflow label add`; the
	// stdout is the final human-readable summary.
	if trimmed := strings.TrimSpace(output); trimmed != "" {
		if err := v.PostIssueComment(opts.Repo, sub.Number, trimmed); err != nil {
			return fmt.Errorf("post result comment: %w", err)
		}
	}
	return nil
}
