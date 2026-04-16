# CLAUDE.md

## 语言偏好

始终用中文回复用户。用户正在学习中文。

---

## 工具规范

**禁止使用 `gh` CLI**，所有 VCS 操作（issue、PR、label、comment）统一使用 `clawflow` 命令：

```bash
clawflow issue list/create/comment/close
clawflow pr list/create/view/comment/ci-wait
clawflow label add/remove
```

`gh` 仅允许在 `clawflow update` 内部实现中使用（拉取 release assets），其他场景一律禁止。

---



每次新版本发布只需两步：

### 1. 打 Tag 并推送

```bash
git tag -a v{x.y.z} -m "{release message}"
git push origin v{x.y.z}
```

GitHub Actions（`.github/workflows/release.yml`）会自动完成：
- 三平台构建（darwin arm64/amd64、linux amd64）
- 创建 GitHub Release 并上传所有平台二进制

版本号规则：
- `v0.x.0` — 新功能（minor）
- `v0.x.y` — bug 修复（patch）
- `v1.0.0` — 首个稳定版

#### Release Tag 描述规范

Tag message 即 GitHub Release 的标题和正文，格式：

```
{一句话概括本版本核心变化}

## What's New
- {功能1}：{一句话说明用户能感知到的变化}
- {功能2}：{同上}

## Bug Fixes
- {修复1}：{影响范围}

## Breaking Changes（如有）
- {变更说明}：{迁移方式}
```

示例：

```
feat: GitLab 支持 + 自动克隆缺失仓库

## What's New
- GitLab 支持（v11.11+）：可监控 GitLab 仓库的 issue，自动评估并提 MR
- 自动克隆：repos.yaml 中配置的仓库本地路径不存在时自动 clone，无需手动操作
- 标签自动初始化：`clawflow repo add` 时自动在目标仓库创建所需标签

## Bug Fixes
- 修复 GitHub URL 格式化问题（normalize 处理 https/ssh 混用场景）
```

### 2. 验证用户可以自动更新

```bash
gh release view v{x.y.z}
# 确认 assets 列表有各平台文件（Actions 完成后）

# 模拟用户更新
clawflow update
# 应输出：binary updated + SKILL.md updated
```

---

## 用户更新流程

用户安装后，后续版本只需运行：

```bash
clawflow update           # 从 GitHub 下载最新 binary + 更新 SKILL.md
clawflow update --from-source  # 从本地 repo 重新构建（开发用）
```

`clawflow update` 自动读取 `~/.clawflow/config/install.yaml`（由 install.sh 写入），
知道 SKILL.md 装在哪个 agent 目录里，并自动更新。

---

## .gitignore 说明

| 规则 | 原因 |
|------|------|
| `/config/` | 用户配置（现存于 `~/.clawflow/config/`，不进 repo） |
| `clawflow` | 构建产物根目录二进制（通过 release asset 分发） |
| `clawflow_*` | 各平台构建产物 |

---

## Skills 构建规范

### 目录结构

```
skills/<skill-name>/
├── SKILL.md           # 必须，主流程入口
├── evaluation.md      # 详细评估策略、评论模板等
├── subagent-prompt.md # Sub-agent Task Prompt 模板
└── scripts/           # 可执行脚本（如有）
```

### SKILL.md 规范

- **控制在 500 行以内**，超出部分拆到独立文件
- 详细内容（评论模板、prompt 模板、评估维度）放到独立 `.md` 文件
- 在 SKILL.md 里用 Markdown 链接引用，说明文件内容和何时加载：
  ```markdown
  详见 [evaluation.md](evaluation.md)，包含：
  - Bug / Feature / 通用三种评估维度
  - 高/低置信度评论模板
  ```

### Frontmatter 关键字段

```yaml
---
name: skill-name
description: "触发条件描述，Claude 用此判断何时自动加载"
metadata:
  openclaw:
    requires:
      bins: ["git", "clawflow"]
    primaryEnv: "GH_TOKEN"
---
```

- `description` 前置核心触发词，总长度不超过 1536 字符
- `disable-model-invocation: true` — 只允许用户手动 `/skill-name` 触发（适合有副作用的操作）
- `allowed-tools` — 预授权工具，避免每次询问用户

### 拆分原则

| 放 SKILL.md | 放独立文件 |
|------------|-----------|
| 流程步骤、CLI 命令 | 评论/prompt 模板 |
| 安全约束、标签流程图 | 评估维度表格 |
| 文件引用说明 | 详细示例、参考文档 |
