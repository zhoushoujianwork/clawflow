# CLAUDE.md

## 工具规范

**禁止使用 `gh` CLI**，所有 VCS 操作（issue、PR、label、comment）统一使用 `clawflow` 命令：

```bash
clawflow issue list/create/comment/close
clawflow pr list/create/view/comment/ci-wait
clawflow label add/remove
```

`gh` 仅允许在 `clawflow update` 内部实现中使用（拉取 release assets），其他场景一律禁止。

---

## 算子规范

ClawFlow 的核心设计:**一切可扩展单元都是算子 (operator)**。要改变行为,通常是写一个算子,不是改 Go 代码。

### 什么是算子

算子 = 一次有明确输入输出的 `claude -p` 调用,表现为一个 SKILL.md 文件:

- **输入** = issue/PR 当前状态(label + body + comments)
- **输出** = label 变更 + comment + 可选 PR
- **不直接跟其他算子通信**,只通过 VCS(label/comment)交接 —— 算子 A 写出的 label 正是算子 B 的触发 label

### 目录约定

| 位置 | 用途 | 优先级 |
|---|---|---|
| `skills/<name>/SKILL.md`(repo 内) | 内置算子,通过 `embed.FS` 打进二进制 | 低 |
| `~/.clawflow/skills/<name>/SKILL.md` | 用户自定义算子 | 高(同名覆盖内置) |

### Frontmatter schema

每个算子的 SKILL.md 顶部:

```yaml
---
name: evaluate-bug
description: "一句话说明算子做什么、何时会被触发"
operator:
  trigger:
    target: "issue"                                           # "issue" | "pr"
    labels_required: ["bug"]                                  # 必须全部存在(AND)
    labels_excluded: ["agent-evaluated", "agent-skipped", "agent-running"]  # 任一存在即跳过(OR NOT)
  lock_label: "agent-running"                                 # 执行前加、完成/失败后删,并发锁
---

# (frontmatter 后是喂给 claude 的 prompt 正文)
```

**字段说明**:

| 字段 | 含义 |
|---|---|
| `name` | 唯一标识,必须与目录名一致 |
| `description` | 人读说明,也用于 `clawflow operators list` |
| `operator.trigger.target` | 扫哪类对象。`issue` 或 `pr` |
| `operator.trigger.labels_required` | 必须**全部**存在才触发(AND) |
| `operator.trigger.labels_excluded` | 有任意一个就跳过(OR NOT) |
| `operator.lock_label` | 并发锁,执行前加、完成后删 |

**故意不做的事**(第一版 schema 的边界):

- 不声明 output(不枚举"加哪些 label / 发哪些 comment")—— 算子自己用工具做,声明式反而限制表达力
- 不支持 `timeout` 字段 —— 由 CLI 全局配置(默认 60 分钟)
- 不支持多平台过滤 —— 一个算子对 GitHub/GitLab 都适用
- 不支持 body 正则匹配 —— label 匹配已足够

### 如何新增算子

1. 在 `skills/<name>/SKILL.md` 创建目录和文件
2. 写 frontmatter(name / description / trigger / lock_label)
3. 正文写给 Claude 的指令(自然语言 prompt)
4. `go build` 重新编译二进制(内置算子嵌入在 binary 里)
5. 本地 `clawflow run` 在测试仓库上验证

用户算子不需要 `go build` —— 直接放在 `~/.clawflow/skills/` 下,下次 `clawflow run` 自动加载。

### 算子设计原则

- **幂等** —— 多次运行结果应一致。靠 `labels_excluded` 跳过已处理对象
- **自包含** —— 一个算子只做一件事。拆细不拆粗
- **通过 label 协同** —— 不要让算子 A 假设算子 B 存在;隐式串联靠 label 流转
- **失败写 comment** —— 算子失败时应在 issue 留一条 comment 说明原因,runner 负责加 `agent-failed`

---

## Release 流程

每次新版本发布只需两步:

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

### 2. 验证用户可以自动更新

```bash
gh release view v{x.y.z}
# 确认 assets 列表有各平台文件(Actions 完成后)

# 模拟用户更新
clawflow update
# 应输出:binary updated
```

---

## 用户更新流程

用户安装后,后续版本只需运行:

```bash
clawflow update                   # 从 GitHub 下载最新 binary
clawflow update --from-source     # 从本地 repo 重新构建(开发用)
```

内置算子随 binary 一起分发（通过 `embed.FS`），binary 更新即算子更新。用户放在 `~/.clawflow/skills/` 下的自定义算子不会被 update 覆盖。

---

## .gitignore 说明

| 规则 | 原因 |
|------|------|
| `/config/` | 用户配置（现存于 `~/.clawflow/config/`，不进 repo） |
| `clawflow` | 构建产物根目录二进制（通过 release asset 分发） |
| `clawflow_*` | 各平台构建产物 |

---

## Skills 构建规范

算子的 schema 见上方"算子规范"。本节是跨算子通用的写作规范。

### 目录结构

每个算子一个目录:

```
skills/<operator-name>/
├── SKILL.md           # 必须,frontmatter + prompt 正文
├── <extras>.md        # 可选:评估模板、维度表等,SKILL.md 过长时拆出来
└── scripts/           # 可选:算子调用的辅助脚本
```

### SKILL.md 规模

- **控制在 500 行以内**,超出部分拆到独立文件
- 详细内容(评论模板、prompt 模板、评估维度)放到独立 `.md` 文件
- SKILL.md 里用 Markdown 链接引用,并说明何时加载:
  ```markdown
  详见 [evaluation.md](evaluation.md),包含 Bug/Feature 评估维度与评论模板
  ```

### 拆分原则

| 放 SKILL.md | 放独立文件 |
|------------|-----------|
| 算子 frontmatter、触发说明 | 评论/prompt 模板 |
| 流程步骤、CLI 命令 | 评估维度表格 |
| 安全约束、核心指令 | 详细示例、参考文档 |

---

## 商业版

此 repo 曾配套过一个闭源的 SaaS 协同方向(`clawflow-saas`,Rust):CLI 作为 worker 连 SaaS 后端拉任务、上报 token 用量、走计费。该方向已废弃,本仓库完全独立,不再依赖任何后端。

如果半年后翻 git log 发现一堆 `worker_*.go`、`/api/v1/worker/...`、`pipeline_run` 字样的历史提交,那就是这段。`clawflow-saas` 代码保留在另一个目录里仅供参考,不再活跃维护。
