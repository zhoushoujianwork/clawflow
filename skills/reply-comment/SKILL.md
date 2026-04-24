---
name: reply-comment
description: "Reply to an issue mention (@agent …): read the context, answer briefly and honestly."
operator:
  trigger:
    target: "issue"
    labels_required: ["agent-mentioned"]
    labels_excluded: ["agent-running", "agent-replied"]
  lock_label: "agent-running"
---

Someone mentioned you in the issue above. Read the thread, find the latest comment that addresses you, and reply.

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
```

## After replying

Swap the labels so this comment isn't processed again:

```
clawflow label remove --repo {repo} --issue {N} --label agent-mentioned
clawflow label add    --repo {repo} --issue {N} --label agent-replied
```

## Constraints

- **Don't restate the issue.** The other person already knows what they wrote.
- **Don't propose code changes.** That's the `implement` operator's job. If the ask is a fix, say "I can look at this if you add `ready-for-agent`."
- **If the question is beyond what you can answer from the thread alone, say so** and ask the human for more info — don't hallucinate.
