# Worker pool 存储细节

返回 [主设计](worker-pool-design.md)。

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
roles 和 backend/model 当前仅保存元数据，后续调度策略由相应切片实现。

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
Web 退出时关闭 worker listener。此入口不提供 dashboard、配置或 merge API。

首次启动或 worker token create 会在 ~/.clawflow/data/coordinator/coordinator.pem
原子创建 ECDSA P-256 自签证书及私钥（合并 PEM，权限 0600），后续复用。
已有损坏证书会报错，不自动轮换。SHA-256 指纹基于证书 DER，以 64 位 hex
输出；启动时显示，token create 将指纹写 stderr、一次性 secret 写 stdout，
便于安全重定向 token 文件。远程 worker 必须校验该指纹。

所有端点均为 POST，带 Authorization: Bearer <token>，JSON 请求上限 1 MiB，
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
