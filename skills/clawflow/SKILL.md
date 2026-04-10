# ClawFlow Skill - 自动化 Issue 修复流水线

## 触发条件

当收到以下指令时触发此技能：
- "检查 ClawFlow 状态"
- "收割 ready-for-agent issues"
- "ClawFlow run"
- "处理 issue"

或者通过 cron 定时任务自动触发。

## 工作流程

### 1. Issue 收割（Harvest）

```bash
# 检查所有配置仓库中带 ready-for-agent 标签的 issue
gh issue list --repo <owner/repo> --label ready-for-agent --state open
```

对于每个找到的 issue：
1. 检查是否已有 `in-progress` 标签（避免重复处理）
2. 如果没有，开始评估流程

### 2. Issue 评估（Evaluate）

读取 issue 内容，评估：
- **清晰度**：问题描述是否清楚？（1-10）
- **范围**：修复范围是否合理？单文件/单函数=好，整个子系统=差（1-10）
- **可行性**：能否找到相关代码？是否有足够信息？（1-10）
- **置信度**：综合评分，是否 >= 配置的阈值？

评估结果记录到 `memory/repos/<owner>-<repo>/issue-<number>.md`

如果置信度 < 阈值：
1. 添加 `agent-skipped` 标签
2. 在 issue 中评论说明原因
3. 跳过此 issue

### 3. Sub-agent 调度（Dispatch）

如果置信度 >= 阈值：
1. 添加 `in-progress` 标签
2. Spawn sub-agent：

```json
{
  "runtime": "subagent",
  "mode": "run",
  "cleanup": "keep",
  "timeoutSeconds": 3600,
  "task": "修复 GitHub issue #<number> in <owner/repo>...\n\nIssue内容:\n<title>\n<body>\n\n要求:\n1. 创建新分支 fix-issue-<number>\n2. 分析并修复问题\n3. 提交 PR 到 <base_branch>\n4. PR 标题格式: [Agent] Fix: <title>\n5. PR body 包含: Fixes #<number>"
}
```

### 4. 结果处理（Notify）

Sub-agent 完成后：
- 成功：发送 PR 链接给 owner
- 失败：添加 `agent-failed` 标签，通知 owner

## 命令接口

### 手动触发

```
检查 ClawFlow 状态
```
→ 显示所有配置仓库和待处理 issue 数量

```
收割 ready-for-agent
```
→ 立即执行一轮收割流程

```
ClawFlow 添加仓库 <owner/repo>
```
→ 将新仓库添加到配置

### Cron 配置

在 OpenClaw 中配置定时任务：

```json
{
  "schedule": { "kind": "every", "everyMs": 900000 },
  "payload": {
    "kind": "agentTurn",
    "message": "执行 ClawFlow issue 收割",
    "thinking": "low"
  }
}
```

## 记忆管理

每个仓库的 issue 处理记录保存在：
`memory/repos/<owner>-<repo>/issue-<number>.md`

记录内容：
- issue 标题和内容
- 评估分数
- 处理状态
- PR 链接（如果有）
- 时间戳

## 安全约束

1. **只处理 `ready-for-agent` 标签的 issue**
2. **不会自己添加 `ready-for-agent` 标签**
3. **PR 目标分支固定为配置的 base_branch**
4. **超时强制停止**（默认 60 分钟）
5. **低置信度直接跳过**

## 依赖

- GitHub CLI (`gh`) 或 GitHub REST API
- OpenClaw sessions_spawn 功能
- Git 操作权限

## 示例

```bash
# 手动检查 llm-wiki 的 ready-for-agent issues
gh issue list --repo zhoushoujianwork/llm-wiki --label ready-for-agent --state open

# 查看特定 issue 详情
gh issue view 123 --repo zhoushoujianwork/llm-wiki

# 添加标签
gh issue edit 123 --repo zhoushoujianwork/llm-wiki --add-label in-progress

# 创建 PR
gh pr create --repo zhoushoujianwork/llm-wiki --base main --head fix-issue-123 --title "[Agent] Fix: ..." --body "Fixes #123"
```