# Sub-agent 开发指南

本文说明 ClawFlow 中 sub-agent 的工作机制，以及如何开发和改进它的行为。

---

## Sub-agent 是什么

ClawFlow 的执行分两层：

```
Orchestrator（主 agent）
    ↓ spawn
Sub-agent（修复 agent）
```

- **Orchestrator** — 由 `SKILL.md` 定义，负责扫描 issue、评估、调度、清理
- **Sub-agent** — 由 orchestrator 在 Phase 4 spawn，专门负责在 worktree 中实现修复并提交 PR

Sub-agent 是一个独立的 Claude Code Task，运行在隔离的 git worktree 中，与主 agent 并发执行。

---

## Sub-agent 的工作流程

每个 sub-agent 收到一个 Task Prompt，按以下步骤执行：

```
ANALYZE     阅读代码，理解 issue 根因
    ↓
IMPLEMENT   最小化改动实现修复
    ↓
TEST_LOCAL  运行本地测试（失败则停止）
    ↓
COMMIT      git commit
    ↓
PUSH        git push origin fix/issue-{number}
    ↓
PR          gh pr create（使用标准模板）
    ↓
CI_WAIT     等待 GitHub CI（最长 10 分钟）
```

### 测试策略

Sub-agent 会自动检测项目类型并运行对应测试：

| 检测条件 | 运行命令 |
|---------|---------|
| `go.mod` 存在 | `go test ./...` |
| `package.json` 存在 | `npm test` |
| `pytest.ini` / `setup.py` 存在 | `pytest` |
| `Makefile` 含 `test` 目标 | `make test` |
| 以上均无 | 跳过（无测试） |

测试失败时 sub-agent 停止，不会推送代码。

---

## Sub-agent 的 Prompt 在哪里

Task Prompt Template 在 `skills/clawflow/SKILL.md` 的 **Phase 4.3** 部分。

Orchestrator 在 spawn sub-agent 时会将模板中的占位符替换为实际值：

| 占位符 | 来源 |
|--------|------|
| `{owner}/{repo}` | `clawflow harvest` 输出 |
| `{worktree_path}` | `clawflow worktree create` 输出 |
| `{number}` / `{title}` / `{body}` | issue 数据 |
| `{base_branch}` | `repos.yaml` 配置 |
| `{previous_attempts_context}` | `clawflow memory read` 输出 |

---

## 如何修改 Sub-agent 行为

所有 sub-agent 行为都由 `skills/clawflow/SKILL.md` 中的 Task Prompt Template 控制。

### 修改流程

```bash
# 1. 编辑 SKILL.md 中的 Task Prompt Template（Phase 4.3）
vim skills/clawflow/SKILL.md

# 2. 同步到 agent 目录（Claude Code）
cp skills/clawflow/SKILL.md ~/.claude/skills/clawflow/SKILL.md

# 3. 或者用 update 命令（从本地重建）
clawflow update --from-source
```

### 常见改进方向

**改变修复策略**：修改 `<instructions>` 中的步骤顺序或内容

**调整 PR 模板**：修改 `gh pr create` 的 `--body` 部分

**增加约束**：在 `<constraints>` 中添加规则，例如禁止修改某些文件

**改进上下文**：在 `<previous_attempts>` 之外注入更多上下文（如相关文件列表）

---

## 调试 Sub-agent

### 查看执行日志

```bash
# 实时查看 cron 触发的执行日志
tail -f ~/.clawflow/logs/cron.log

# 查看某个 issue 的历史处理记录
clawflow memory read --repo owner/repo --issue 7
```

### 查看 worktree 状态

```bash
# 列出当前所有活跃 worktree
git worktree list

# 进入某个 issue 的 worktree 手动检查
cd /tmp/clawflow-fix/owner-repo-issue-7
git log --oneline -5
```

### 手动触发单个 issue

在 Claude Code 中直接给 sub-agent 发 prompt（跳过 orchestrator）：

```
你是代码修复 agent。任务：修复 GitHub issue 并创建 PR。
仓库: owner/repo
Issue: #7
...（参考 SKILL.md Phase 4.3 的完整模板）
```

---

## 开发建议

**最小化改动原则**：sub-agent 的 prompt 明确要求最小化改动，避免引入无关重构。修改 prompt 时保持这个约束。

**测试先行**：修改 Task Prompt 后，先在一个低风险的测试仓库上跑一轮，确认行为符合预期再推广。

**利用 memory**：`clawflow memory write/read` 记录了每次尝试的结果，sub-agent 在重试时会读取历史记录。改进 prompt 时可以利用这个上下文让 agent 避免重复犯同样的错误。

**并发限制**：`repos.yaml` 中的 `max_concurrent_agents` 控制同时运行的 sub-agent 数量，默认 3。调试时建议设为 1，方便观察日志。

---

## 文件位置速查

| 文件 | 用途 |
|------|------|
| `skills/clawflow/SKILL.md` | Sub-agent Task Prompt 模板（源文件） |
| `~/.claude/skills/clawflow/SKILL.md` | 运行时加载的版本 |
| `~/.clawflow/logs/cron.log` | 执行日志 |
| `~/.clawflow/memory/repos/{owner}-{repo}/` | 每个 issue 的处理记录 |
| `/tmp/clawflow-fix/` | Worktree 默认存放位置 |
