# Phase 3 — 评估策略与评论模板

## 评估策略（按类型区分）

根据 issue 的 `labels` 字段，采用不同的评估策略：

**类型判断规则（按优先级）：**
1. labels 包含 `bug` → Bug 类型评估
2. labels 包含 `enhancement` 或 `feat` → Feature 类型评估
3. 以上均不包含 → 通用评估（fallback）

---

### Bug 类型评估

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

### Feature 类型评估

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

### 通用评估（无类型标签 fallback）

对于没有 `bug`、`enhancement`、`feat` 标签的 issue，先推断类型再评估：

1. **类型推断**：根据 title 和 body 判断是 bug（描述异常行为/错误）还是 feature（描述新功能/改进），并在评估报告中注明推断结果
2. **评估维度**：按推断类型套用对应的 Bug 或 Feature 评估维度
3. **标签建议**：在评估评论中建议 owner 补充对应类型标签（`bug` 或 `enhancement`）

**置信度 = (维度1 + 维度2 + 维度3) / 3**

---

## 拆分建议评估

在完成置信度评估后，判断是否需要拆分（满足任意一条即建议拆分）：

| 触发条件 | 说明 |
|---------|------|
| 涉及 2 个以上独立模块/功能点 | 各功能点可独立实现、独立测试 |
| 预计改动文件超过 5 个 | 改动范围过大，PR 难以 review |
| issue body 中包含多个独立的 TODO 项 | 每个 TODO 可单独成为一个 issue |

**注意：子 issue 不再触发拆分（拆分深度限制为 1 层）。**
判断方法：检查 issue body 是否包含 `Parent Issue: #` 字样，有则为子 issue，跳过拆分建议。

### 拆分建议评论模板（追加到评估报告末尾）

当满足拆分条件时，在评估评论末尾追加：

```
---

### 🔀 拆分建议

此 issue 建议拆分为以下子任务：

1. **子任务 1**：{标题} — {一句话说明}
2. **子任务 2**：{标题} — {一句话说明}

**拆分原因：** {触发条件说明}

如同意拆分，请添加 `ready-for-agent` 标签触发自动创建子 issue。
如不需要拆分，请在评论中说明后再添加 `ready-for-agent`。
```

---

## 高置信度处理（推荐修复）

对于置信度 >= threshold 的 issue：

```bash
clawflow label add --repo {owner}/{repo} --issue {number} --label agent-evaluated
clawflow issue comment --repo {owner}/{repo} --issue {number} --body "<evaluation_body>"
```

### Bug 类型评论模板

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

---
🤖 Powered by [ClawFlow](https://github.com/zhoushoujianwork/clawflow) — automated issue → fix → PR pipeline
```

### Feature 类型评论模板

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

---
🤖 Powered by [ClawFlow](https://github.com/zhoushoujianwork/clawflow) — automated issue → fix → PR pipeline
```

---

## 低置信度处理（需要补充信息）

对于置信度 < threshold 的 issue：

```bash
clawflow label add --repo {owner}/{repo} --issue {number} --label agent-evaluated
clawflow label add --repo {owner}/{repo} --issue {number} --label agent-skipped
clawflow issue comment --repo {owner}/{repo} --issue {number} --body "<missing_info_body>"
```

### 低置信度评论模板

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

---
🤖 Powered by [ClawFlow](https://github.com/zhoushoujianwork/clawflow) — automated issue → fix → PR pipeline
```
