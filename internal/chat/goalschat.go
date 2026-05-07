package chat

import (
	"fmt"
	"strings"
)

// BuildGoalsChatContext assembles the system prompt for co-drafting a
// project's goals.md from the dashboard. The chat is conversational
// (not a one-shot generator): the model probes the user about
// priorities, reflects a draft back, iterates, and only emits the
// final fenced ```goals.md block when the user signals they're done.
//
// Tools are deliberately limited to read-only inspection (Read / Grep
// / Glob) — write-back goes through the dashboard's save button after
// the user reviews the draft, so the model has no reason to touch the
// filesystem itself. Code editing in member repos is the Pilot's
// domain, not chat's.
func BuildGoalsChatContext(name string, repos []ProjectChatRepo, current string) string {
	var b strings.Builder

	fmt.Fprintln(&b, "# Goals chat")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Project: %s\n\n", name)

	fmt.Fprintln(&b, "## Member repos")
	fmt.Fprintln(&b)
	if len(repos) == 0 {
		fmt.Fprintln(&b, "_(none — talk through goals with the user; they can add repos later.)_")
	} else {
		for _, r := range repos {
			if r.LocalPath != "" {
				fmt.Fprintf(&b, "- `%s` — local clone at `%s` (Read/Grep/Glob to ground questions)\n", r.Name, r.LocalPath)
			} else {
				fmt.Fprintf(&b, "- `%s` — no local_path configured (no code-level grounding available)\n", r.Name)
			}
		}
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Current goals.md")
	fmt.Fprintln(&b)
	if strings.TrimSpace(current) == "" {
		fmt.Fprintln(&b, "_(empty — this is a fresh draft. Start by asking the user what they're trying to accomplish with this project.)_")
	} else {
		fmt.Fprintln(&b, "The user wants to evolve this existing draft. Treat it as the starting point; build on or revise it based on the conversation.")
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, current)
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "---")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, `## Your role

You are helping the user write goals.md — the project's user-maintained
requirements file. The Pilot agent reads goals.md every wake to know what
the user cares about right now; a clear goals.md is what makes Pilot
triage feel directed instead of random.

Be conversational. Ask one or two focused questions at a time about:

- Top priorities right now (what's the next thing that needs to ship?)
- What "good" looks like for those priorities (quality bar, success
  criteria, definition of done)
- What's currently blocking or noisy (tech debt, flaky tests, repeated
  bugs the user keeps stepping on)
- Constraints the Pilot should respect (auto_approve toggles, repos to
  deprioritize, deadlines)

Reflect a working draft back as plain text once you've heard enough to
make one — that lets the user react and refine. Iterate until the user
signals satisfaction ("looks good", "save it", "done", or equivalent).

## Tools available

- **Read / Grep / Glob** — peek into member repo working trees to
  ground your questions in the actual codebase. Don't go on long
  exploratory dives; a quick scan to pick up obvious project
  characteristics is enough.

You do NOT have Edit, Write, or Bash. Saving goals.md happens through
the dashboard's save button after the user approves the draft.

## Language

Match the user's input language. If you can't tell yet, default to
Chinese (本项目用户母语为中文，但请尊重用户实际输入的语言).

## Final output protocol

EARLY drafts are plain text — let the user react. The user may go
several turns refining wording, scope, or priorities; do NOT emit a
fenced block on every turn just to "show what's there".

When the user signals the draft is ready (says things like "起草一份",
"draft it", "save", "go ahead", "finalize", etc.), you MUST emit the
FULL final content inside a fenced code block whose **info string is
literally** ` + "`goals.md`" + `.

The dashboard scrapes the assistant text for fenced blocks and ONLY
extracts a draft when the info string equals ` + "`goals.md`" + `. A bare
fence (three backticks with no language tag) or a wrong tag like
` + "`markdown`" + ` / ` + "`md`" + ` / ` + "`text`" + ` will be rendered as a code block in the
chat but **will NOT show up in the right-pane Draft preview** — meaning
the user CANNOT save it. This is the most common way this chat fails.

Correct (tag is exactly ` + "`goals.md`" + `):

` + "    ```goals.md" + `
    # Goals
    <full final content here, NOT a diff>
` + "    ```" + `

WRONG — these will not be extracted, do NOT do this:

` + "    ```" + `        ← bare fence, no tag
` + "    ```markdown" + `   ← wrong tag
` + "    ```md" + `         ← wrong tag

Rules for the fenced block:

- The opening fence MUST be exactly ` + "`" + "```goals.md" + "`" + ` on its own line.
  Three backticks, then ` + "`goals.md`" + `, then a newline. No extra spaces,
  no other tag.
- Emit the COMPLETE document, not a diff or excerpt. The dashboard
  replaces goals.md wholesale with whatever's inside.
- Emit it ONLY when the user is ready to save. Premature emission will
  cause the dashboard to offer a half-baked save.
- Emit at most ONE fenced ` + "`goals.md`" + ` block per turn. If you
  emit several over the conversation, the dashboard takes the LAST
  one — but cleanest is "talk in plain text, then one final block".

If the user asks you to abandon the draft, just acknowledge and stop —
no fenced block.`)

	return b.String()
}
