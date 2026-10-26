---
name: repair-ci
description: "Repair failing CI on an existing managed GitHub PR using local headless reproduction. Dispatched only by the runner."
operator:
  trigger:
    target: "issue"
    applies_to: "leaf"
    labels_required: ["agent-implemented"]
    labels_excluded: ["agent-failed", "agent-skipped", "agent-running", "tracking", "question"]
  outcomes: ["agent-implemented", "agent-failed"]
---

ClawFlow runs this skill through direct Offload with explicit managed-development delivery. Complete the task directly; do not launch Offload or delegate recursively. Offload profiles own credentials and models.


Repair the existing PR specified in the CI repair context. Your cwd is a fresh
isolated repository detached at the exact expected PR head. Stay in this cwd.
The runner has reserved one of at most three total repair attempts for this PR.

This is local-reproduction-based repair: ClawFlow supplies aggregate CI failure
status, not CI logs. Do not claim to have read remote job logs. Inspect repository
test instructions and CI definitions read-only, then reproduce the failure with
the configured headless test command or the relevant repository headless tests.
If reproduction fails or the cause cannot be established, report the commands,
results, and limitation honestly, then emit agent-failed. Do not guess a fix.

Make the smallest code correction. Preserve test strength. Never skip, delete,
weaken, or change assertions merely to pass. Do not modify CI configuration,
dependencies, release files, or unrelated code. Do not open/control GUI apps,
deploy, release, merge, or push to the base branch. Do not delegate.

Run focused headless checks, inspect the diff, and commit the correction using
the repository's commit conventions. Before pushing, run:

    git ls-remote --exit-code origin refs/heads/<head-branch>

Compare its exact SHA to the expected head SHA in the repair context. If the
branch changed, stop with agent-failed and preserve your evidence. Otherwise:

    git push origin HEAD:refs/heads/<head-branch>

Use a normal push only, never force or force-with-lease. A rejected push is a
failure; do not fetch/rebase/retry onto concurrent work. Do not create a new PR.
Do not call gh or tools that write labels/comments. The runner owns those writes.

Your stdout is the issue comment. Report the existing PR URL, correction,
commands/results, and any limitations. After a successful tested push, finish
with exactly `<!-- clawflow:outcome=agent-implemented -->` so the runner waits
for CI and reuses auto-merge. Otherwise finish with exactly
`<!-- clawflow:outcome=agent-failed -->`. Never claim success without the push.
