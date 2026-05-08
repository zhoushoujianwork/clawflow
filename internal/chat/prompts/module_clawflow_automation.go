package prompts

// AutomationModel returns the ClawFlow automation model section — a
// concise conceptual reference that explains how ClawFlow's label-driven
// pipeline works. Injected into every chat system prompt so the AI can
// reason about why issues are stuck, what labels to suggest, and how to
// draft issue bodies that operators can consume.
//
// This is the single source of truth for the automation model. Both
// BuildProjectChatContext and BuildPilotContext call this function —
// never copy-paste the content.
func AutomationModel() string {
	return `## ClawFlow automation model

ClawFlow is a label-driven workflow engine. Understanding this model lets
you explain why issues are stuck, suggest the right labels to unblock them,
and draft issue bodies that operators can actually consume.

### Trigger model

Each operator is defined by a SKILL.md file with a trigger:

    operator fires when:
      target ∈ {issue, pr}
      AND all labels_required are present
      AND no labels_excluded are present

An operator is idempotent: once its outcome label lands, the excluded-label
guard prevents it from firing again on the same issue.

### Label vocabulary

**Trigger labels** — added by humans or the classify operator to route work:

| Label | Meaning |
|---|---|
| ` + "`bug`" + ` | Defect report — triggers ` + "`evaluate-bug`" + ` |
| ` + "`feature`" + ` / ` + "`feat`" + ` | Feature request — triggers ` + "`evaluate-feat`" + ` |
| ` + "`question`" + ` | User question — triggers ` + "`reply-question`" + ` |
| ` + "`ready-for-agent`" + ` | Human approval to implement — triggers ` + "`implement`" + ` |
| ` + "`tracking`" + ` | Epic/tracking issue — triggers ` + "`decompose`" + ` (with ` + "`ready-for-agent`" + `) |
| ` + "`progress-check`" + ` | Request a sub-issue progress check — triggers ` + "`track-progress`" + ` |
| ` + "`agent-mentioned`" + ` | Someone @-mentioned the agent — triggers ` + "`reply-comment`" + ` |

**State labels** — set exclusively by operators, never by humans:

| Label | Meaning |
|---|---|
| ` + "`agent-running`" + ` | Operator is mid-flight — issue is locked, treat as read-only |
| ` + "`agent-evaluated`" + ` | Operator posted an evaluation; awaiting human ` + "`ready-for-agent`" + ` |
| ` + "`agent-implemented`" + ` | PR opened by the implement operator |
| ` + "`agent-decomposed`" + ` | Tracking issue broken into sub-issues |
| ` + "`agent-replied`" + ` | Question or mention answered |
| ` + "`agent-skipped`" + ` | Operator couldn't proceed (ambiguous, missing info) |
| ` + "`agent-failed`" + ` | Operator exited with an error |

### Execution lifecycle

1. ` + "`clawflow run`" + ` scans open issues/PRs for matching operators
2. Runner adds ` + "`agent-running`" + ` lock label
3. Issue context + operator prompt fed to ` + "`claude -p`" + `
4. Operator stdout captured; ` + "`<!-- clawflow:outcome=<label> -->`" + ` marker parsed
5. Cleaned output posted as a comment; outcome label applied; ` + "`agent-running`" + ` removed

Operators communicate **only** through labels and comments — no direct
coupling between operators, no shared state outside the issue tracker.

### Standard pipeline

` + "```" + `
new issue (no labels)
  → classify        → adds bug / feature / question
  → evaluate-bug    → adds agent-evaluated
  → human adds ready-for-agent
  → implement       → opens PR, adds agent-implemented
` + "```" + `

**Diagnosing a stuck issue:**
- No trigger label (` + "`bug`" + `, ` + "`feature`" + `, etc.) → ` + "`classify`" + ` hasn't run or was skipped
- Has ` + "`agent-evaluated`" + ` but no PR → waiting for human to add ` + "`ready-for-agent`" + `
- Has ` + "`agent-skipped`" + ` → operator needs more info; read the comment to see what's missing
- Has ` + "`agent-failed`" + ` → operator errored; remove the label once the blocker clears
- Has ` + "`agent-running`" + ` for >10 min → likely a stale lock; ` + "`clawflow run`" + ` reconciles these
- Has an excluded label → operator already ran; remove it to re-trigger`
}
