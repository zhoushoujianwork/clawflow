# ADR-0001：触发面从「纯 label」扩展为「label + 事件」

> **状态**：Proposed
> **日期**：2026-08-06
> **调研对象**：GitLab Duo Agent Platform（2026-01 GA，19.x 系列持续迭代）
> **影响范围**：`internal/operator`（Trigger schema、matcher）、`internal/run`（scan 循环）、`skills/`

---

## 1. 背景

有人问「GitLab Duo 是不是和 ClawFlow 类似」。调研后结论是：**架构思路高度重合，但 Duo 的触发面比我们全一个数量级**。这篇 ADR 记录对比结论，以及由此确定的能力实现顺序。

### 1.1 两者是同一类东西

| | ClawFlow | GitLab Duo Agent Platform |
|---|---|---|
| 核心抽象 | 算子 = 一个 `SKILL.md` | Agent（角色）+ Flow（多步编排） |
| 状态载体 | VCS label / comment，无 DB | GitLab 原生对象 + session 记录 |
| 执行位置 | 用户本机 `claude -p` | GitLab Runner，用 service account 的 composite identity |
| 定义方式 | Markdown frontmatter | flow registry v1 YAML（`components` / `prompts` / `routers`） |
| 多步编排 | 无显式编排，靠 label 隐式串联（A 的 outcome = B 的 trigger） | Flow 内显式多 agent 编排 + router 分支 |
| 触发模型 | 轮询 open issue/PR | 事件驱动（webhook 级） |
| 平台覆盖 | GitHub + GitLab | 仅 GitLab |
| 成本 | 本地执行，用户自带 Claude 订阅 | Premium/Ultimate + Duo 席位 |

这个设计撞车不是坏消息——它证明「label/事件驱动 + AI 算子 + VCS 原生状态」这条路径被一家上市公司独立验证过。

### 1.2 Duo 的 6 类 trigger 事件

1. **Mention** — service account 被 @ 在 issue/MR comment 里（comment 正文即 goal）
2. **Assign** — 被指派到 issue/MR（资源 IID 作为 goal）
3. **Assign reviewer** — 被指派为 MR reviewer
4. **Pipeline events** — running / passed / **failed** / canceled（完整 webhook payload 作为 goal）
5. **Merge request** — approved / marked ready / **merge conflict**
6. **Work item** — 创建、状态变更

此外它们的 Events Platform 还接外部事件源（Jira / Jenkins / Slack）。

### 1.3 我们的现状

`internal/operator/operator.go` 的 `Trigger` 结构体共 6 个字段，除 `Target` 与 `AppliesTo` 外**全部是 label 语义**：

```go
type Trigger struct {
	Target            string   // "issue" | "pr"
	LabelsRequired    []string // AND
	LabelsRequiredAny []string // OR
	LabelsExcluded    []string // OR NOT
	LabelsConsumed    []string // 一次性流转标记，写回 outcome 后清除
	AppliesTo         string   // "any" | "parent" | "leaf"
}
```

事实清单：

- **触发维度只有一个**：label 的存在与否。没有「谁 @ 了我」「CI 挂了」「被指派了」
- **`target: "pr"` 是死代码**：schema 与 matcher 都支持，但 `skills/` 下 8 个内置算子（classify / decompose / evaluate-bug / evaluate-feat / implement / reply-comment / reply-question / track-progress）**全部是 `target: "issue"`**；且 scan 循环根本不拉 PR——`run.go:631` 里 `IsPR` 恒为 `false`，`run.go:708` 留着 TODO 注释「pr-target operator appears we'll add the same loop over `ListOpenPRs`」
- **轮询而非事件**：延迟取决于 cron 间隔，且每轮全量扫 open issue/PR

### 1.4 label-only 的真实代价

不是「少了个功能」，而是**抬高了首次使用的门槛**：

- 用户要先知道有哪些算子、先在仓库建对应 label，才能触发第一次
- label 是布尔信号，**不携带意图**。用户想说「帮我看下这个函数为什么慢」，label 表达不了，只能靠算子自己去 body 里猜
- 相比之下 mention 是零学习成本入口，且 comment 正文天然就是 prompt

---

## 2. 决策

**把 `Trigger` 从「纯 label 匹配」扩展为「label + 事件」的联合门控，label 语义保持完全向后兼容。**

具体形态（待实现时细化）：

- 新增可选字段 `trigger.on: [...]`，声明该算子响应哪些事件类型
- 未声明 `on` 的算子 = 现有行为（轮询期扫描 + label 匹配），**存量 8 个算子零改动**
- 声明了 `on` 的算子，事件到达时把事件载荷作为 goal 注入 prompt，label 条件仍然生效（作为 AND 门）

**明确不做的**（保持第一版边界）：

- 不引入 YAML DSL 做多算子编排。label 隐式串联依然是编排机制——这是我们相对 Duo 的 DX 优势，不要丢
- 不做外部事件源（Jira/Jenkins/Slack）。等 GitHub/GitLab 原生事件跑通再说
- 不强制 webhook。轮询模式下也能拿到事件（拉 comment/CI 状态做 diff），webhook 是后续优化而非前置条件

### 2.1 我们守住的差异化

补触发面的同时，这几条不能因为「追齐 Duo」而牺牲：

1. **跨平台** — Duo 只服务 GitLab。GitHub 那一大半市场它够不着，这是最硬的护城河
2. **本地执行** — 跑在用户机器上，有真实 worktree、能跑测试、能读私有依赖。Runner 沙箱做这些成本高得多
3. **零 DSL** — 算子就是一个 Markdown。Duo 的 v1 YAML 还砍掉了 `model`、`response_schema_id`、`stop` 等一堆字段，写起来更绕
4. **不吃 seat 费** — 用户自带 Claude 订阅，不额外买 GitLab 席位

---

## 3. 能力实现清单（按 ROI 排序）

以下是这次讨论确定的实现顺序。**前三项都不需要改架构**，只是把 trigger 从「纯 label 匹配」扩成「label + 事件」。

### P0 — `@clawflow` mention 触发

| | |
|---|---|
| **对应 Duo** | Mention trigger |
| **价值** | 最高。零学习成本入口（不用先建 label），且 comment 正文天然携带意图，直接作为 goal |
| **改动** | `Trigger.on` 增加 `mention`；scan 期拉 comment 增量、识别 `@clawflow` 前缀；comment 正文注入 prompt |
| **风险** | 需要幂等：同一条 comment 不能重复触发。用「已回复过的 comment id」或回复自身作为已处理标记 |

### P1 — PR 侧算子（`review-pr`）

| | |
|---|---|
| **对应 Duo** | Code Review foundational flow |
| **价值** | `target: "pr"` 已在 schema 里但零实现，属于「已付的架构成本没兑现」。打开整条 PR 赛道 |
| **改动** | **两处**：① scan 循环补 PR 拉取——`cmd/clawflow/commands/run.go:708` 有明确 TODO 注释「pr-target operator appears we'll add the same loop over `ListOpenPRs`」，目前 `IsPR` 恒为 `false`（run.go:631），PR 从未进入扫描；② 新建 `skills/review-pr/SKILL.md` |
| **已就位** | `matcher.go:43-46` 的 target 门控已实现；`run.go:1476` 已把 `target == "pr"` 归入需要本地 repo；`vcs.ListOpenPRs` 接口已存在。缺的只有 scan 侧那一层循环 |
| **风险** | 低。改动点明确且已被前人预留 |

### P2 — CI failed 触发（`fix-ci`）

| | |
|---|---|
| **对应 Duo** | Pipeline events（failed） |
| **价值** | 明确且高频的痛点。GitHub Actions 与 GitLab CI 都能拿到状态 |
| **改动** | `Trigger.on` 增加 `ci_failed`；VCS 层补 pipeline/check-run 状态查询；失败日志注入 prompt |
| **风险** | 中。日志可能很长，需要截断策略；重试风暴需要退避（同一 commit 只修一次） |

### P3 — assignee 触发

| | |
|---|---|
| **对应 Duo** | Assign / Assign reviewer trigger |
| **价值** | 比 label 更符合人的直觉——「把活派给机器人」。可与 P0 共用同一套事件抓取逻辑 |
| **改动** | `Trigger.on` 增加 `assigned`；复用 P0 的事件管道 |
| **风险** | 低（P0 做完后基本是增量） |

---

## 4. 后果

**正面**

- 首次使用门槛显著下降（mention 不需要预建 label）
- 触发信号从布尔值升级为带载荷的事件，算子能拿到用户的真实意图
- PR 赛道打开，覆盖场景从「需求侧」扩到「代码侧」

**负面 / 需要盯的**

- scan 循环复杂度上升：从「拉 open 列表 + 比 label」变成「还要拉 comment/CI 状态增量」，API 调用量增加。`internal/vcs` 目前**无分页、零限流**（见 `docs/project-assessment-2026-05.md`），这会先撞到限流问题
- 幂等性从「靠 label 排除」变成「靠事件去重」，需要新的已处理状态载体。这是本次扩展最大的设计风险点
- `Trigger` schema 变大，算子作者的认知负担上升。必须保证「不写 `on` 就是老行为」

---

## 5. 参考

- [GitLab Duo Triggers 文档](https://docs.gitlab.com/user/duo_agent_platform/triggers/)
- [Custom flow YAML schema](https://docs.gitlab.com/user/duo_agent_platform/flows/custom_flows_schema/)
- [Introduction to GitLab Duo Agent Platform](https://about.gitlab.com/blog/introduction-to-gitlab-duo-agent-platform/)
- [Understanding flows: multi-agent workflows](https://about.gitlab.com/blog/understanding-flows-multi-agent-workflows/)
- [Issue to Merge Request Flow](https://docs.gitlab.com/user/duo_agent_platform/flows/issue_to_mr/)
- [GitLab Events Platform（handbook）](https://handbook.gitlab.com/handbook/engineering/ai/ai-coding/event_platform)
