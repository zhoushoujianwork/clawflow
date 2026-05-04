# 双层自动化：固定管道 + 智能体观察者

> ClawFlow 的核心洞察：**确定性规则处理已知路径，智能体处理需要判断的部分**。两层通过 label 协作，形成全自动闭环。

---

## 问题：纯规则不够，纯智能体太贵

传统 CI/CD 是纯规则系统——触发条件明确，执行路径固定。它擅长处理"如果 X 则做 Y"，但面对需要理解上下文的决策就无能为力：

- 这个 bug 报告质量够不够高？该分配给谁？
- 这个 feature request 和现有架构冲突吗？
- 三个 issue 卡了一周没人动，该升级优先级吗？
- 这个 PR 的测试覆盖率下降了，是合理的还是偷懒？

另一个极端是让 AI agent 全权处理——每个 issue 都跑一遍完整的分析链。这能工作，但成本高、延迟大、不可预测。大部分 issue 的处理路径其实是确定的，不需要每次都"思考"。

ClawFlow 的答案是**分层**。

---

## 两层架构

```
┌─────────────────────────────────────────────────────┐
│                   可变层 (Brain)                      │
│                                                      │
│   Claude Code skill + /loop                          │
│   观察 → 判断 → 决策 → 输出 label/issue              │
│                                                      │
│   "这个 issue 卡住了，需要升级"                        │
│   "这批 PR 可以批量合并"                               │
│   "这个 repo 最近 bug 密度异常"                        │
└──────────────────────┬──────────────────────────────┘
                       │ label / issue / comment
                       ▼
┌─────────────────────────────────────────────────────┐
│                   固定层 (Hands)                      │
│                                                      │
│   clawflow run                                       │
│   label 匹配 → 算子执行 → outcome label               │
│                                                      │
│   bug → evaluate-bug → agent-evaluated               │
│   ready-for-agent → implement → agent-implemented    │
└─────────────────────────────────────────────────────┘
```

### 固定层：`clawflow run`

确定性管道。规则写在算子的 frontmatter 里：

```yaml
operator:
  trigger:
    target: "issue"
    labels_required: ["bug"]
    labels_excluded: ["agent-evaluated", "agent-running"]
```

- **输入确定**：label 组合决定是否触发
- **路径确定**：每个算子只做一件事
- **输出确定**：outcome label + comment
- **可预测**：同样的输入永远走同样的路径

固定层不做判断，只做执行。它是流水线上的机械臂——精确、快速、可靠。

### 可变层：skill + `/loop`

智能体观察者。通过 Claude Code 的 skill 机制实现，用 `/loop` 保持持续运转：

```
/loop 30m 扫描项目状态，识别需要干预的 issue，执行对应动作
```

- **输入不确定**：需要理解 issue 内容、项目上下文、时间维度
- **判断不确定**：同一个 issue 在不同阶段可能需要不同处理
- **输出是 label/issue**：把决策结果翻译成固定层能消费的信号

可变层是"大脑"——它观察全局，做需要理解力的判断，然后把结论交给固定层执行。

### 协作点：Label

两层唯一的通信协议就是 **label**。

可变层的输出：
```
观察到 issue #12 卡了 5 天没进展
  → 判断：需要升级优先级
  → 动作：加 label "priority-high"，留 comment 说明原因
```

固定层的消费：
```
clawflow run 扫到 priority-high + bug
  → 匹配 escalate-bug 算子
  → 执行：通知 owner，调整评估权重
```

这个设计的关键在于**解耦**——可变层不需要知道固定层有哪些算子，固定层不需要知道 label 是人加的还是智能体加的。Label 就是合约。

---

## 设计智能体 Skill

一个观察者 skill 的核心结构：

### 1. 观察（Observe）

收集当前状态。不是每次都全量扫描——聚焦于"上次检查以来发生了什么变化"。

```bash
# 获取所有 open issue 及其 label
clawflow issue list --repo owner/repo --state open

# 获取最近的 comment（判断是否有新进展）
clawflow issue comment-list --repo owner/repo --issue 42

# 获取 PR 状态（CI 是否通过、review 状态）
clawflow pr list --repo owner/repo
```

### 2. 判断（Evaluate）

这是智能体的核心价值——做需要理解上下文的决策。几个典型判断模式：

**时间维度判断**：
- issue 创建超过 N 天没有 agent-evaluated → 可能被遗漏
- PR 开了超过 N 天没合并 → 可能需要催促或关闭
- 最近 N 天 bug 数量异常增长 → 可能需要暂停 feature 开发

**内容维度判断**：
- issue body 信息不足以评估 → 需要追问
- 多个 issue 描述的是同一个问题 → 需要去重
- feature request 和已有 issue 冲突 → 需要标记

**状态维度判断**：
- issue 有 agent-evaluated 但没人加 ready-for-agent → 可能需要提醒
- PR CI 失败了但没人修 → 可能需要重新触发或通知
- 某个 repo 的 agent-failed 比例异常高 → 算子可能需要调整

### 3. 行动（Act）

把判断结果翻译成固定层能消费的信号：

```bash
# 加 label → 触发对应算子
clawflow label add --repo owner/repo --issue 42 --label needs-triage

# 创建新 issue → 把发现的问题显式化
clawflow issue create --repo owner/repo --title "..." --body "..."

# 留 comment → 记录判断依据（可追溯）
clawflow issue comment --repo owner/repo --issue 42 --body "..."
```

### 4. 循环（Loop）

通过 `/loop` 保持持续运转。间隔取决于场景：

- **高频（5-15min）**：CI 监控、PR review 催促
- **中频（30min-1h）**：issue 状态巡检、优先级调整
- **低频（4h-1d）**：趋势分析、周报生成

---

## 实战：设计一个项目巡检 Skill

以下是一个完整的观察者 skill 设计示例——**项目健康巡检**。

### 目标

每 30 分钟扫描项目下所有 repo，识别需要干预的 issue/PR，自动执行对应动作。

### 触发方式

```
/loop 30m 执行项目巡检，扫描所有 repo 的 issue 和 PR 状态，识别异常并处理
```

### 巡检规则

| 条件 | 判断 | 动作 |
|------|------|------|
| issue 有 `bug` 但 3 天没有 `agent-evaluated` | 可能被遗漏 | 重新加 `bug` label 触发评估 |
| issue 有 `agent-evaluated` 超过 7 天没有 `ready-for-agent` | owner 可能忘了 review | 留 comment 提醒 |
| PR CI 失败超过 2 天 | 可能被遗忘 | 留 comment 提醒，标记 `needs-attention` |
| 同一 repo 一周内 `agent-failed` 超过 3 次 | 算子可能有问题 | 创建 issue 报告异常 |
| issue body 少于 50 字且没有代码块 | 信息不足 | 留 comment 追问细节 |
| 多个 open issue 标题相似度 > 80% | 可能重复 | 留 comment 标记疑似重复 |

### Skill 结构

```
~/.claude/skills/project-patrol/
└── SKILL.md
```

```markdown
---
name: project-patrol
description: "项目健康巡检 skill，通过 /loop 持续监控 issue/PR 状态"
---

# Project Patrol

你是一个项目健康巡检智能体。每次被唤醒时，执行以下检查流程。

## 检查流程

1. 获取所有启用的 repo 列表
2. 对每个 repo，检查 open issue 和 PR 的状态
3. 根据规则判断是否需要干预
4. 执行对应动作（加 label、留 comment、创建 issue）
5. 汇报本轮检查结果

## 规则（按优先级排序）

### P0 — 立即处理
- agent-failed 且没有人工跟进 → 重新触发或报告
- PR CI 失败超过 48h → 标记 needs-attention

### P1 — 当天处理
- issue 有触发 label 但 72h 没被消费 → 检查原因，必要时重新触发
- agent-evaluated 超过 7 天没有 ready-for-agent → 提醒 owner

### P2 — 观察
- 疑似重复 issue → 留 comment 标记
- issue 信息不足 → 追问

## 约束

- 每轮最多执行 5 个动作，避免刷屏
- 对同一个 issue 不要在 24h 内重复提醒
- 所有动作都要留 comment 说明原因（可追溯）
- 使用 clawflow 命令操作，不要用 gh
```

### 运行效果

```
[巡检 #1] 14:00
  扫描 3 个 repo，12 个 open issue，4 个 open PR
  发现：owner/backend#15 有 agent-evaluated 8 天未处理
  动作：留 comment 提醒 owner review 评估结果
  发现：owner/frontend#23 PR CI 失败 3 天
  动作：留 comment 提醒，加 needs-attention label

[巡检 #2] 14:30
  扫描 3 个 repo，12 个 open issue，4 个 open PR
  无异常

[巡检 #3] 15:00
  扫描 3 个 repo，11 个 open issue，3 个 open PR
  发现：owner/backend#15 owner 已加 ready-for-agent
  → 下次 clawflow run 会自动触发 implement 算子
```

---

## 进阶模式

### 模式一：级联触发

可变层创建 issue → 固定层消费 → 可变层观察结果 → 决定下一步。

```
观察者发现 repo 最近 bug 密度高
  → 创建 issue "近两周 bug 数量翻倍，建议暂停 feature 开发"
  → 加 label "needs-triage"
  → 固定层的 triage 算子评估并分类
  → 观察者在下一轮检查 triage 结果
  → 如果 triage 建议暂停，观察者给所有 feat issue 加 "on-hold" label
```

### 模式二：多 Skill 协作

不同 skill 关注不同维度，通过 label 隐式协作：

| Skill | 关注点 | 输出 |
|-------|--------|------|
| `project-patrol` | issue/PR 状态异常 | `needs-attention`, `stale`, `duplicate` |
| `quality-gate` | 代码质量指标 | `needs-refactor`, `test-coverage-low` |
| `release-watch` | 发布节奏 | `release-blocker`, `ready-to-ship` |

每个 skill 独立运行自己的 `/loop`，互不感知，但通过 label 形成协作网络。

### 模式三：自适应频率

观察者根据项目活跃度动态调整检查频率：

```
如果上一轮发现了异常 → 下一轮 10 分钟后检查（跟进）
如果连续 3 轮无异常 → 拉长到 1 小时
如果是工作日白天 → 30 分钟
如果是周末/夜间 → 2 小时
```

这正是 `/loop` 动态模式（不指定固定间隔）的设计意图——让智能体自己决定下次什么时候醒来。

---

## 设计原则

### 1. 固定层处理 80%，可变层处理 20%

大部分 issue 的生命周期是确定的：打标 → 评估 → 审批 → 实现 → 合并。这条路径用固定层的算子链就够了。可变层只处理偏离正常路径的情况。

### 2. 可变层只输出信号，不直接执行

观察者的输出永远是 label、comment、issue——不是代码修改、不是 PR 合并、不是部署。执行交给固定层，因为固定层的行为可预测、可审计、可回滚。

### 3. Label 是唯一的通信协议

不要让可变层直接调用固定层的内部 API。通过 label 解耦意味着：
- 可以随时替换任一层的实现
- 可以人工介入任何环节（手动加/删 label）
- 状态完全可见（看 label 就知道当前在哪一步）

### 4. 每个动作都要可追溯

观察者做的每个判断都应该留 comment 说明原因。这不是为了好看——是为了：
- 人工 review 时能理解为什么这么做
- 下一轮巡检时能避免重复动作
- 出问题时能回溯决策链

### 5. 宁可漏报，不要误报

观察者的判断阈值应该偏保守。一个被遗漏的 issue 最多晚处理几小时；一个误报的 "needs-attention" 会打断人的工作流，多几次就没人看了。

---

## 总结

| 维度 | 固定层 (`clawflow run`) | 可变层 (skill + `/loop`) |
|------|------------------------|--------------------------|
| 触发 | label 精确匹配 | 智能体判断 |
| 路径 | 确定，写在 frontmatter | 不确定，每次可能不同 |
| 成本 | 低（只跑匹配的算子） | 较高（需要理解上下文） |
| 频率 | 按需或定时 | 持续循环 |
| 输出 | label + comment + PR | label + comment + issue |
| 可预测性 | 高 | 中 |
| 适合场景 | 已知路径的执行 | 需要判断的决策 |

两层的关系不是替代，是互补。固定层给你效率和可靠性，可变层给你智能和适应性。通过 label 这个简单的协议把它们粘在一起，就是 ClawFlow 的全自动化方案。
