---
name: clawflow
description: "自动化 Issue → 评估 → 修复 → PR 流水线。两阶段机制：(1) 评估阶段：自动扫描新 issues，评估置信度，评论提案并添加 `agent-evaluated` 标签；(2) 执行阶段：owner 确认后手动添加 `ready-for-agent` 标签，agent 执行修复并提交 PR。触发条件：(1) 用户说'ClawFlow run'、'检查 ClawFlow'；(2) cron 定时任务自动触发。关键约束：agent 不自己添加 `ready-for-agent` 标签，必须等待 owner 批准。"
metadata:
  openclaw:
    requires:
      bins: ["curl", "git"]
      anyBins: ["gh"]
    primaryEnv: "GH_TOKEN"
---

# ClawFlow — 自动化 Issue 修复流水线

你是一个 orchestrator。按照以下流程执行，不要跳过步骤。

---

## Setup

1. Install [GitHub CLI](https://cli.github.com/) and authenticate: `gh auth login`
2. Ensure `git` is installed
3. Edit `~/.clawflow/config/repos.yaml` — add the repositories you want to monitor
4. Set `GH_TOKEN` environment variable (or rely on `gh auth` token)

```bash
# Verify setup
gh auth status
gh repo view <your-org/your-repo>
```

---

## Phase 1 — 配置加载

读取仓库配置文件：

```bash
REPOS_FILE="~/.clawflow/config/repos.yaml"
```

解析 YAML，提取所有 `enabled: true` 的仓库。对于每个仓库：

- `owner/repo` — 仓库标识
- `base_branch` — PR 目标分支
- `labels.trigger` — 触发标签（默认 `ready-for-agent`）
- `labels.in_progress` — 处理中标签

存储为 `REPOS_LIST`。

---

## Phase 2 — Issue 收割

### 2.1 评估队列（新 issues）

扫描所有开放的 issues，筛选需要评估的：

```bash
curl -s -H "Authorization: Bearer $GH_TOKEN" -H "Accept: application/vnd.github+json" \
  "https://api.github.com/repos/{owner}/{repo}/issues?state=open"
```

**过滤规则（需评估）：**
- 没有 `pull_request` 字段（排除 PRs）
- 没有 `agent-evaluated` 标签（未评估过）
- 没有 `in-progress` 标签
- 没有 `agent-skipped` 标签
- 没有 `agent-failed` 标签

这些 issues 加入 `ISSUES_TO_EVALUATE` 列表。

### 2.2 执行队列（已批准）

扫描带 `ready-for-agent` 标签的 issues：

```bash
curl -s -H "Authorization: Bearer $GH_TOKEN" -H "Accept: application/vnd.github+json" \
  "https://api.github.com/repos/{owner}/{repo}/issues?state=open&labels=ready-for-agent"
```

**过滤规则（可执行）：**
- 没有 `pull_request` 字段
- 没有 `in-progress` 标签（未在处理中）
- 有 `agent-evaluated` 标签（已评估过）

这些 issues 加入 `ISSUES_TO_EXECUTE` 列表。

---

## Phase 3 — Issue 评估（评估队列）

对于 `ISSUES_TO_EVALUATE` 中的每个 issue，进行置信度评估并评论。

### 评估策略（按类型区分）

根据 issue 的标签类型，采用不同的评估策略：

#### Bug 类型评估

对于带有 `bug` 标签的 issue，评估**复现情况**：

| 维度 | 标准 | 分数 (1-10) |
|------|------|-------------|
| **复现性** | 能否根据描述复现问题？有明确的复现步骤？ | 复现清晰=高分，无法复现=低分 |
| **根因定位** | 能否定位到具体代码位置？根因是否明确？ | 已定位=高分，模糊=低分 |
| **修复难度** | 修复是否简单直接？是否涉及核心逻辑？ | 单点修复=高分，系统性改动=低分 |

**Bug 评估输出内容：**
- **复现步骤**：如何复现这个 bug？
- **根因分析**：问题出在哪里？哪个文件/函数？
- **修复建议**：如何修复？改动范围多大？

#### Feature 类型评估

对于带有 `enhancement` 或 `feat` 标签的 issue，评估**实现方案与架构对齐**：

| 维度 | 标准 | 分数 (1-10) |
|------|------|-------------|
| **需求清晰度** | 功能需求是否明确？有清晰的输入输出定义？ | 明确=高分，模糊=低分 |
| **设计合理性** | 提出的设计方案是否合理？是否与整体项目架构一致？ | 符合架构=高分，架构偏离=低分 |
| **确认必要性** | 该实现是否涉及重大设计决策，需要 owner 额外确认？ | 无需确认=高分，需确认=低分 |

**Feature 评估输出内容：**
- **实现方案**：如何实现这个功能？具体步骤？
- **技术选型**：用什么技术/库/API？
- **改动范围**：需要改动哪些文件/模块？
- **架构对齐分析**：设计方案是否遵循项目的整体架构原则？是否存在架构偏离风险？
- **Owner 确认标记**：是否需要 owner 在设计层面进一步确认？（是/否）

**置信度 = (维度1 + 维度2 + 维度3) / 3**

### 高置信度处理（推荐修复）

对于置信度 >= threshold 的 issue：

1. **添加 `agent-evaluated` 标签**
2. **评论评估结果和修复/实现方案**

**优先使用 gh CLI**（已配置认证）：

```bash
# 添加标签
gh issue edit {number} -R {owner}/{repo} --add-label "agent-evaluated"

# 评论（根据类型选择模板）
gh issue comment {number} -R {owner}/{repo} --body "<evaluation_body>"

# 添加多个标签
gh issue edit {number} -R {owner}/{repo} --add-label "agent-evaluated,agent-skipped"
```

**Bug 类型评论模板：**

```
## 🔍 ClawFlow 评估报告

**Issue 类型:** Bug
**置信度:** {score}/10 ✅ (高于阈值 {threshold})

---

### 复现情况分析

**复现性:** {reproducibility}/10 — {repro_reason}
**根因定位:** {root_cause}/10 — {root_reason}
**修复难度:** {fix_difficulty}/10 — {fix_reason}

**复现步骤：**
{repro_steps}

**⚠️ 复现验证状态:** {reproduction_verified} ⚠️
- **验证结果：** {verify_result}
- **验证详情：** {verify_details}

**根因分析:**
{root_cause_analysis}

**修复建议:**
{fix_suggestion}

---

👉 **如果您同意此方案，请手动添加 `ready-for-agent` 标签以触发自动修复。**

⚠️ 注意：Agent 不会自动添加此标签，需要 owner 确认后手动操作。
```

**Feature 类型评论模板：**

```
## 🔍 ClawFlow 评估报告

**Issue 类型:** Feature
**置信度:** {score}/10 ✅ (高于阈值 {threshold})

---

### 实现方案分析

**需求清晰度:** {clarity}/10 — {clarity_reason}
**设计合理性:** {design}/10 — {design_reason}
**确认必要性:** {confirmation}/10 — {confirm_reason}

**实现方案:**
{implementation_plan}

**技术选型:**
{tech_choice}

**改动范围:**
{change_scope}

---

### 🏗️ 架构对齐分析

**架构一致性:** {arch_alignment} — {arch_reason}

> {architecture_notes}

**Owner 确认标记：** {owner_confirmation_flag} ⚠️
- **是否需要确认：** {need_owner_confirmation}
- **确认理由：** {confirmation_reason}

---

👉 **如果您同意此方案，请手动添加 `ready-for-agent` 标签以触发自动修复。**

⚠️ 注意：Agent 不会自动添加此标签，需要 owner 确认后手动操作。
```

### 低置信度处理（需要补充信息）

对于置信度 < threshold 的 issue：

1. **添加 `agent-evaluated` 和 `agent-skipped` 标签**
2. **评论说明缺少什么信息**

```bash
gh issue edit {number} -R {owner}/{repo} --add-label "agent-evaluated,agent-skipped"
gh issue comment {number} -R {owner}/{repo} --body "<missing_info_body>"
```

**重要**：优先使用 `gh` CLI 命令操作 GitHub，不要用 curl API。gh CLI 已配置好认证。

**低置信度评论模板：**

```
## 🔍 ClawFlow 评估报告

**Issue 类型:** {type}
**置信度:** {score}/10 ⚠️ (低于阈值 {threshold})

---

### 评估详情

{evaluation_details}

**需要补充的信息:**
{missing_info}

---

💡 请补充以上信息后，移除 `agent-skipped` 标签并添加 `ready-for-agent` 以重新触发评估。
```

---

## Phase 4 — Sub-agent 调度（执行队列）

对于 `ISSUES_TO_EXECUTE` 中的每个 issue（已带 `ready-for-agent` 标签）：

### Step 4.1 — 添加处理中标签

```bash
gh issue edit {number} -R {owner}/{repo} --add-label "in-progress"
```

**重要**：使用 `gh` CLI 操作 GitHub，不要用 curl API。

### Step 4.2 — Spawn Sub-agent

使用 `sessions_spawn` 启动修复 agent：

```json
{
  "runtime": "subagent",
  "mode": "run",
  "cleanup": "keep",
  "runTimeoutSeconds": 3600,
  "task": "<task_prompt>"
}
```

**Task Prompt Template:**

```
你是代码修复 agent。任务：修复 GitHub issue 并创建 PR。

<config>
仓库: {owner}/{repo}
Base branch: {base_branch}
Issue: #{number}
</config>

<issue>
标题: {title}
内容: {body}
标签: {labels}
</issue>

<instructions>
1. CLONE/FETCH — 获取代码
2. BRANCH — 创建 fix/issue-{number} 分支
3. ANALYZE — 分析问题，定位相关代码
4. IMPLEMENT — 实现修复（最小化改动）
5. TEST — 运行测试（如果存在）
6. COMMIT — 提交改动
7. PUSH — 推送分支
8. PR — 创建 Pull Request

PR body 必须包含：
- 修复摘要
- Files changed 列表
- "Fixes #{number}" 链接
</instructions>

<constraints>
- 不 force-push
- 不修改 base branch
- 不添加无关改动
- 超时 60 分钟
</constraints>
```

### Step 4.3 — 记录处理状态

将处理记录写入 memory：

```bash
MEMORY_FILE="~/.clawflow/memory/repos/{owner}-{repo}/issue-{number}.md"
mkdir -p $(dirname $MEMORY_FILE)
```

---

## Phase 5 — 结果收集与通知

### Sub-agent 完成后

等待 sub-agent 返回结果：

- **成功：** PR URL
- **失败：** 错误信息

**成功处理：**

1. 更新 memory 文件，添加 PR URL
2. 移除 `in-progress` 标签
3. 通过 OpenClaw message 发送通知

**失败处理：**

1. 添加 `agent-failed` 标签
2. 在 issue 评论失败原因
3. 更新 memory 文件记录失败

---

## Phase 6 — 安全约束

**严格执行：**

1. **评估阶段不执行修复** — 只评论评估结果
2. **执行阶段只处理 `ready-for-agent` 标签** — 必须有 owner 批准
3. **不自己添加 `ready-for-agent` 标签** — 只有 owner 可以
4. **PR 目标分支固定为配置的 `base_branch`**
5. **超时强制停止**（60 分钟）
6. **低置信度不强行修复** — 请求补充信息

### 标签流程图

```
新 Issue
    ↓
[自动评估] → agent-evaluated + 评论
    ↓
┌─────────────────────────────────────┐
│ 高置信度: 等待 owner 添加 ready-for-agent │
│ 低置信度: agent-skipped, 等待补充信息    │
└─────────────────────────────────────┘
    ↓
[owner 添加 ready-for-agent]
    ↓
[执行修复] → in-progress → 创建 PR → 完成
```

---

## 手动触发命令

| 命令 | 行为 |
|------|------|
| `ClawFlow run` | 执行一轮完整收割流程 |
| `检查 ClawFlow 状态` | 显示所有配置仓库和待处理 issue 数量 |
| `ClawFlow 添加仓库 <owner/repo>` | 将新仓库添加到配置 |

---

## 配置文件位置

- 仓库配置: `~/.clawflow/config/repos.yaml`
- 标签定义: `~/.clawflow/config/labels.yaml`
- 处理记录: `~/.clawflow/memory/repos/{owner}-{repo}/`