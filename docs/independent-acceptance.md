# Independent acceptance

ClawFlow can require an independent review before merging a managed GitHub PR. Development and acceptance use separate native sessions. For acceptance-enrolled repositories, ClawFlow records a plan from the requirements before implementation without source access, then independently reviews a disposable detached checkout of the submitted commit with command-capable permissions.

This is a local reference implementation for [platform issue #349](https://github.com/zhoushoujianwork/clawflow/issues/349). The broader [platform design](managed-development-design.md) describes the distributed workflow target. [Technical details](independent-acceptance-detail.md) describe evidence and limits.

## Planner 隔离

默认使用 `tools-disabled`：空 scratch 目录、`ToolsDisabled: true` 和 `PermissionMode: "default"`。仅当执行器返回 `unsupported_tools` 错误时，在同一次 attempt 内重试一次 `isolated-readonly`：不传工具限制，使用空 scratch 和 `default` 只读策略（Codex 为无网络只读 sandbox）。其他错误仍终止验收。

实现前的计划保存在私有的 `acceptance-plans/` 数据目录，按仓库、issue、需求摘要（标题和正文）及 planner 策略区分。实现者和 CI、验收、冲突修复均收到同一份清单，须逐项自检并在 PR 正文说明覆盖方式、执行的检查及限制。自检不能作为验收证据。需求未变时，验收复用已保存的计划；正文或 planner 策略改变后重新规划。协调器将计划随任务传给 worker，跨机器执行仍使用同一份计划。未启用 `acceptance_required` 的仓库不增加规划调用。

复用计划时保留 planner 的原始会话和时间，将计划副本绑定当前 PR 契约及验收 attempt。审查仍使用独立的新会话，校验其与开发者及 planner 的身份不同，并独立获取提交对应的证据。旧的逐 attempt 计划仍可读取。

计划持久化后才创建、fetch 和 checkout 审查工作区。计划及记录中的 `plan.planner_isolation`（独立计划文件中为 `planner_isolation`）、status 输出和 harvest pass/non-pass 日志记录实际模式。两种模式均要求 native policy 为 `default`；缺失或未知模式的旧计划不能通过 gate，需重新验收。

`isolated-readonly` 仍有残余风险：只读 sandbox 可以读取其他本地文件，无法由后端强制保证不看源码。隔离依赖空工作目录、延迟创建 checkout，以及禁止读取工作目录外文件或任何仓库的 planner 提示词；它不是完整的文件读取隔离边界。

## 报告格式纠正

reviewer 执行成功且源码检查通过后，若报告解析或校验失败，同一验收 attempt 最多续接原生 reviewer 会话一次。纠正提示包含原始校验错误与报告结构，仅要求返回完整修正 JSON，禁止重新审查或补跑检查。纠正使用相同工作区、profile 和 session，记录新的 native run；源码在纠正前后重新核对。执行失败和 `infrastructure_error` 不进入纠正，第二份报告仍无效则按原有错误路径保存，不产生有效 verdict。

验收记录的可选 `correction` 保存 `validation_error` 和纠正轮的 `reviewer` 原生身份，原始 `reviewer` 字段保留首次审查身份。纠正轮用量通过 `acceptance-review-correction` 执行记录单独保存，不另建验收 attempt；未提供的用量仍为未知。

## Usage

After building this revision, enroll a registered repository:

```bash
clawflow repo set owner/repo --acceptance-required on
clawflow acceptance run --repo owner/repo --pr 123 --timeout 30m
clawflow acceptance status --repo owner/repo --pr 123 --json
```

Enrollment preserves monitoring and automatic-merge settings. The scan sweep never launches a reviewer; it only reports missing evidence. `clawflow acceptance run` stays available for explicit runs.

## Automatic harvest

`clawflow run` 在 operator 执行前调度独立验收。需要开启 `auto_merge` 和 `acceptance_required`，支持整轮扫描或 [targeted run](#targeted-run)。

- 每轮按仓库轮转启动符合条件的 PR 验收，每仓库最多同时运行 `acceptance_max_parallel` 个（默认 3）。通过 `clawflow repo set owner/repo --acceptance-max-parallel 3` 设置；所有后台验收与实现共享本机 `max_concurrent_agents` 限额。
- 本地验收在独立后台进程中运行，调度轮次启动后即返回，不在审查期间持有仓库运行锁。日志见 `~/.clawflow/logs/acceptance-background.log` 和 `run.log`。
- 验收通过后，由后续 sweep 串行合并；每次合并前重新检查当前 head、base 和证据。一个 PR 合并导致 base 变化后，其他 PR 的旧验收不能用于合并。
- 每次完成审查后更新 PR 上由当前认证账号拥有的一条验收评论，包含提交、结论、逐项证据、未解决问题与下一步负责人；旧 attempt 保留在私有审计记录中。发布失败可由后续 harvest 重试。
- 启动失败使 `clawflow run` 返回非零；后台审查中的基础设施错误（包括提供者失败和无效评审报告）记录在日志和验收记录中，不改变已经返回的调度命令退出码，15 分钟后可重试。同一 PR head 最多连续失败 2 次，随后以一条评论和看板原因 `acceptance-infra` 暂停。新 head、issue 正文变化或新 ClawFlow 二进制会重置计数；手动验收命令也遵守此上限。这些错误不消耗产品修复次数。
- A `failed` verdict is repaired on the same PR, see [Repair after a failed verdict](#repair-after-a-failed-verdict). A `blocked` verdict is not: the comment says a human decision is needed.

The PR must be open, non-draft, from the same repository, and reference an open issue with a closing keyword. A successful managed development run must have captured that issue's requirements and trustworthy native execution events. Historical runs missing the requirements snapshot need a new managed development run. The configured acceptance profile must support the existing Offload execution protocol.

The latest completed acceptance attempt must pass for the current requirements, developer identity, head, base and policy. New submissions or changed requirements/configuration need fresh acceptance. A newer failed or cancelled attempt supersedes an older pass. Both automatic merge and explicit `clawflow pr merge` enforce the policy for enrolled repositories, along with required CI and provider protections.

## CI 与验收并行

要求 CI 的仓库默认允许 harvest 在 CI 为 pending 时启动验收；其他候选筛选与重试规则保持不变。可通过以下命令关闭（配置字段为可选布尔值 `acceptance_overlap_ci`）：

```bash
clawflow repo set owner/repo --acceptance-overlap-ci off
```

审查开始前，若仓库要求 CI，且现有检查聚合确认全部检查在合约 head 上成功，审查者会收到 head 和检查身份。`review_mode` 为 `ci-verified`，`ci` 保存该次检查身份；未满足条件时保留兼容字段 `full-suite`，但新证据协议不再据此强制全量执行。是否运行测试取决于需求证据缺口。独立计划阶段不接收 CI 或源码证据。

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

## 按需求验收（证据协议 1）

新合约包含 `evidence_schema: 1`，因此历史自由文本验收记录不能替代新验收。规划阶段从已批准需求提取逐项标准，以需求原文、稳定 ID 和所需最小证明（源码、执行或 live）组成清单，持久化后才读取源码。需求覆盖的语义判断仍由独立 reviewer 负责；结构校验保证计划清单全部有对应结果，不宣称能自动理解需求全文。

每项 `checks` 记录检查内容、证据类型（source/unit/integration/live）、引用、适用提交、观察结果（inspected/exists/passed/failed/unverified）及缺失证明。已有测试在提交 head 上的可信成功结果可以复用，无须重复执行；测试文件存在、源码推断和实际执行结果分别报告。无关测试不能覆盖需求。没有成功 CI 也不默认要求全量测试，只补有意义的证据缺口。保留原有 CI 和合并门禁；此变更不实现 #350 的 workflow/job 来源认证。

`passed` 要求每项证据与提交及所需证明一致且没有未解决缺口。`failed` 要求有结构化、精确提交上的具体缺陷证据，进入既有有界修复。`blocked` 要求明确的缺失证明、人工负责人及下一步行动，不进入修复。工具/基础设施错误按既有重试政策处理。审查建议不扩展需求；仅在批准标准有要求且明确授权的窗口内做 visual/live 验证。

PR 评论只更新当前认证账号拥有的 `clawflow:acceptance-report` 评论；同一 attempt 重发无副作用，较旧或时间顺序冲突的结果被拒绝。发布前重新核对当前合约及最新 attempt；本机使用现有 PR 锁，分布式 worker 由 coordinator 接收 fenced 结果后发布。它不是跨多个独立 coordinator 的远端原子 CAS；继续遵循现有单 authority 部署边界。旧 issue 评论不删除。私有 store 保留每次审查；不会把未经清理的原始 transcript 发布到 PR。
