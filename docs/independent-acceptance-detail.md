# Independent acceptance: implementation details

[Main usage and scope](independent-acceptance.md) is authoritative.

## Contract and execution

`internal/acceptance` binds repository/PR, task and content generation, head/base, optional integration SHA, requirements, policy digest and developer native run/session identity. Generation is derived from requirements content; it is not a durable monotonic workflow generation.

The producer derives developer identity from the latest relevant successful managed run's bounded native event stream and verifies its captured issue title/body against current requirements. Missing, unsuccessful, ambiguous or legacy provenance blocks acceptance. Native identities come from execution results, never model-authored JSON.

每次 attempt 先使用空规划目录，以 `tools-disabled`（`ToolsDisabled: true`、`default`）执行 planner。仅对 `unsupported_tools` 错误，在同一 attempt 内回退一次到 `isolated-readonly`：不传工具限制，保持 `default` 和空 scratch。Codex 的该策略为无网络只读 sandbox。失败回退不再重试，其他 planner 错误保持失败行为。

Runner 保存并重新读取不可变计划后，才调用 prepare 创建、fetch 和 detached checkout 审查工作区。两种模式均验证 native policy 为 `default`，`planner_isolation` 保存在计划及记录内嵌的 plan 中，也出现在 status 和 harvest pass/non-pass 日志。未知或缺失模式不能通过校验。

只读 sandbox 仍可读取其他本地文件，因此 `isolated-readonly` 的源码隔离依赖工作目录隔离、checkout 创建顺序和提示词，而非后端文件读取限制。提示词明确禁止读取工作目录之外的文件或任何仓库；现存其他本地 checkout 属于残余风险。

Reviewer 使用新的 `managed-development` 会话执行构建和测试。审查前后检查本地 HEAD、额外提交及当前 PR head/base、需求和策略；构建产生的修改或未跟踪文件允许存在。

The model returns exactly `status`, `summary` and `evidence`. Exact schema keys are enforced; unknown, duplicate, conflicting-case or trailing JSON fields fail closed. Native errors, cancellation, developer-session reuse, missing evidence and mismatched contracts cannot pass.

## Storage and merge

Private local storage under the ClawFlow data directory retains per-attempt plans and records, with size bounds, digests and atomic publication. Lookup selects the latest complete outcome for the exact contract, including failure/cancellation. Corruption or ambiguous timestamp ordering blocks. This is a reference scan, not a transactional distributed authority. Local PID-owned locks exclude review and enrolled merge for the same repository/PR.

`acceptance_required` 默认为 false。启用后，共享的自动合并入口和显式 CLI 合并都会受验收门禁保护，并在尝试合并时重新检查验收证据和 CI。GitHub 与 GitLab 的条件合并均传入原始验收 head SHA；只有服务端确认已合并且返回有效合并提交 SHA 才算成功。GitLab 支持自托管实例及子组路径，验收 checkout 的 origin 必须匹配配置的实例和仓库；head 移动或 MR 不可合并时返回失败。不支持条件合并的提供方会阻止已启用验收的仓库合并。提供方的分支保护规则仍然有效。

Head conditional merge does not atomically pin the target base. Integration candidates, merge queues and post-merge verification require subsequent work. Read-only execution uses the configured backend's permission enforcement; it is not a new container or project credential-isolation layer. Same-host file ownership remains a trust boundary until the durable authority/effect gateway work is complete.

## Automatic harvest

`runHarvestPhase` (`cmd/clawflow/commands/harvest.go`) runs between the operator phase and the snapshot/PM phases, outside the scan budget. Repos run sequentially with the scan's bound-machine rules. Selection reuses the sweep's candidate filter, then requires one successful CI read when CI is required and `errAcceptanceMissing` from `currentAcceptance`. That sentinel covers only an absent store or absent completed record; any existing record, unreadable evidence or contract error (such as missing development provenance) launches nothing and logs `run/harvest_skip`.

Each run gets `min(timeout, time to watchdog - 2m)` and is skipped under 5 minutes. A completed record is a reviewer verdict only if it has evidence, which the runner sets after a typed reviewer report. A passing report later rejected by the gate keeps its evidence and stays a verdict. A completed record without evidence is an infrastructure attempt (setup, provider, inspection, storage or cancellation error); the runner saves it as blocked, and `Store.InfrastructureAttempts` counts these per exact contract with the same corruption and ordering checks as `LatestRecord`. The merge gate is unchanged: such a record never passes.

Harvest 在最新记录为基础设施错误、同一 head 和需求在当前二进制下连续失败不足 2 次且距离上次结束至少 15 分钟时重试，否则记录 `infra_cooldown`。有效评审 verdict 不会重试，并终止连续错误序列。2 次错误后记录 `infra_exhausted`，看板显示 `acceptance-infra`，并使用 `<!-- clawflow:acceptance-infra=<重试身份摘要前 12 位> -->` 去重发布一条评论。实际执行入口也限制手动和 worker 验收。达到上限不算新的运行失败，也不消耗产品修复次数。新 head、issue 正文变化或新二进制会重置限额；仅 base 或策略变化不会重置。非通过 verdict 的评论继续使用 `<!-- clawflow:acceptance=<契约摘要前 12 位> -->`。

## Validation

Core tests cover stale contracts, self-acceptance, cancellation, phase ordering, exact JSON schemas, per-attempt retries, latest-failure selection, corruption and local storage constraints. Command fixtures cover requirements provenance, isolated checkout, policy enrollment, missing/stale evidence, local exclusion, manual merge, automatic merge/sweep and conditional retries. Provider fixtures cover expected-head requests, changed-head conflicts and unconfirmed/malformed merge responses.

Live provider execution, GitHub Actions runs and production rollout require separate recorded evidence. No live result is implied by this document.
