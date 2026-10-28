# Independent acceptance

ClawFlow can require an independent review before merging a managed GitHub PR. Development and acceptance use separate native sessions. Acceptance first records a plan from the requirements without source access, then reviews a disposable detached checkout of the submitted commit with command-capable permissions.

This is a local reference implementation for [platform issue #349](https://github.com/zhoushoujianwork/clawflow/issues/349). The broader [platform design](managed-development-design.md) describes the distributed workflow target. [Technical details](independent-acceptance-detail.md) describe evidence and limits.

## Planner 隔离

默认使用 `tools-disabled`：空 scratch 目录、`ToolsDisabled: true` 和 `PermissionMode: "default"`。仅当执行器返回 `unsupported_tools` 错误时，在同一次 attempt 内重试一次 `isolated-readonly`：不传工具限制，使用空 scratch 和 `default` 只读策略（Codex 为无网络只读 sandbox）。其他错误仍终止验收。

计划持久化后才创建、fetch 和 checkout 审查工作区。计划及记录中的 `plan.planner_isolation`（独立计划文件中为 `planner_isolation`）、status 输出和 harvest pass/non-pass 日志记录实际模式。两种模式均要求 native policy 为 `default`；缺失或未知模式的旧计划不能通过 gate，需重新验收。

`isolated-readonly` 仍有残余风险：只读 sandbox 可以读取其他本地文件，无法由后端强制保证不看源码。隔离依赖空工作目录、延迟创建 checkout，以及禁止读取工作目录外文件或任何仓库的 planner 提示词；它不是完整的文件读取隔离边界。

## Usage

After building this revision, enroll a registered repository:

```bash
clawflow repo set owner/repo --acceptance-required on
clawflow acceptance run --repo owner/repo --pr 123 --timeout 30m
clawflow acceptance status --repo owner/repo --pr 123 --json
```

Enrollment preserves monitoring and automatic-merge settings. The scan sweep never launches a reviewer; it only reports missing evidence. `clawflow acceptance run` stays available for explicit runs.

## Automatic harvest

`clawflow run` has an acceptance phase after the operator phase. It needs `auto_merge` on and `acceptance_required` on, and it only runs for the whole pass or a [targeted run](#targeted-run).

- Per repo and pass, at most one acceptance runs, for the first open PR that passes the sweep's filters, has successful required CI and has no evidence for its current contract.
- A passing result goes straight to the conditional merge path with the accepted head SHA.
- A failed or blocked verdict posts one comment on the issue with the status and reviewer summary. Later passes find the recorded verdict and stay silent.
- Setup or provider errors post nothing, are logged, and make `clawflow run` exit non-zero. They are retried after 15 minutes, up to 3 attempts per head. After that one comment points to `clawflow acceptance run`.
- A `failed` verdict is repaired on the same PR, see [Repair after a failed verdict](#repair-after-a-failed-verdict). A `blocked` verdict is not: the comment says a human decision is needed.

The PR must be open, non-draft, from the same repository, and reference an open issue with a closing keyword. A successful managed development run must have captured that issue's requirements and trustworthy native execution events. Historical runs missing the requirements snapshot need a new managed development run. The configured acceptance profile must support the existing Offload execution protocol.

The latest completed acceptance attempt must pass for the current requirements, developer identity, head, base and policy. New submissions or changed requirements/configuration need fresh acceptance. A newer failed or cancelled attempt supersedes an older pass. Both automatic merge and explicit `clawflow pr merge` enforce the policy for enrolled repositories, along with required CI and provider protections.

## CI 与验收并行

要求 CI 的仓库默认允许 harvest 在 CI 为 pending 时启动验收；其他候选筛选与重试规则保持不变。可通过以下命令关闭（配置字段为可选布尔值 `acceptance_overlap_ci`）：

```bash
clawflow repo set owner/repo --acceptance-overlap-ci off
```

审查开始前，若仓库要求 CI，且 GitHub 现有检查聚合确认全部检查在合约 head 上成功，审查者会收到 head 和 check-run ID，执行需求针对性测试及源码审查，不再重复全量测试。证据的 `review_mode` 为 `ci-verified`，`ci` 保存该次检查身份；其他情况使用 `full-suite`，仍要求标准或配置的全量测试命令。无法提供精确提交与检查身份的适配器也使用全量模式。独立计划阶段不接收 CI 或源码证据。

看板在并行执行时显示 “acceptance running (CI pending)”。通过验收不会绕过 CI 门禁：同一 head 的 CI 成功后才能合并；CI 后续失败时保留验收记录并进入现有修复路径。新 head 仍需重新验收。这里复用已有 check-run 聚合，不新增分支保护规则发现或 workflow-run 身份证明。

## Repair after a failed verdict

The reviewer never edits. When the latest completed record for the PR's current contract is a reviewer verdict `failed` with evidence, the scan dispatches the existing `repair-ci` operator (development role) against the same PR branch. Conditions: managed same-repository GitHub PR, `acceptance_required` on, and repair budget left.

- The repair prompt carries the reviewer summary and evidence as bounded, redacted, untrusted data, separate from the issue requirements. Requirements stay the source of truth.
- CI repairs and acceptance repairs share one ledger per PR: at most 3 consumed attempts, a 5 minute cooldown, and infrastructure outcomes (capacity, rate limit, launch, transport, timeout before any tool activity) are released instead of consumed.
- When the budget is spent, one comment (deduped by marker) is posted and automatic repair stops. Nothing is merged.
- The repair pushes a normal commit to the existing branch and opens no PR. The new head is a new contract, so CI and a fresh acceptance run follow through the normal harvest rules, with a reviewer session different from the repairing one.
- `clawflow run --repo owner/repo --issue N` includes this repair for the PR closing N.
- Logs: `run/acceptance_repair` (phase start/end, attempt, reason), plus `run/acceptance_repair_skip` and the existing harvest events.

## Targeted run

```bash
clawflow run --repo owner/repo --issue 123
```

`--repo` plus `--issue` is a targeted run. The repo must be configured but may be disabled, so the web auto-run scheduler never picks up its other issues. Only issue 123 is dispatched. The acceptance phase and the auto-merge sweep consider only open PRs whose closing keyword names issue 123, so an earlier PR is accepted and merged on a later targeted pass. All other rules are unchanged: `auto_merge` and `acceptance_required` enrollment, required CI, `bound_machine` and `require_binding`, and the retry limits. An unknown repo fails, and `--repo` without `--issue` still needs an enabled repo. If no operator matches the issue, the run prints `[owner/repo#123] no operator matches this issue (labels: ...)` and exits successfully.

## Scope

This gate reviews the submitted head and rechecks the base. It does not establish tested integration-commit provenance, an atomic merge queue, target-branch verification, distributed fencing, or 10,000-worker capacity. GitHub Actions evidence integration remains [#350](https://github.com/zhoushoujianwork/clawflow/issues/350); durable scheduling remains [#351](https://github.com/zhoushoujianwork/clawflow/issues/351).

Local fixture tests do not establish a live provider or GitHub Actions result. See the [pilot execution record](platform-pilot-report.md) for earlier work and recorded validation.
