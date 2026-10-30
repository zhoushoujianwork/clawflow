# Worker pool 存储细节

返回 [主设计](worker-pool-design.md)。

## 平台主机与客户端初始化

平台管理员先按 [PostgreSQL 设置](storage-design.md) 配置数据库并运行 `clawflow db migrate`。
在平台主机配置（不要在客户端设置 coordinator 来绕过平台连接）：

```yaml
settings:
  coordinator:
    enabled: true
    listen: ":8093"
    local_worker:
      enabled: true
      max_parallel: 1
      tags: [github]
```

平台主机的 `clawflow web` 提供已有调度器和独立 HTTPS API；这是管理员配置平台的步骤，
不是客户端注册仓库的步骤。平台需配置扫描与写回使用的 VCS 凭据，worker 需独立配置执行凭据。
GitHub 使用 `clawflow config set-token <token>`（repo、read:org），GitLab 使用
`clawflow config set-gitlab-token <token>`（api）；让用户私下输入实际 secret。凭据保存在
权限 0600 的 `~/.clawflow/config/credentials.yaml`，`GH_TOKEN` / `GITLAB_TOKEN` 优先。
VCS token 与平台 admin/worker token 是不同凭据。平台执行份额由同机 worker 通过正常 worker API 领取；
没有匹配标签的任务保持 pending。已有启用仓库无需重新注册。

在平台主机分别创建客户端管理 token 和每台机器的执行 token：

```sh
umask 077
clawflow worker token create --name client-admin --scope admin > client-admin.token
clawflow worker token create --name laptop --scope worker > laptop.token
clawflow worker token list
clawflow worker token revoke --name laptop
```

create 仅在 stdout 输出一次 secret，证书 SHA-256 指纹输出到 stderr；list 不显示 secret/hash。
通过私有渠道分发对应 token 文件和可信指纹，禁止提交或写进 issue/日志。同名 token 不覆盖，
撤销后需使用新名称创建替代 token。admin 访问 `/admin/v1`，worker 访问 `/worker/v1`，权限不互通。
客户端执行主文档的 `platform login`；登录验证成功后保存 URL、token 文件路径和可选指纹，
后续 `repo add/list/set/enable/disable` 访问平台，不修改客户端仓库配置。其他本地命令不要
当作远程管理接口；例如本地 `repo show` 不能验证平台注册。平台拒绝 `--local-path`。

## 添加 worker 与凭据亲和性

worker 机器配置自己的 `settings.execution.profile`（由 Offload 管理凭据和模型），
并配置执行所需的 GitHub/GitLab 凭据；不要复制平台的 admin token 来执行任务。
使用主文档的 worker 命令；`--fingerprint` 对 worker 必填，`--roles operator,acceptance`
可声明执行角色。默认角色为 operator；验收容量需要声明 acceptance。
`--tags` 应包含任务要求的平台标签：GitHub 为 `github`，GitLab 为 `gitlab`，自定义实例
为其小写 hostname。worker 也自动携带 `host-<hostname>` 标签。

只有某机器具备的凭据可通过附加标签约束，例如：

```sh
clawflow worker --coordinator https://platform.example.com:8093 \
  --token-file /private/path/laptop.token --fingerprint <SHA-256> \
  --tags github,creds-laptop --run-untagged=false --roles operator,acceptance
```

仓库 `worker_tags`、`worker_tags_by_kind` 或 issue `worker:creds-laptop` 标签决定匹配；
验收任务也继承 issue 的 worker 标签。未匹配任务不会回退到平台同机 worker。
启动后用 `clawflow platform status` 查看在线 worker 和队列摘要；成功启动进程不等于完成任务。
新增容量不需要启动 `run`、web scheduler 或在 worker 上启用仓库。
客户端 `run --local` 是显式调试例外，不能用作托管注册或连接失败时的回退。

## 数据与迁移

复用 internal/store 的 GORM PostgreSQL 连接和版本迁移，版本 2 创建：
- coordinator_tasks：id、kind、repo、issue、pr、operator、payload JSONB、tags、
  priority、queue_position、state、attempt、lease_owner、lease_expires_at、
  fencing_token、result JSONB、created_at、updated_at。
- coordinator_workers：id、tags、run_untagged、max_parallel、roles、backend/model 与时间戳。
- coordinator_tokens：name、32 字节 SHA-256 hash、created_at、last_seen、revoked。
- coordinator_audit：递增 id、worker、task、action、created_at；不记录 payload 或 secret。

任务 kind 为 implement/repair/acceptance/reply。pending 和 leased 为 active。
部分唯一索引确保每个 repo + issue/PR + kind 只有一个 active task。
PR 非零时以 PR 为目标，关联 issue 不参与去重；issue 与同号 PR 独立。
重复入队返回已有任务，不修改其 payload。完成后可重新入队。
Enqueue 不接受调用方注入状态、fencing 或时间戳。

## 领取、租约与 fencing

Claim 先锁 worker 行，并统计其 leased 任务数以保证 max_parallel。
未清理的过期租约仍占槽位，调用方应定期执行 ExpireLeases。
worker 注册必须显式提供 run_untagged（协议默认 true 应由 API 层设置），max_parallel 必须大于零。
任务 tags 必须是 worker tags 的子集；空 tags 只匹配 run_untagged=true。
roles 区分 operator 与 acceptance 容量；backend/model 保存执行元数据。

候选按 priority 升序（数字越小越优先）、queue_position 升序、created_at 升序排序，
同时间以 id 稳定排序。使用 SELECT FOR UPDATE SKIP LOCKED 逐行检查。
领取事务同时更新 pending→leased、owner、120 秒 TTL 和单调递增 fencing_token。
调用方每 30 秒 Heartbeat；数据库时钟续租 120 秒。

Heartbeat、Complete、Fail 均条件更新：任务必须仍 leased、owner 与 fence 匹配且尚未过期。
不满足返回 ErrStaleLease；即使旧 worker 未报告，过期后也不能提交结果。
Complete/Fail 写入 done/failed 和 result；重复结果同样拒绝。
ExpireLeases(now) 将到期任务退回 pending，清空租约并 attempt+1；下一次领取递增 fence。
重试上限、repo 并发门禁和验收独立性由后续 runner 接入层执行，不在此基础存储推断。
所有成功变更与审计同行事务提交；HTTP 层通过 AppendAudit 记录成功及拒绝的 API 调用。

## Token

crypto/rand 生成 32 字节随机值，hex 编码后仅创建时返回。
数据库只保存该 secret 字符串的 SHA-256。VerifyToken 对所有固定长度 hash 使用
constant-time 比较，不因匹配提前返回，并条件检查 revoked=false 后更新 last_seen。
ListTokens 仅返回元数据。撤销不可恢复；HTTP 层复用 VerifyToken 进行 bearer 认证（见下文）。

## 验证

测试复用 #439 的 isolated-schema harness：
`CLAWFLOW_TEST_DATABASE_DSN=... go test -race ./internal/coordinator ./internal/store/...`。
未配置 DSN 时 PG 测试显式跳过；纯 tag 匹配测试仍运行。
完整验收运行 `go test -race ./...`，需区分本地结果与 GitHub Actions 证据。

## HTTPS worker API（#389）

仅在 settings.coordinator.enabled=true 时，clawflow web 启动独立 HTTPS
listener；settings.coordinator.listen 默认为 :8093。监听失败使启动失败，
Web 退出时关闭 worker listener。此入口不提供 dashboard 或 merge API；另有 admin token 保护的平台注册与状态 API。

首次启动或 worker token create 会在 ~/.clawflow/data/coordinator/coordinator.pem
原子创建 ECDSA P-256 自签证书及私钥（合并 PEM，权限 0600），后续复用。
已有损坏证书会报错，不自动轮换。SHA-256 指纹基于证书 DER，以 64 位 hex
输出；启动时显示，token create 将指纹写 stderr、一次性 secret 写 stdout，
便于安全重定向 token 文件。远程 worker 必须校验该指纹。

worker 端点均为 POST，带 Authorization: Bearer <worker-token>，JSON 请求上限 1 MiB，
请求处理限时 10 秒，并设置 header/read/write/idle 超时。
worker 身份固定为 token 名称，不能从请求 body 指定其他身份：

- /worker/v1/register：tags、run_untagged（默认 true）、max_parallel（默认 1）、
  roles、backend、model；返回 {"worker_id":"token-name"}。
- /worker/v1/claim：请求 {}；返回 {"task":任务对象}，任务字段使用 snake_case，
  包含 payload、fencing_token、lease_owner、lease_expires_at。无任务时 task 为 null。
- /worker/v1/tasks/{id}/heartbeat：请求 {"fencing_token":整数}。
- /worker/v1/tasks/{id}/complete、/fail：请求 {"fencing_token":整数,"result":任意 JSON}。

成功返回 200；认证缺失、未知或撤销返回 401；过期、错误 owner/fencing、
已结束任务返回 409；非法 JSON/字段返回 400；过大请求返回 413。
每次请求追加 http:action:status 审计（包括拒绝请求）；审计失败返回 500。
审计不保存 secret、请求正文或原始 URL。结果仅由 store 持久化，
runner effects、acceptance 与自动合并接入仍属于后续切片。

## Coordinator 调度与结果接收（#398）

`settings.coordinator.enabled: true` 时，run 扫描继续执行队列选择、harvest、
sweep、快照和 Pilot；算子任务改为入队 implement / repair / reply。
本机无需实现任务的 clone，machine binding 转为 `host-<小写主机名>` 任务标签
（去掉 `.local`）。本机模式仍保留原有绑定与 clone 检查。

仓库支持 `worker_tags`、`worker_tags_by_kind` 和整数 `priority`；每种 kind 的
标签覆盖仓库默认标签，再加入 `worker:<tag>` issue 标签和平台标签（默认
`github` / `gitlab`，自定义 `base_url` 使用其小写 hostname）。priority 越小越优先，
同一仓库 priority 内 `priority:high` 先于 normal、low，随后按全局 queue 手工
位置及任务创建时间排序。pending、leased 任务计入仓库在途容量。

payload 包含 task_id、kind、operator、skill_digest、repo、platform、base_url、
issue、role、language、timeout_sec、tags、repo_policy、reopen_event_id、repair、scan。
skill_digest 是解析后的 Operator（Source 清空）JSON 的 SHA-256；不包含本机路径。
repair 中传递 coordinator 预留的 attempt、PR head 身份和独立验收 findings。
重复入队先去重，不再次预留修复次数。

worker complete/fail 的 result 使用 jobResult JSON，可附带 `events` 数组；只保留
末尾最多 100 条且总计不超过 64 KiB。HTTP 请求大小仍受 1 MiB 限制。
coordinator 使用自己的 VCS client 执行按序 effects，再根据 outcome/output 做
post-automation；worker 的 PRNumber 不决定合并目标。usage 与终态进入本地 run
记录及已绑定的 PostgreSQL run index，修复 ledger 和熔断也由 coordinator 更新。

接收结果时，在行锁内重新验证 owner、fencing token 和未过期租约，再保存 result，
清空 lease_expires_at；任务保持 leased 以阻止重复入队和重新领取。成功应用后才
转为 done/failed。重复或旧 token 返回 409，不重放任何 effect。

外部 VCS 写入不能与数据库事务原子提交。本切片采用至多一次尝试：写回失败或
接收后进程退出时，保留 leased + result + 空 lease_expires_at，禁止自动重放，避免
重复评论/关闭。写回失败记录 result-apply-failed 审计和 run 的 WritebackError。
恢复前需检查已落地的 VCS 效果；这里尚未实现自动恢复或通用 effect outbox。
