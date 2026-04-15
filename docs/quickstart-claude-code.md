# 快速上手 — 基于 Claude Code

本文介绍如何在本地用 **Claude Code** 跑起 ClawFlow 完整流水线。

---

## 前置条件

| 工具 | 说明 |
|------|------|
| [Claude Code](https://claude.ai/code) | AI agent 运行环境，需登录 Claude 账号 |
| Go 1.22+ | 从源码安装时需要 |
| Node.js | claude-hud 插件依赖，需在系统 PATH 可见 |

```bash
# 验证环境
claude --version
go version

# 如果 Claude Code 启动时报 "env: node: No such file or directory"
# 是因为 Claude Code 不继承 shell PATH，需要将 node 链接到系统路径：
sudo ln -sf $(which node) /usr/local/bin/node
```

> ClawFlow v0.7+ 使用 REST API 直接与 GitHub/GitLab 通信，不再依赖 `gh` CLI。

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

## 第二步 — 配置认证 Token

**GitHub：**
```bash
clawflow config set-token ghp_xxxxxxxxxxxx
```
所需权限：`repo`（完整）、`read:org`。

**GitLab（自托管）：**
```bash
clawflow config set-gitlab-token glpat-xxxxxxxxxxxx
```
所需权限：`api`。

Token 保存到 `~/.clawflow/config/credentials.yaml`（权限 0600）。  
也可以通过环境变量传入（优先级更高）：

```bash
export GH_TOKEN=ghp_xxxxxxxxxxxx
export GITLAB_TOKEN=glpat-xxxxxxxxxxxx
```

验证配置：
```bash
clawflow config show
```

---

## 第三步 — 添加要监控的仓库

`repo add` 会自动识别平台，支持多种输入方式：

```bash
# GitHub
clawflow repo add https://github.com/your-org/your-repo
clawflow repo add your-org/your-repo --local-path ~/github/your-repo

# GitLab 自托管（支持嵌套 namespace）
clawflow repo add https://gitlab.company.com/ns/group/repo

# 本地目录 — 自动读取 .git/config 的 origin URL
clawflow repo add .
clawflow repo add ~/github/your-repo
```

`--local-path` 是仓库在本地的路径，ClawFlow 会在这里创建 worktree 来隔离每个 issue 的修复工作。使用本地路径方式时会自动填充。

查看配置：

```bash
clawflow status
```

---

## 第四步 — 初始化 Labels

`repo add` 会自动在仓库创建所需标签。也可以手动执行：

```bash
clawflow label init your-org/your-repo
```

创建的标签：

| Label | 含义 |
|-------|------|
| `ready-for-agent` | Owner 审批通过，触发修复流水线 |
| `agent-evaluated` | ClawFlow 已评估此 issue |
| `in-progress` | Agent 正在处理 |
| `agent-skipped` | 置信度低，需要更多信息 |
| `agent-failed` | Agent 尝试失败 |

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
# 1. 创建 issue（可用 CLI 快速创建测试 issue）
clawflow issue create --repo your-org/your-repo --title "bug: something broken" --body "details..."

# 2. 在 Claude Code 中触发评估
ClawFlow run

# 3. 查看 ClawFlow 在 issue 下的评估评论
#    确认方案后，手动添加 ready-for-agent 标签

# 4. 再次运行，触发自动修复
ClawFlow run

# 5. 查看生成的 PR，review 后合并
```

---

## 常用命令

```bash
# 查看所有仓库状态
clawflow status

# 列出 open issues
clawflow issue list --repo owner/repo

# 手动管理标签
clawflow label add --repo owner/repo --issue 7 --label ready-for-agent
clawflow label remove --repo owner/repo --issue 7 --label in-progress

# 重试失败的 issue
clawflow retry --repo owner/repo --issue 7

# 更新 ClawFlow 到最新版
clawflow update
```

---

## 下一步

- [配置多仓库监控](../readme.md#setup)
- [CLI 完整参考](../readme.md#cli-reference)
