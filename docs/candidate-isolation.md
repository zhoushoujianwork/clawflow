# 本地开发候选与独立验收的宿主隔离

状态：实现位于 PR #617（#613），仍需独立验收。范围：CLI 与后台服务；不涉及 Web UI。原生 App 仅提供独立的 bundle id 与产物路径，GUI 目视验收仍须在明确的测试窗口内另行执行。

## 目标

在同一台 Mac 上运行 ClawFlow 开发、独立验收与测试链，而不影响正在运行的稳定 CLI、controller、worker 与 macOS App。仅设置 worker 的 `--state-dir` 不够：显式 clone 路径、配置、凭据、端口、数据库与 launchd 标签仍可能共享。候选环境因此按固定提交整体隔离。

## 候选环境（`clawflow candidate`）

每个候选由完整 40 位提交 SHA 推导身份 `id = sha256("clawflow-candidate\0"+commit)[:12]`，因此同一提交总得到同一环境，不同提交互不冲突。

| 资源 | 候选值 | 与稳定宿主的区别 |
| --- | --- | --- |
| 根目录 | `<cache>/ClawFlow/candidates/<id>` | 不在 `config.RootDir()` 之内，也不包含它 |
| HOME / 配置 | `<root>/home`（`.clawflow/config`） | 不读取稳定凭据；不生成 `credentials.yaml` |
| 状态 / 锁 / worktree | `<root>/state`、`state/locks`、`state/worktrees` | 独立 `CLAWFLOW_STATE_DIR` |
| 评审 worker 会话 | `<root>/state-review` | 与开发 worker 不嵌套、不相等 |
| 缓存 / 临时 | `<root>/cache`、`<root>/tmp` | 独立 `GOCACHE`、`TMPDIR` |
| 监听端口 | 提交推导的 20000–39999，仅 `127.0.0.1` | 拒绝稳定端口（8090、8093、5432 及配置的监听端口）与 1024 以下端口 |
| 数据库 | schema `cf_cand_<id>`，DSN 以 `search_path` 固定 | 连接未固定到该 schema 即被拒绝 |
| launchd 标签 | `com.zhoushoujianwork.clawflow.dev.<id>.<kind>` | 稳定 App 与 CLI 都不管理 `.dev.` 标签，反之亦然 |
| App bundle id | `com.clawflow.dashboard.dev.<id>` | 偏好域（UserDefaults）因此独立 |
| App 产物 | `<root>/app/ClawFlow Dev.app` | 拒绝 `macos/dist`、`/Applications`、`~/Applications` |

### 命令

```sh
# 1. 固定的 SOP 与能力：先检查，缺失即返回 blocked 与 next_owner，不执行任何变更
clawflow candidate prepare --commit <sha> --repo owner/repo \
  --sop-required --sop-digest <sha256-of-testing.md> \
  --capability fixture-runtime --machine-capability fixture-runtime \
  --fixture owner/repo=http://127.0.0.1:<fixture-port>

# 2. 在一次性 checkout（HEAD 必须等于 <sha> 且无改动）上构建并启动开发 controller 与两个 worker
export CLAWFLOW_CANDIDATE_ADMIN_DSN=...   # 仅经环境变量传入；不会写入任何文件或证据
clawflow candidate up --commit <sha> --checkout <disposable-worktree>

# 3. 停止（仅停止清单中仍属于本候选的进程），对比稳定宿主，写证据
clawflow candidate down --commit <sha> --offline passed

# 4. 独立评审：校验证据属于被评审的提交
clawflow candidate verify <root>/evidence.json --commit <sha>

# 5. 清理：仅删除自己的根目录与 schema
clawflow candidate clean --commit <sha>
```

`CLAWFLOW_CANDIDATE_ADMIN_DSN` 未设置时 `up` 返回 blocked（environment-maintainer），不会伪造通过。

候选构建以只读模块模式执行，并在构建后再次核对提交和工作区。每次 `up` 都先归档旧证据，再创建本轮记录；启动失败不会沿用上轮成功。运行中的候选拒绝重复启动。`verify` 会根据各层结果重新计算结论，不只读取 `status`。

### 冲突与所有权检查（变更之前）

`Plan.Validate` 与 `Prepare` 在创建任何目录之前拒绝：根目录与稳定根重叠、目录逃出候选根、稳定端口或已占用端口、同一基目录下其他候选已占用的端口/schema/bundle id、非候选 schema、稳定 bundle id、稳定 launchd 标签、已存在但不是候选的目录。每个候选根含 `manifest.json` 与随机令牌的 `.clawflow-candidate` 标记，二者一致才视为本候选所有。

### 崩溃清理仅针对自有资源

`candidate down/clean` 只向清单中记录的进程发信号，并且要求 (a) PID 仍存活，(b) 当前命令行以记录的可执行路径开头，(c) 该路径位于候选 `bin/` 之下。被复用的 PID、位于候选之外的稳定二进制、无关进程均不会被触及。`clean` 拒绝：令牌不匹配、根目录是符号链接、清单中仍有存活进程。schema 仅在本候选记录过创建时才删除。

## 运行时守卫（`internal/devmode`）

进程环境含 `CLAWFLOW_CANDIDATE_ID` 即进入候选模式；标识格式不合法时失败关闭（拒绝一切）。守卫位于出口点而非调用约定：

- **HTTP**：`http.DefaultTransport` 被替换为仅允许回环地址，覆盖所有 VCS/更新/同步客户端及每一次重定向。
- **仓库**：配置加载时，非回环 `base_url`（含默认 github.com）的仓库被禁用，clone 不在候选根内的仓库被禁用，`auto_merge`、`self_upgrade`、GitHub App 身份一律清除；`newVCSClient`、API 与 Pilot 的客户端构造再次拒绝真实地址。
- **子进程**：清除 `GH_TOKEN`、`GITHUB_TOKEN`、`GITLAB_TOKEN`、`SSH_AUTH_SOCK`，`GIT_ALLOW_PROTOCOL=file`。
- **监听**：coordinator 仅可绑定回环地址上的候选端口。
- **数据库**：`store.Open` 要求 DSN 固定到候选 schema。
- **宿主变更命令**：`update`、`sync`、`login`、`web`、`candidate`、`__respawn` 被拒绝；稳定 launchd 服务封装与自升级的二进制替换路径同样拒绝。

默认情况下候选配置不启用任何仓库（`ConfigOptions.Dispatch=false`）、`run_interval_minutes=0`、`auto_merge=false`，因此夹具不会启动重复工作或写回。

## 开发通道与独立验收通道

沿用既有的 `operator` / `acceptance` 角色与 `acceptance_worker: different` 路由，不新增角色：

| 通道 | 角色 | 标签 | 会话目录 | 并发 |
| --- | --- | --- | --- | --- |
| 开发 | `operator` | `candidate-<id>-dev` | `state/worker-dev` | `--max-parallel 1` |
| 评审 | `acceptance` | `candidate-<id>-review` | `state-review/worker-review` | `--max-parallel 1` |

两者各自创建 token（身份不同：`dev-<id>`、`review-<id>`），`--run-untagged=false`，仓库 `max_in_flight: 1`，`max_concurrent_agents: 2`。`CheckIndependence` 拒绝身份、会话目录（含嵌套）或通道标签相同，以及角色错配。评审使用同一固定提交构建的二进制。

### SOP 与能力的一致应用

`--sop-required/--sop-digest/--capability` 同时写入候选仓库配置（`testing_sop_*`、`required_capabilities`）与两个 worker 的 `--capabilities`，由既有 #603 机制在开发与验收两端以同一摘要校验。`CheckReadiness` 在启动前核对本机 SOP 摘要与能力；缺失返回 `blocked`、缺项列表、`next_owner`（`project-maintainer` 或 `environment-maintainer`）与 `next_action`。离线通过不能替代这些检查。

### 生产 worker 的配置时机

本文所述候选环境不更改任何现有 worker 或仓库配置。要把受支持的本地 worker 配到稳定宿主上，须在 `candidate prepare` 与 `candidate down` 的证据均为 `passed` 之后，由机器维护者在没有运行中任务的窗口内手工执行；本变更不自动改动稳定配置。

## 证据

`evidence.json`（权限 0600）绑定：候选提交、运行时身份（id、端口、schema、bundle id）、环境（OS、架构、Go 版本、候选二进制 SHA-256）、SOP 摘要与能力、各层结果、worker 身份、命令及结果、稳定宿主差异、状态。令牌、DSN 与密码在写入前脱敏。

层级：`offline`、`environment-ready`、`candidate-loaded`、`lifecycle`、`live-write`、`save-reload`、`native-visual`。`live-write`/`save-reload` 默认 `not-run`，`native-visual` 默认 `pending`；任何一层都不由其他层推断。阻塞之后的步骤保持 `not-run`，整体状态为 `blocked`。稳定宿主的配置、数据（不含 `runs/` 历史）、监听端口或进程身份在运行前后不一致时，整体状态为 `failed`。配置与数据比较实际内容和文件类型/权限；单纯触碰修改时间不会被误判为内容变更。旧记录保存在候选根下的 `attempt-*` 目录中。

## 原生 App 与 GUI

`CLAWFLOW_CANDIDATE_ID=<12 hex> CLAWFLOW_CANDIDATE_OUT=<绝对路径> CLAWFLOW_CANDIDATE_CLI=<候选 clawflow> macos/build-app.sh` 构建 `ClawFlow Dev.app`，bundle id 为 `com.clawflow.dashboard.dev.<id>`。脚本在创建任何目录前拒绝 `macos/dist`、`/Applications`、`~/Applications`（含符号链接后的路径）以及稳定 bundle id。该 App 只管理自己的 `.dev.<id>.` 标签，启动时不自动拉起服务，且不回落到已安装的 CLI。构建脚本不启动也不安装 App。

自动验收不得打开、聚焦或操作桌面，不得读写剪贴板，也不得替换已安装的 App/CLI。原生目视检查在没有明确测试窗口时记录为 `pending`。

## 仍保留的门禁

必需 CI（`ci.yml`）与独立验收门禁不变。本地结果不是 GitHub Actions 的运行结果，候选证据也不替代它们。涉及 PostgreSQL 的测试在没有 `CLAWFLOW_TEST_DATABASE_DSN` 时被跳过，以精确提交上的 CI 为准。

## 限制

- 稳定宿主快照只读取文件、探测端口与 `ps`；未覆盖稳定宿主上由其他用户启动的进程。
- 本变更不实现跨机器的权威租约，也不为候选自动安装 launchd 服务；`LabelPrefix`/`ServiceLabel` 只定义命名空间并由 App 与 CLI 强制。
- 隔离是对协作程序的约束：恶意子进程若自行构造 HTTP 客户端并显式设置 `Transport`，仍可绕过进程内传输守卫；git 与 offload 子进程通过清除的环境变量与 `GIT_ALLOW_PROTOCOL` 约束。

## 升级

候选通过隔离验证后，使用 `clawflow upgrade` 滚动升级已安装的 controller 与 worker，见 [controller-upgrade.md](controller-upgrade.md)。
