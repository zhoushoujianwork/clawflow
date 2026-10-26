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

> 内置算子(`implement`、`reply-comment`、`track-progress`)随 binary 一起分发(embed.FS),不需要单独安装到 agent 目录。

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

普通 open leaf issue 会直接进入 `implement`，无需分类、独立评估或人工批准标签。实现过程中内部完成分析、计划、测试和修复。Question/comment 保留回复路径，父任务通过 `progress-check` 触发 `track-progress`；父任务不会直接执行实现。

状态标签包括 `agent-implemented`（已开 PR）、`agent-failed`（失败待检查）、`agent-skipped`（跳过）和 `agent-replied`（已回复）。并发控制使用 `~/.clawflow/locks/` 的本地锁文件；不要通过删除远端标签绕过锁。

## 运行与交付

人负责方向和需求，ClawFlow 在配置范围内自主实现与交付：

```bash
clawflow repo set your-org/your-repo --auto-merge on --ci-required on
clawflow repo enable your-org/your-repo
clawflow issue create --repo your-org/your-repo --title "bug: something broken" --body "details..."
clawflow run --repo your-org/your-repo
clawflow pr list --repo your-org/your-repo --state open
```

一次扫描所有符合条件的工作；可用 cron / launchd 定时执行。需要 CI 时，只有成功结果允许合并，缺失、等待、失败或不可读都不会放行。暂停、取消、机器绑定、终态排除和重试上限仍然生效。

失败 CI 的自动修复仅支持同仓库 GitHub 的受管 PR：本地复现测试失败并正常推送到原分支，每个 PR 最多三次尝试，间隔五分钟。不会读取 CI 日志，不支持 fork 或 GitLab 自动修复；GitLab 其他 issue/MR/CI 功能继续保留。

## 迁移与扩展

只有一条自主生命周期，没有模式选择器。旧配置仍可读取；显式 legacy 选择会提示迁移并按自主流程执行。重写配置会去除 `workflow`、`auto_approve`、`auto_evaluate_all_issues` 和 `confidence_threshold`，保留启用状态、绑定、合并与 CI 等有效设置。

`classify`、`evaluate-bug`、`evaluate-feat`、`decompose` 已退役，同名用户覆盖也会被拒绝。其他自定义算子、历史标签和记录保留；eval model 角色仅供自定义 `evaluate-*` 扩展使用，不构成内置评估流水线。

```bash
clawflow operators list
clawflow issue list --repo owner/repo
clawflow update
```

当前 runner 不等同于拟议的持久任务引擎，详见[托管开发设计](managed-development-design.md)。

---

## 下一步

- [配置多仓库监控](../readme.md#setup)
- [算子架构与 schema](../CLAUDE.md#算子规范)
