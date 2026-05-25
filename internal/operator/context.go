package operator

import (
	"fmt"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

// BuildSystemPrompt returns the stable, cacheable portion of the operator
// prompt: the project header (context.md / testing.md) plus the SKILL.md
// body. This content is identical across every issue the same operator
// processes, so passing it via `--system-prompt` lets Anthropic's prompt
// cache hit on subsequent runs of the same operator.
//
// lang is the preferred output language from Settings.Language ("zh", "en",
// or "" for the historical auto-detect behaviour). A non-empty value appends
// a language directive so all operator output (comments, verdicts) uses the
// configured language.
func BuildSystemPrompt(op *Operator, repo string, lang string) string {
	var b strings.Builder

	if header := project.HeaderForRepo(repo); header != "" {
		b.WriteString(header)
	}

	fmt.Fprintf(&b, "# Your Task (Operator: %s)\n\n", op.Name)
	fmt.Fprint(&b, op.Prompt)
	if directive := config.LanguageDirective(lang); directive != "" {
		b.WriteString(directive)
	}
	return b.String()
}

// BuildUserMessage returns the per-issue dynamic context: repo, issue
// number, title, body, labels, and recent comments. This varies on every
// invocation and is passed as the positional user message to `claude -p`.
//
// comments is optional; pass nil to skip the "Recent Comments" section.
func BuildUserMessage(sub *Subject, repo string, comments []string) string {
	var b strings.Builder

	fmt.Fprintln(&b, "# Context")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Repo: %s\n", repo)

	subjType := "Issue"
	if sub.IsPR {
		subjType = "Pull Request"
	}
	fmt.Fprintf(&b, "%s Number: #%d\n", subjType, sub.Number)
	fmt.Fprintf(&b, "%s Title: %s\n", subjType, sub.Title)
	fmt.Fprintf(&b, "Current Labels: %v\n", sub.Labels)
	if sub.HeadBranch != "" {
		fmt.Fprintf(&b, "Head Branch: %s\n", sub.HeadBranch)
	}
	if sub.URL != "" {
		fmt.Fprintf(&b, "URL: %s\n", sub.URL)
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Body")
	fmt.Fprintln(&b)
	if strings.TrimSpace(sub.Body) == "" {
		fmt.Fprintln(&b, "_(empty)_")
	} else {
		fmt.Fprintln(&b, sub.Body)
	}

	if len(comments) > 0 {
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "## Recent Comments")
		fmt.Fprintln(&b)
		for i, c := range comments {
			fmt.Fprintf(&b, "### Comment %d\n\n%s\n\n", i+1, c)
		}
	}

	return b.String()
}

// BuildPrompt constructs the full prompt handed to `claude -p` as a single
// string. Retained for back-compat with callers that don't use the split
// system-prompt / user-message path (e.g. tests, one-off invocations).
// It passes an empty language string (auto-detect), which preserves the
// historical default behaviour.
func BuildPrompt(op *Operator, sub *Subject, repo string, comments []string) string {
	sys := BuildSystemPrompt(op, repo, "")
	usr := BuildUserMessage(sub, repo, comments)
	return usr + "---\n\n" + sys
}
