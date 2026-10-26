package prompts

// AutomationModel is shared by chat and Pilot so their lifecycle guidance agrees.
func AutomationModel() string {
	return `## ClawFlow automation model

ClawFlow implements eligible open issues autonomously when repository monitoring
is enabled. No classification, evaluation or per-issue approval stage is required.
Project direction and testing instructions supply the implementation context.

### Trigger model

Operators are SKILL.md packages. Custom operators retain their target, required
labels, excluded labels and parent/leaf constraints. User packages override
built-ins by name, except retired pipeline names cannot reactivate old stages.

The built-in implement operator accepts open leaf issues, including unlabeled
ones. Parents, tracking issues and questions do not enter implementation.
Existing implemented, skipped, failed and running states remain exclusions.

### Label vocabulary

| Label | Meaning |
|---|---|
| bug / feat | Descriptive issue categories; no approval gate |
| question | Route to reply-question |
| tracking | Parent coordination; excluded from direct implementation |
| progress-check | Request track-progress for a parent issue |
| agent-mentioned | Request reply-comment |
| agent-running | Existing running-state signal; treat as read-only |
| agent-implemented | Implementation opened a PR; delivery still requires merge |
| agent-replied | Question or mention answered |
| agent-skipped | Work needs information or a prerequisite; read the comment |
| agent-failed | Work failed; inspect run history before retrying |

### Execution lifecycle

1. clawflow run scans enabled, machine-bound repositories for eligible work.
2. The runner acquires a local cross-process issue lock and rechecks eligibility.
3. Project context, issue context and the operator prompt are passed to claude -p.
4. The operator analyzes, implements, tests and repairs, then opens a PR.
5. The runner parses the final outcome marker, posts the cleaned comment and
   applies the outcome label. Generic last-text recovery preserves assistant output.
6. If auto_merge is enabled, the runner checks merge eligibility and configured
   CI requirements. Failed managed GitHub PR checks can enter bounded repair when
   both auto_merge and ci_required are enabled. Repair updates the existing PR.

Pause, cancellation, locks, retry limits, CI requirements, run history and project
settings remain operational controls. Auto-merge is independent of monitoring;
neither setting is enabled by removing retired configuration keys.

### Diagnosing a stuck issue

- Check repository enablement, machine binding, base branch and local clone.
- Read agent-skipped comments for missing information or prerequisites. Do not
  clear skipped or failed states merely to force implementation.
- Read failure history and retry only after the underlying blocker clears.
- Inspect locks and active runs before interfering with work in progress.
- A PR being open is not proof of delivery: inspect CI, repair and merge status.
- Parent progress counts merged delivery and deliberately closed work separately.
  An open skipped issue or an unmerged PR is still pending.

Preserve historical labels and comments. Removing retired stages does not rewrite
the issue tracker or delete earlier runs.`
}
