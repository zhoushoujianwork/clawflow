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
所有成功变更与审计同行事务提交；拒绝的 API 调用审计留给 #389，AppendAudit 可供其使用。

## Token

crypto/rand 生成 32 字节随机值，hex 编码后仅创建时返回。
数据库只保存该 secret 字符串的 SHA-256。VerifyToken 对所有固定长度 hash 使用
constant-time 比较，不因匹配提前返回，并条件检查 revoked=false 后更新 last_seen。
ListTokens 仅返回元数据。撤销不可恢复；此切片没有网络认证或 TLS listener。

## 验证

测试复用 #439 的 isolated-schema harness：
`CLAWFLOW_TEST_DATABASE_DSN=... go test -race ./internal/coordinator ./internal/store/...`。
未配置 DSN 时 PG 测试显式跳过；纯 tag 匹配测试仍运行。
完整验收运行 `go test -race ./...`，需区分本地结果与 GitHub Actions 证据。
