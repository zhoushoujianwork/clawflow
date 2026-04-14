---
name: clawflow
description: "自动化 Issue → 评估 → 修复 → PR 流水线。两阶段机制：(1) 评估阶段：自动扫描新 issues，评估置信度，评论提案并添加 `agent-evaluated` 标签；(2) 执行阶段：owner 确认后手动添加 `ready-for-agent` 标签，agent 执行修复并提交 PR。触发条件：(1) 用户说'ClawFlow run'、'检查 ClawFlow'；(2) cron 定时任务自动触发。关键约束：agent 不自己添加 `ready-for-agent` 标签，必须等待 owner 批准。"
metadata:
  openclaw:
    requires:
      bins: ["git", "gh", "clawflow"]
    primaryEnv: "GH_TOKEN"
---

# ClawFlow — 自动化 Issue 修复流水线

你是一个 orchestrator。按照以下流程执行，不要跳过步骤。

---

## Setup

1. 安装 [GitHub CLI](https://cli.github.com/) 并认证：`gh auth login`
2. 安装 ClawFlow CLI：`./install.sh`（自动构建 `~/.clawflow/bin/clawflow`）
3. 确认 CLI 可用：`clawflow --help`
4. 编辑 `~/.clawflow/config/repos.yaml`，添加要监控的仓库（含 `local_path`）

```bash
# 验证环境
clawflow status
```

---

## Phase 1 — 状态检查

```bash
clawflow status
```

输出各仓库的 issue 计数。确认有待处理 issue 后进入 Phase 2。

---

## Phase 2 — Issue 收割

调用 CLI 一次性完成所有过滤和 PR 去重检查：

```bash
clawflow harvest
```

输出 JSON，包含两个列表：

```json
{
  "to_evaluate": [{"repo":"owner/repo","number":1,"title":"...","body":"..."}],
  "to_execute":  [{"repo":"owner/repo","number":5,"title":"...","body":"...","worktree_path":"..."}]
}
```

- `to_evaluate` — 新 issue，未评估，无 agent 标签
- `to_execute` — 已有 `ready-for-agent` + `agent-evaluated`，无 in-progress，无已开放 PR

将两个列表分别存为 `ISSUES_TO_EVALUATE` 和 `ISSUES_TO_EXECUTE`。

如果只需处理某个仓库：

```bash
clawflow harvest --repo owner/repo
```

---

## Phase 3 — Issue 评估（评估队列）

对于 `ISSUES_TO_EVALUATE` 中的每个 issue，进行置信度评估并评论。

### Step 3.0 — 重复 Issue 检查

在评估之前，先检查当前 issue 是否与已有工作重复：

#### 检查步骤

```bash
# 1. 检查已合并 PR 是否覆盖该 issue 功能
gh pr list -R {owner}/{repo} --state merged --json number,title,body | \
  jq '.[] | select(.title + .body | test("{keywords}"; "i"))'

# 2. 检查已关闭的 agent-evaluated issues 是否有相同功能
gh issue list -R {owner}/{repo} --state closed --label agent-evaluated \
  --json number,title,body | \
  jq '.[] | select(.title + .body | test("{keywords}"; "i"))'

# 3. 检查 issue body 中是否有 "Parent Issue" 或 "Related to #X" 链接
# （手动检查 issue body 中的关联引用）
```

#### 判断标准

| 检查类型 | 判断方法 |
|---------|---------|
| **PR 已合并** | 已合并 PR 的标题/描述覆盖当前 issue 的核心功能 |
| **Issue 已关闭** | 已关闭的 agent-evaluated issue 与当前 issue 功能相同 |
| **父 Issue 分解** | issue body 中存在 "Parent Issue" 或 "Related to #X" 引用 |

#### 发现重复时的处理流程

```bash
# 1. 评论说明重复原因
gh issue comment {number} -R {owner}/{repo} --body "## 🔄 重复 Issue 检查报告

经自动检查，此 issue 与已有工作存在重复：

**重复原因：** {duplicate_reason}
**关联参考：** {reference_link}

因此关闭此 issue，避免重复处理。"

# 2. 添加 agent-evaluated 标签
clawflow label add --repo {owner}/{repo} --issue {number} --label agent-evaluated

# 3. 关闭 issue
gh issue close {number} -R {owner}/{repo}
```

**无重复时**：直接进入下方评估策略。

---

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

```bash
# 1. 添加标签
clawflow label add --repo {owner}/{repo} --issue {number} --label agent-evaluated

# 2. 发表评论（根据类型选择模板）
gh issue comment {number} -R {owner}/{repo} --body "<evaluation_body>"
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

```bash
clawflow label add --repo {owner}/{repo} --issue {number} --label agent-evaluated
clawflow label add --repo {owner}/{repo} --issue {number} --label agent-skipped
gh issue comment {number} -R {owner}/{repo} --body "<missing_info_body>"
```

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

对于 `ISSUES_TO_EXECUTE` 中的每个 issue：

### Step 4.1 — 添加处理中标签

```bash
clawflow label add --repo {owner}/{repo} --issue {number} --label in-progress
```

### Step 4.2 — 创建 Git Worktree

```bash
WORKTREE_PATH=$(clawflow worktree create --repo {owner}/{repo} --issue {number})
# 输出示例: /tmp/clawflow-fix/owner-repo-issue-7
```

### Step 4.3 — Spawn Sub-agent

启动修复 agent，工作目录指向 worktree：

**Task Prompt Template:**

```
你是代码修复 agent。任务：修复 GitHub issue 并创建 PR。

<config>
仓库: {owner}/{repo}
本地 worktree 路径: {worktree_path}
分支: fix/issue-{number}
Base branch: {base_branch}
Issue: #{number}
</config>

<issue>
标题: {title}
内容: {body}
</issue>

<instructions>
1. 在 worktree 路径中工作（不要 clone，已有代码）
2. ANALYZE — 阅读代码，理解问题
3. IMPLEMENT — 实现修复（最小化改动）
4. TEST — 运行测试（如果存在）
5. COMMIT — git commit
6. PUSH — git push origin fix/issue-{number}
7. PR — gh pr create，body 必须包含 "Fixes #{number}"
</instructions>

<constraints>
- 不 force-push
- 不修改 base branch
- 不添加无关改动
- 超时 60 分钟
</constraints>
```

### Step 4.4 — 记录处理状态

```bash
clawflow memory write --repo {owner}/{repo} --issue {number} --status in-progress
```

---

## Phase 5 — 结果收集与清理

等待 sub-agent 返回结果，**无论成功或失败，最后都必须清理 worktree**。

### 成功处理

```bash
# 1. 写入 memory
clawflow memory write --repo {owner}/{repo} --issue {number} --status success --pr-url {pr_url}

# 2. 移除 in-progress 标签
clawflow label remove --repo {owner}/{repo} --issue {number} --label in-progress

# 3. 清理 worktree（必须执行）
clawflow worktree remove --repo {owner}/{repo} --issue {number}
```

### 失败处理

```bash
# 1. 写入 memory
clawflow memory write --repo {owner}/{repo} --issue {number} --status failed --reason "{error}"

# 2. 添加 agent-failed 标签，移除 in-progress
clawflow label add    --repo {owner}/{repo} --issue {number} --label agent-failed
clawflow label remove --repo {owner}/{repo} --issue {number} --label in-progress

# 3. 在 issue 评论失败原因
gh issue comment {number} -R {owner}/{repo} --body "ClawFlow agent 处理失败：{error}"

# 4. 清理 worktree（必须执行，即使失败）
clawflow worktree remove --repo {owner}/{repo} --issue {number}
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
7. **worktree 必须在 Phase 5 清理** — 无论成功或失败

### 标签流程图

```
新 Issue
    ↓
[Phase 2] clawflow harvest
    ↓
[Phase 3] 评估 → agent-evaluated + 评论
    ↓
┌─────────────────────────────────────┐
│ 高置信度: 等待 owner 添加 ready-for-agent │
│ 低置信度: agent-skipped, 等待补充信息    │
└─────────────────────────────────────┘
    ↓
[owner 添加 ready-for-agent]
    ↓
[Phase 4] clawflow worktree create → sub-agent 修复 → PR
    ↓
[Phase 5] clawflow worktree remove（成功或失败都执行）
```

---

## 手动触发命令

| 命令 | 行为 |
|------|------|
| `ClawFlow run` | 执行一轮完整收割流程 |
| `检查 ClawFlow 状态` | `clawflow status` |
| `ClawFlow 添加仓库 <owner/repo>` | 编辑 `~/.clawflow/config/repos.yaml` |

---

## 配置文件位置

- 仓库配置: `~/.clawflow/config/repos.yaml`
- 标签定义: `~/.clawflow/config/labels.yaml`
- 处理记录: `~/.clawflow/memory/repos/{owner}-{repo}/`
- CLI 二进制: `~/.clawflow/bin/clawflow`
