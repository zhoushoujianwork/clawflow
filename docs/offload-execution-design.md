# Direct Offload execution

ClawFlow delegates bounded engineering work directly to Offload. AGH is not a required execution hop; it may supply user intake or context separately.

ClawFlow owns issue selection, execution locks, worktrees, acceptance, CI gates, merge policy and cleanup. Offload owns profile selection and the native coding-agent process. In an explicitly authorized delivery mode, that agent may edit, test, commit, push and create a PR through the supplied ClawFlow CLI. The final report does not itself prove that the PR is valid or CI passed.

The integration uses the Offload CLI rather than importing its internal Go packages. It loads the operator skill and project instructions explicitly, sends the task through stdin, and retains structured progress and the terminal result. Model credentials stay in Offload configuration. Ordinary Offload tasks retain their existing permission policy.

The first acceptance uses one worker and an isolated repository. It verifies instruction loading, a code change, focused tests, a local Git commit/push, result handling and cancellation. A real remote issue/PR rollout is recorded separately. Existing active runs must not be duplicated or interrupted to manufacture an acceptance result.

Implementation details and capability limits are in the [technical companion](offload-execution-design-detail.md). This design supersedes the requirement to route ClawFlow execution through AGH.

## Configuration

```yaml
settings:
  execution:
    command: offload
    profile: engineering
    role_profiles:
      chat: research
    codex_sandbox: workspace-write
```

The profile is required and owns credentials and default models. Configure it with `offload config`; ClawFlow's old provider settings no longer control execution. `role_profiles` and `codex_sandbox` are optional. Choosing `danger-full-access` is an explicit trusted delivery choice. Execution settings remain machine-local and are excluded from sync. Environment overrides are `CLAWFLOW_OFFLOAD_COMMAND`, `CLAWFLOW_OFFLOAD_PROFILE`, and `CLAWFLOW_OFFLOAD_CODEX_SANDBOX`.

Implement, CI repair, Pilot and explicit issue edit conversations use managed delivery. Auxiliary operators and document generation use read-only execution; tools-disabled and narrower tool restrictions remain explicit. Positive dollar budgets fail before launch with `unsupported_budget`; no budget is silently dropped. Existing budgeted Pilot settings therefore remain blocked until the execution contract supports those caps.

The dashboard shows profile configuration without launching a provider. Headless fixtures and two live local Git delivery checks passed, including ClawFlow's production execution adapter. Real remote PR/CI/merge acceptance and installed-runtime rollout remain separate. See the [acceptance record](offload-live-acceptance-2026-10-26.md).
