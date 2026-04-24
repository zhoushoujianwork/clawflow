# 快速上手 — 基于 Claude Code

本文介绍如何在本地用 **Claude Code** 跑起 ClawFlow 的算子流水线。

---

## 前置条件

| 工具 | 说明 |
|------|------|
| [Claude Code](https://claude.ai/code) | ClawFlow 的每个算子都通过 `claude -p` 执行,需登录 Claude 账号 |
| Go 1.22+ | 从源码安装时需要 |
| Node.js | claude-hud 插件依赖,需在系统 PATH 可见 |

```bash
# 验证环境
claude --version
go version

# 如果 Claude Code 启动时报 "env: node: No such file or directory"
# 是因为 Claude Code 不继承 shell PATH,需要将 node 链接到系统路径:
sudo ln -sf $(which node) /usr/local/bin/node
```

> ClawFlow 使用 REST API 直接与 GitHub/GitLab 通信,不依赖 `gh` CLI。

---

## 第一步 — 安装 ClawFlow

```bash
git clone https://github.com/zhoushoujianwork/clawflow
cd clawflow
./install.sh              # 从源码编译 + 装到 ~/.clawflow/bin/clawflow
```

或者直接下载预编译二进制(不需要 Go 工具链):

```bash
curl -fsSL https://raw.githubusercontent.com/zhoushoujianwork/clawflow/main/get.sh | bash
```

安装完成后验证:

```bash
clawflow --help
clawflow operators list     # 确认内置算子已加载
```

> 内置算子(`evaluate-bug`、`evaluate-feat`、`implement`、`reply-comment`)随 binary 一起分发(embed.FS),不需要单独安装到 agent 目录。

---

## 第二步 — 配置认证 Token

**GitHub:**
```bash
clawflow config set-token ghp_xxxxxxxxxxxx
```
所需权限:`repo`(完整)、`read:org`。

**GitLab(自托管):**
```bash
clawflow config set-gitlab-token glpat-xxxxxxxxxxxx
```
所需权限:`api`。

Token 保存到 `~/.clawflow/config/credentials.yaml`(权限 0600)。也可以通过环境变量传入(优先级更高):

```bash
export GH_TOKEN=ghp_xxxxxxxxxxxx
export GITLAB_TOKEN=glpat-xxxxxxxxxxxx
```

验证配置:
```bash
clawflow config show
```

---

## 第三步 — 添加要监控的仓库

`repo add` 会自动识别平台,支持多种输入方式:

```bash
# GitHub
clawflow repo add https://github.com/your-org/your-repo
clawflow repo add your-org/your-repo --local-path ~/github/your-repo

# GitLab 自托管(支持嵌套 namespace)
clawflow repo add https://gitlab.company.com/ns/group/repo

# 本地目录 — 自动读取 .git/config 的 origin URL
clawflow repo add .
clawflow repo add ~/github/your-repo
```

`--local-path` 是仓库在本地的路径,需要跑 `implement` 算子时 ClawFlow 会在这里执行构建和测试。使用本地路径方式时会自动填充。

查看配置:

```bash
clawflow repo list
```

---

## 第四步 — 初始化 Labels

`repo add` 会自动在仓库创建所需标签。也可以手动执行:

```bash
clawflow label init your-org/your-repo
```

标签分两类。**触发类** —— 决定哪个算子会跑:

| Label | 触发算子 |
|-------|------|
| `bug` | `evaluate-bug` |
| `feat` | `evaluate-feat`(规划中) |
| `ready-for-agent` | `implement`(你手动添加,表示同意修复方案) |
| `agent-mentioned` | `reply-comment` |

**状态类** —— 算子写回的标记:

| Label | 含义 |
|-------|------|
| `agent-running` | 算子正在运行,并发锁 |
| `agent-evaluated` | 评估算子已完成,避免重复评估 |
| `agent-skipped` | 置信度低,算子拒绝继续 |
| `agent-implemented` | `implement` 完成,PR 已开 |
| `agent-failed` | 某个算子执行失败 |
| `agent-replied` | `reply-comment` 已回复 |

---

## 运行

```bash
clawflow run
```

一次性扫描所有启用的仓库,对每个 open issue/PR 匹配已注册算子,命中即执行。流程:

```
clawflow run
   → 扫到带 bug 标签的 issue
     → evaluate-bug 算子给出评估 comment + agent-evaluated 标签
   → 你阅读评估,手动加 ready-for-agent 标签
     → 下一次 clawflow run
       → implement 算子创建分支、写代码、开 PR + agent-implemented 标签
```

想定时跑就挂 cron / launchd;想即时跑就在终端执行。ClawFlow 自身不保持后台进程。

---

## 典型工作流

```bash
# 1. 创建一个测试 issue 并打上 bug 标签
clawflow issue create --repo your-org/your-repo --title "bug: something broken" --body "details..."
clawflow label add --repo your-org/your-repo --issue 7 --label bug

# 2. 触发评估
clawflow run

# 3. 查看 ClawFlow 在 issue 下的评估 comment
#    确认方案后,手动添加 ready-for-agent 标签
clawflow label add --repo your-org/your-repo --issue 7 --label ready-for-agent

# 4. 再次运行,触发 implement 算子
clawflow run

# 5. 查看生成的 PR,review 后合并
clawflow pr view --repo your-org/your-repo <pr-number>
```

---

## 常用命令

```bash
# 看已注册的算子
clawflow operators list

# 列出 open issues
clawflow issue list --repo owner/repo

# 手动管理标签
clawflow label add --repo owner/repo --issue 7 --label ready-for-agent
clawflow label remove --repo owner/repo --issue 7 --label agent-running

# 更新 ClawFlow 到最新版
clawflow update
```

---

## 下一步

- [配置多仓库监控](../readme.md#setup)
- [算子架构与 schema](../CLAUDE.md#算子规范)
