package chat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"
)

// ProjectChatRepo is one row of the member-repo table embedded in the
// project chat system prompt. LocalPath may be empty when the repo
// has no local clone configured — the prompt notes that explicitly so
// Claude knows what it can and can't read directly.
type ProjectChatRepo struct {
	Name      string
	LocalPath string
}

// BuildProjectChatContext assembles the system prompt for
// `clawflow project chat`. The chat is the user's primary surface
// for two distinct things:
//
//  1. Maintaining the project's shared context.md (a living document
//     auto-injected into every per-repo chat). Updates land via a
//     ```context.md fenced block which the tool extracts after the
//     session ends and offers to write back.
//
//  2. Cross-repo project-manager work — issue triage, PR review,
//     label/milestone management — performed via Bash invocations of
//     the `clawflow` CLI. Member repos are mounted via --add-dir so
//     Claude can read code from any of them when triaging.
//
// The prompt establishes the role, lists member repos with paths,
// enumerates allowed tools and CLI commands, and pins the output
// contract for context.md updates.
func BuildProjectChatContext(name string, repos []ProjectChatRepo, contextMD string) string {
	var b strings.Builder

	fmt.Fprintln(&b, "# Project Chat Context")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Project: %s\n", name)
	fmt.Fprintln(&b)

	// Member repo table — Claude needs to know what it can read and
	// which slugs to pass to `clawflow ... --repo <owner/name>`.
	fmt.Fprintln(&b, "## Member repos")
	fmt.Fprintln(&b)
	if len(repos) == 0 {
		fmt.Fprintln(&b, "_(none — ask the user to add repos before triaging issues)_")
	} else {
		for _, r := range repos {
			if r.LocalPath != "" {
				fmt.Fprintf(&b, "- `%s` — local clone at `%s` (readable via Read/Grep/Glob)\n", r.Name, r.LocalPath)
			} else {
				fmt.Fprintf(&b, "- `%s` — no local_path configured (only VCS metadata via clawflow CLI)\n", r.Name)
			}
		}
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, "## Current context.md")
	fmt.Fprintln(&b)
	if strings.TrimSpace(contextMD) == "" {
		fmt.Fprintln(&b, "_(empty — this is a new project; treat it as an opportunity to draft the first version with the user)_")
	} else {
		fmt.Fprintln(&b, contextMD)
	}

	fmt.Fprintln(&b, "---")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, `## Your role

You are the **project manager AI** for this project. You help the user with
two complementary jobs:

1. **Maintain context.md** — the shared document above. Edit it as the
   architecture, conventions, or status evolve.
2. **Coordinate work across the member repos** — triage and create
   issues, review PRs, manage labels, audit code across repos for
   consistency, surface duplicated work, etc.

Treat yourself as a peer to the user with full read access to every
member repo's working tree. You can grep across all of them in one
session — that's the value of project-level chat over per-repo chat.

## Working directory

You start in the project's metadata dir
(` + "`~/.clawflow/projects/<this-project>/`" + `). It contains
` + "`project.yaml`" + ` (member repo list, canonical) and ` + "`context.md`" + `
(this document). Member repo source trees are mounted via
` + "`--add-dir`" + ` — read them by their absolute paths listed above.
This cwd intentionally has no git history, so plain ` + "`git status`" + `
won't show anything; use ` + "`git -C <member-path>`" + ` when you need
git commands against a specific member repo.

## Available tools

- **Read / Grep / Glob** — inspect any file in any member repo's
  local clone listed above, or in the project metadata dir.
- **Edit / Write / NotebookEdit** — you have full write access in
  member repo working trees and in the project metadata dir. Use
  judgment: show the proposed diff or edit plan before substantial
  changes, especially across repos. Trivial fixes (typos, comment
  cleanup) you can apply directly.
- **Bash** — clawflow CLI calls (see below) and any shell work the
  user asks for. Avoid destructive defaults; confirm before rm,
  mv across repos, ` + "`git push`" + `, branch deletions, etc.
- **WebFetch** — for issue/PR refs, docs, external context.

## clawflow CLI cheatsheet

Use Bash to invoke these. Never use ` + "`gh`" + ` — clawflow wraps both
GitHub and GitLab uniformly.

### Issues
- ` + "`clawflow issue list --repo <owner/name> [--label foo] [--state open]`" + `
- ` + "`clawflow issue view --repo <owner/name> <number>`" + `
- ` + "`clawflow issue create --repo <owner/name> --title \"...\" [--body \"...\"] [--label bug]`" + `
- ` + "`clawflow issue comment --repo <owner/name> <number> --body \"...\"`" + `
- ` + "`clawflow issue close --repo <owner/name> <number>`" + `

### PRs
- ` + "`clawflow pr list --repo <owner/name> [--state open]`" + `
- ` + "`clawflow pr view --repo <owner/name> <number>`" + `
- ` + "`clawflow pr comment --repo <owner/name> <number> --body \"...\"`" + `

### Labels
- ` + "`clawflow label add --repo <owner/name> --issue <n> --label <name>`" + `
- ` + "`clawflow label remove --repo <owner/name> --issue <n> --label <name>`" + `

When the user asks "what's on my plate" or similar, default to listing
open issues across ALL member repos in parallel.

## Behavior rules

- **Confirm before mutations.** Listing/viewing is free. Creating issues,
  posting comments, adding/removing labels, or closing issues all
  require explicit user OK first. Show the exact command you intend
  to run before running it.
- **Cross-repo by default.** When the user mentions "the project," assume
  they mean the union of member repos unless they name one explicitly.
- **Stay grounded.** Cite file paths and issue numbers when making
  claims. If you haven't read the code, say so before recommending.

## Updating context.md

When the user asks you to update context.md, or when the conversation
naturally produces a meaningful change, output the COMPLETE updated
document inside a fenced code block with the ` + "`context.md`" + ` info string:

` + "```" + `context.md
# Project Name
... full document content ...
` + "```" + `

The tool reads the LAST such block from your output after the session
ends and offers it to the user for review + save. Always output the
COMPLETE document, never a partial diff.

If the user is just discussing or triaging issues and not editing the
context, you don't need to output it. The save prompt only fires when
a fenced block is found.`)

	return b.String()
}

// streamJSONMessage is the minimal projection of a claude --output-format
// stream-json assistant message event. We only care about extracting text
// content from assistant messages.
type streamJSONMessage struct {
	Type    string `json:"type"`
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
	// For content_block events
	ContentBlock *contentBlock `json:"content_block,omitempty"`
	// For message events with content array
	Message *messagePayload `json:"message,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type messagePayload struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

// ExtractLastContextMD scans a stream-json output (one JSON object per line)
// and returns the content of the last fenced code block tagged "context.md"
// found in assistant messages. Returns "" if none found.
func ExtractLastContextMD(output string) string {
	// Collect all assistant text from the stream-json output.
	// Claude's stream-json format emits various event types; we look for
	// assistant message text in multiple possible shapes.
	var allText strings.Builder

	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var msg streamJSONMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}

		// assistant message with inline content string
		if msg.Role == "assistant" && msg.Content != "" {
			allText.WriteString(msg.Content)
			continue
		}
		// content_block with text
		if msg.ContentBlock != nil && msg.ContentBlock.Type == "text" {
			allText.WriteString(msg.ContentBlock.Text)
			continue
		}
		// message wrapper with content array
		if msg.Message != nil && msg.Message.Role == "assistant" {
			for _, cb := range msg.Message.Content {
				if cb.Type == "text" {
					allText.WriteString(cb.Text)
				}
			}
			continue
		}
	}

	return extractFencedBlock(allText.String(), "context.md")
}

// extractFencedBlock finds the last fenced code block with the given info
// string in text. Returns the content between the fences, or "" if not found.
func extractFencedBlock(text, infoString string) string {
	var last string
	lines := strings.Split(text, "\n")
	inBlock := false
	var blockLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			// Look for opening fence: ```context.md or ``` context.md
			if (strings.HasPrefix(trimmed, "```"+infoString) || strings.HasPrefix(trimmed, "~~~ "+infoString)) &&
				len(trimmed) <= len("```"+infoString)+1 { // allow trailing whitespace
				inBlock = true
				blockLines = nil
				continue
			}
			// Also match with a space: "``` context.md"
			if strings.HasPrefix(trimmed, "``` "+infoString) {
				inBlock = true
				blockLines = nil
				continue
			}
		} else {
			// Look for closing fence
			if trimmed == "```" || trimmed == "~~~" {
				inBlock = false
				last = strings.Join(blockLines, "\n")
				continue
			}
			blockLines = append(blockLines, line)
		}
	}

	return last
}
