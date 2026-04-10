# ClawFlow 项目

自动化 Issue → 修复 → PR 流水线，挂在 OpenClaw 上运行。

## 项目结构

```
clawflow/
├── config/
│   ├── repos.yaml          # 已接入的仓库配置
│   ├── labels.yaml         # 标签定义
│   └── settings.yaml       # 全局设置
├── skills/
│   └── clawflow/
│       ├── SKILL.md        # Agent 技能定义（核心逻辑）
│       └── scripts/        # 辅助脚本（预留）
├── memory/
│   └── repos/              # 各仓库的 issue 处理记录
└── docs/
    └── README.md           # 本文档
```

## 快速开始

### 1. 添加新仓库

编辑 `config/repos.yaml`，添加：

```yaml
repos:
  owner/repo:
    enabled: true
    base_branch: main
    owner: owner
    description: 项目描述
    added_at: 2026-04-10
```

### 2. 配置 GitHub 标签

在目标仓库创建以下标签：
- `ready-for-agent` (绿色) - 触发流水线
- `in-progress` (橙色) - 处理中
- `agent-skipped` (灰色) - 已跳过
- `agent-failed` (红色) - 处理失败

### 3. 设置 Webhook（可选）

如果需要实时触发（而不是 cron 轮询），配置 GitHub webhook：
- Payload URL: `https://your-openclaw/webhook/clawflow`
- Events: Issues, Pull requests

### 4. 触发修复

在 GitHub issue 上添加 `ready-for-agent` 标签，agent 会自动：
1. 收割 issue
2. 评估可行性
3. Spawn sub-agent 修复
4. 创建 PR 并通知

## 运维命令

```bash
# 检查 ClawFlow 状态
# 在 OpenClaw 中说："检查 ClawFlow 状态"

# 手动触发收割
# 在 OpenClaw 中说："收割 ready-for-agent"

# 查看某仓库的处理记录
cat memory/repos/<owner>-<repo>/issue-<number>.md
```

## 持续迭代

### 需要改进的地方

1. **置信度评估算法**：目前是简单的分数，可以加入：
   - 代码相似度分析
   - 历史 issue 匹配
   - 自然语言理解深度

2. **并行处理**：目前串行处理 issue，可以：
   - 使用 `max_concurrent_agents` 配置
   - 实现队列和调度器

3. **回滚机制**：PR 被 reject 后：
   - 记录失败原因
   - 学习避免类似错误

4. **实时触发**：从 cron 轮询升级到 webhook：
   - 降低延迟
   - 减少 API 调用

### 如何贡献

1. 编辑 `skills/clawflow/SKILL.md` 改进核心逻辑
2. 更新 `config/repos.yaml` 添加新仓库
3. 提交 PR 到本项目

---

*ClawFlow - 让 Agent 爪子自动抓 bug 🦀*