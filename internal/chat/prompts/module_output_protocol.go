package prompts

import "fmt"

// OutputProtocol returns the fenced-block output protocol section for
// chats that write back a document (context.md, testing.md, goals.md).
//
// tag is the fenced-block info string the dashboard scrapes, e.g.
// "context.md", "testing.md", or "goals.md".
//
// emitCondition describes when the model should emit the block, e.g.
// "when the user signals the draft is ready" vs "when proposing a change".
func OutputProtocol(tag string, emitCondition string) string {
	return fmt.Sprintf(`## Output protocol

When %s, emit the COMPLETE updated document inside a fenced code block
whose **info string is literally** `+"`%s`"+`:

`+"    ```%s"+`
    <full document content>
`+"    ```"+`

Rules:
- The opening fence MUST be exactly `+"`"+"`"+"`%s`"+"`"+` on its own line.
- Emit the COMPLETE document, not a diff or excerpt.
- Emit at most ONE fenced `+"`%s`"+` block per turn.
- Do NOT emit a fenced block just to "show what's there" — only when
  proposing a change or when the user is ready to save.

The dashboard scrapes the assistant text for fenced blocks and ONLY
extracts a draft when the info string equals `+"`%s`"+`. A bare fence or a
wrong tag like `+"`markdown`"+` / `+"`md`"+` / `+"`text`"+` will NOT show up in the
right-pane Draft preview — meaning the user CANNOT save it.`,
		emitCondition, tag, tag, tag, tag, tag)
}
