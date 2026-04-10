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

## Phase 1 — 配置加载

读取仓库配置文件：

```bash
REPOS_FILE="~/.openclaw/workspace/clawflow/config/repos.yaml"
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

### 评估维度（各 1-10 分）

| 维度 | 标准 |
|------|------|
| **清晰度** | 问题描述是否清楚？有明确的预期行为和实际行为？ |
| **范围** | 修复范围是否合理？单文件/单函数=好，整个子系统=差 |
| **可行性** | 能否找到相关代码？是否有足够上下文？ |

**置信度 = (清晰度 + 范围 + 可行性) / 3**

### 高置信度处理（推荐修复）

对于置信度 >= threshold 的 issue：

1. **添加 `agent-evaluated` 标签**
2. **评论评估结果和修复提案**

```bash
curl -s -X POST \
  -H "Authorization: Bearer $GH_TOKEN" \
  -H "Accept: application/vnd.github+json" \
  https://api.github.com/repos/{owner}/{repo}/issues/{number}/labels \
  -d '{"labels":["agent-evaluated"]}'

curl -s -X POST \
  -H "Authorization: Bearer $GH_TOKEN" \
  -H "Accept: application/vnd.github+json" \
  https://api.github.com/repos/{owner}/{repo}/issues/{number}/comments \
  -d '{"body": "## 🔍 ClawFlow 评估报告\n\n**置信度:** {score}/10 ✅ (高于阈值 {threshold})\n\n**评估详情:**\n- 清晰度: {clarity}/10 — {clarity_reason}\n- 范围: {scope}/10 — {scope_reason}\n- 可行性: {feasibility}/10 — {feasibility_reason}\n\n**修复提案:**\n{fix_proposal}\n\n---\n\n👉 **如果您同意此修复方案，请手动添加 `ready-for-agent` 标签以触发自动修复。**\n\n⚠️ 注意：Agent 不会自动添加此标签，需要 owner 确认后手动操作。"}'
```

### 低置信度处理（需要补充信息）

对于置信度 < threshold 的 issue：

1. **添加 `agent-evaluated` 和 `agent-skipped` 标签**
2. **评论说明缺少什么信息**

```bash
curl -s -X POST \
  -H "Authorization: Bearer $GH_TOKEN" \
  -H "Accept: application/vnd.github+json" \
  https://api.github.com/repos/{owner}/{repo}/issues/{number}/labels \
  -d '{"labels":["agent-evaluated","agent-skipped"]}'

curl -s -X POST \
  -H "Authorization: Bearer $GH_TOKEN" \
  -H "Accept: application/vnd.github+json" \
  https://api.github.com/repos/{owner}/{repo}/issues/{number}/comments \
  -d '{"body": "## 🔍 ClawFlow 评估报告\n\n**置信度:** {score}/10 ⚠️ (低于阈值 {threshold})\n\n**评估详情:**\n- 清晰度: {clarity}/10 — {clarity_reason}\n- 范围: {scope}/10 — {scope_reason}\n- 可行性: {feasibility}/10 — {feasibility_reason}\n\n**需要补充的信息:**\n{missing_info}\n\n---\n\n💡 请补充以上信息后，移除 `agent-skipped` 标签并添加 `ready-for-agent` 以重新触发评估。"}'
```

---

## Phase 4 — Sub-agent 调度（执行队列）

对于 `ISSUES_TO_EXECUTE` 中的每个 issue（已带 `ready-for-agent` 标签）：

### Step 4.1 — 添加处理中标签

```bash
curl -s -X POST \
  -H "Authorization: Bearer $GH_TOKEN" \
  -H "Accept: application/vnd.github+json" \
  https://api.github.com/repos/{owner}/{repo}/issues/{number}/labels \
  -d '{"labels":["in-progress"]}'
```

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

使用 GitHub REST API（不要用 gh CLI）：
- GH_TOKEN 已在环境中
- curl -H "Authorization: Bearer $GH_TOKEN" ...

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
MEMORY_FILE="~/.openclaw/workspace/clawflow/memory/repos/{owner}-{repo}/issue-{number}.md"
mkdir -p $(dirname $MEMORY_FILE)

echo "# Issue #{number} - {title}

- 仓库: {owner}/{repo}
- 置信度: {score}/10
- 状态: processing
- Sub-agent session: {session_id}
- 开始时间: {timestamp}
- Issue URL: {url}

## Issue Body

{body}
" > $MEMORY_FILE
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

### 通知格式

使用 `message` tool：

```json
{
  "action": "send",
  "message": "✅ ClawFlow PR 已创建\n\n仓库: {owner}/{repo}\nIssue: #{number} - {title}\nPR: {pr_url}\n置信度: {score}/10"
}
```

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

用户可以通过以下命令手动触发：

| 命令 | 行为 |
|------|------|
| `ClawFlow run` | 执行一轮完整收割流程 |
| `检查 ClawFlow 状态` | 显示所有配置仓库和待处理 issue 数量 |
| `收割 ready-for-agent` | 立即执行收割（同 `ClawFlow run`） |
| `ClawFlow 添加仓库 <owner/repo>` | 将新仓库添加到配置 |

---

## Cron 配置

在 OpenClaw 中配置定时任务（已创建）：

```json
{
  "name": "ClawFlow Issue Harvest",
  "schedule": { "kind": "every", "everyMs": 900000 },
  "payload": {
    "kind": "agentTurn",
    "message": "执行 ClawFlow issue 收割",
    "thinking": "low"
  }
}
```

---

## 与 gh-issues 技能的区别

| 特性 | ClawFlow | gh-issues |
|------|----------|-----------|
| 触发方式 | 标签 `ready-for-agent` | 命令参数 |
| 安全机制 | Owner-only 标签控制 | 无 |
| 评估机制 | 置信度评估 | 无（直接处理） |
| 适用场景 | 需要人工审批的自动化 | 批量处理已知 issues |

---

## 配置文件位置

- 仓库配置: `~/.openclaw/workspace/clawflow/config/repos.yaml`
- 标签定义: `~/.openclaw/workspace/clawflow/config/labels.yaml`
- 处理记录: `~/.openclaw/workspace/clawflow/memory/repos/{owner}-{repo}/`

---

## 示例：手动收割

```bash
# 检查 llm-wiki 的 ready-for-agent issues
curl -s -H "Authorization: Bearer $GH_TOKEN" \
  "https://api.github.com/repos/zhoushoujianwork/llm-wiki/issues?state=open&labels=ready-for-agent"

# 添加标签
curl -s -X POST -H "Authorization: Bearer $GH_TOKEN" \
  https://api.github.com/repos/zhoushoujianwork/llm-wiki/issues/123/labels \
  -d '{"labels":["in-progress"]}'
```