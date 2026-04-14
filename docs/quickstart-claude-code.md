# 快速上手 — 基于 Claude Code

本文介绍如何在本地用 **Claude Code** 跑起 ClawFlow 完整流水线。

---

## 前置条件

| 工具 | 说明 |
|------|------|
| [Claude Code](https://claude.ai/code) | AI agent 运行环境，需登录 Claude 账号 |
| [GitHub CLI](https://cli.github.com/) | ClawFlow 所有 GitHub 操作依赖 `gh` |
| Go 1.22+ | 从源码安装时需要 |
| Node.js | claude-hud 插件依赖，需在系统 PATH 可见 |

```bash
# 验证环境
claude --version
gh auth status
go version

# 如果 Claude Code 启动时报 "env: node: No such file or directory"
# 是因为 Claude Code 不继承 shell PATH，需要将 node 链接到系统路径：
sudo ln -sf $(which node) /usr/local/bin/node
```

---

## 第一步 — 安装 ClawFlow

```bash
git clone https://github.com/zhoushoujianwork/clawflow
cd clawflow
./install.sh --agent claude
```

安装完成后验证：

```bash
clawflow --help
```

> `install.sh` 会自动将 SKILL.md 写入 `~/.claude/skills/clawflow/`，Claude Code 下次启动时自动加载。

---

## 第二步 — 配置 GitHub 认证

```bash
# 登录 GitHub CLI（如未登录）
gh auth login

# 验证
gh auth status
```

ClawFlow 直接复用 `gh` 的认证，无需单独配置 token。

---

## 第三步 — 添加要监控的仓库

```bash
clawflow repo add your-org/your-repo --base main --local-path ~/github/your-repo
```

`--local-path` 是仓库在本地的路径，ClawFlow 会在这里创建 worktree 来隔离每个 issue 的修复工作。

查看配置：

```bash
clawflow status
```

---

## 第四步 — 创建 GitHub Labels

```bash
./install.sh --create-labels your-org/your-repo
```

这会在目标仓库创建 ClawFlow 所需的标签（`ready-for-agent`、`agent-evaluated` 等）。

---

## 第五步 — 配置 Claude Code 权限

为了让 sub-agent 在执行修复时不被权限弹窗打断，在项目的 `.claude/settings.json` 中预授权：

```json
{
  "permissions": {
    "allow": [
      "Edit",
      "Write",
      "Bash(git:*)",
      "Bash(gh:*)",
      "Bash(go:*)",
      "Bash(make:*)",
      "Bash(clawflow:*)"
    ]
  }
}
```

> 这是项目级配置，只对当前仓库生效，不影响全局权限。

---

## 运行

在 Claude Code 中输入：

```
ClawFlow run
```

ClawFlow 会自动执行完整流水线：

```
扫描仓库 issues
    ↓
评估新 issue → 发表评论 + 添加 agent-evaluated 标签
    ↓
等待你手动添加 ready-for-agent 标签（owner 审批）
    ↓
spawn sub-agent → 在 worktree 中实现修复 → 提交 PR
    ↓
清理 worktree
```

---

## 典型工作流

```bash
# 1. 在 GitHub 上创建 issue，描述 bug 或功能需求

# 2. 在 Claude Code 中触发评估
ClawFlow run

# 3. 查看 ClawFlow 在 issue 下的评估评论
#    确认方案后，手动在 GitHub 上添加 ready-for-agent 标签

# 4. 再次运行，触发自动修复
ClawFlow run

# 5. 查看生成的 PR，review 后合并
```

---

## 常用命令

```bash
# 查看所有仓库状态
clawflow status

# 只处理某个仓库
# 在 Claude Code 中：ClawFlow run（会自动读取 repos.yaml）

# 手动管理标签
clawflow label add --repo owner/repo --issue 7 --label ready-for-agent
clawflow label remove --repo owner/repo --issue 7 --label in-progress

# 更新 ClawFlow 到最新版
clawflow update
```

---

## 下一步

- [基于 OpenClaw 运行](quickstart-openclaw.md)（即将更新）
- [配置多仓库监控](../readme.md#setup)
- [自定义评估阈值](../readme.md#cli-reference)
