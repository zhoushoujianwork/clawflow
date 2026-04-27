---
name: reply-comment
description: "Reply to an issue mention (@agent …): read the context, answer briefly and honestly."
operator:
  trigger:
    target: "issue"
    labels_required: ["agent-mentioned"]
    labels_excluded: ["agent-running", "agent-replied"]
  lock_label: "agent-running"
  outcomes: ["agent-replied"]
---

Someone mentioned you in the issue above. Read the thread, find the latest comment that addresses you, and reply.

## Output contract (MUST follow)

Your stdout IS the reply comment. ClawFlow posts it verbatim, then applies the outcome label declared by the marker line at the end. Three hard rules:

1. **No tool calls that mutate VCS state.** Do NOT run `clawflow label`, `clawflow issue comment`, `gh`, or any other command that changes labels / comments. ClawFlow owns those side-effects — your job is to produce text only.
2. **End with exactly one outcome marker line:** `<!-- clawflow:outcome=agent-replied -->`. ClawFlow strips this line before posting and uses it to add the `agent-replied` label.
3. **Do NOT add a meta status line** like "Reply posted, labels swapped". Stdout is the reply only — anything else becomes a second visible comment on the issue.

## Task

1. Read the issue body + all comments in the Context block.
2. Find the most recent comment addressed to `@agent` (or equivalent).
3. Give a short, honest, useful reply. If you don't know, say so.

## Output (stdout → becomes the reply comment)

Output the reply directly. No preamble, no code fences.

```
@{username} {your reply}

---
_ClawFlow auto-reply_

<!-- clawflow:outcome=agent-replied -->
```

## Constraints

- **Don't restate the issue.** The other person already knows what they wrote.
- **Don't propose code changes.** That's the `implement` operator's job. If the ask is a fix, say "I can look at this if you add `ready-for-agent`."
- **If the question is beyond what you can answer from the thread alone, say so** and ask the human for more info — don't hallucinate.
- The marker MUST be the last non-empty line of stdout. Do not run any tools after emitting the reply.
