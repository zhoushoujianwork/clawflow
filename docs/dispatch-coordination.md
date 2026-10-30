# Development dispatch coordination

Before development starts, ClawFlow compares the issue with active work, linked pull requests and explicit dependencies. Compatible work continues automatically. Waiting work releases execution capacity and is reconsidered when its evidence changes. Questions that change scope wait for the owner; routine coding choices remain autonomous.

An issue can be closed as superseded only with an explicit owner-authored replacement directive and verified replacement evidence. A matching title or an open pull request is never completion evidence. Existing CI, acceptance and merge gates remain authoritative.

Local manual development claims use `clawflow-skip` and an issue comment before editing. This owner-controlled label remains in place during local implementation and review, preventing the scheduler from consuming the same work.

Implementation and validation: [technical companion](dispatch-coordination-detail.md).
