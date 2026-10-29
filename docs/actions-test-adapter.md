# GitHub Actions 测试证据适配器

`clawflow pr actions-observe --request request.json` 只观察已有 PR CI，单次查询后返回 JSON；不 dispatch、rerun 或修改 VCS。先运行 `clawflow db migrate` 应用 migration 5。每个请求固定独立验收 contract 和策略，PostgreSQL 保存当前 attachment 及各 run/attempt 的证据。重启后以相同请求调用即可重新关联；不需要保持 agent 会话，也不占用 coordinator 的 worker 租约。

这是控制面证据采集接口，不是新的调度器或独立验收 verdict。调用方在 `waiting_external` 时结束本次调用；coordinator 启动和每 30 秒自动恢复最早等待的一个 attachment（每次最多 30 秒），不分配 worker 或 agent 会话。没有 coordinator 时，重新调用同一命令继续查询。终态后的新 rerun 需要显式再次观察。当前不会自动给每个仓库绑定策略或启用新门禁；现有 required CI、provider protections 和 acceptance 门禁继续生效。worker 会话不能调用此命令来提交信任策略。观察成功不会批准策略、合并 PR 或改变仓库配置。

请求格式（SHA、仓库/工作流编号和身份均需替换为真实值）：

```json
{
  "contract": {
    "version": 1,
    "task_id": "<已有任务>",
    "generation": 1,
    "repository": "owner/repo",
    "pr_number": 7,
    "head_sha": "<40 位提交 SHA>",
    "base_sha": "<40 位 base SHA>",
    "integration_sha": "<待证明的 PR synthetic merge SHA>",
    "policy_digest": "<独立验收策略 digest>",
    "requirements": "<冻结的任务要求>",
    "development": {"session_id": "<开发会话>", "run_id": "<开发运行>"}
  },
  "policy": {
    "version": 1,
    "repository_id": 123,
    "workflow_id": 456,
    "workflow_path": ".github/workflows/ci.yml",
    "workflow_digest": "<经单独授权的 workflow 原始字节 SHA-256>",
    "jobs": [{"name": "Test", "steps": ["Build web bundle", "Unit tests", "CLI smoke tests"]}],
    "artifacts": []
  }
}
```

策略必须由单独授权的控制面提供，不能从当前代码任务修改后的 workflow 自动生成并激活。命令允许采集候选策略的证据，但不会把输入视为已授权的新门禁。workflow digest 在实际 checkout 的 synthetic merge 提交上校验；缺少批准版本或该文件变化时不能通过。现有 CI 没有上传 artifact，因此默认示例没有必需 artifact；配置了必需 artifact 却找不到时返回 `unknown`，不得自动降低要求。

证据包含 repository/workflow 编号和文件版本、contract/policy、run URL/ID/attempt、job/check ID、各必需步骤状态、提交 head/base/test-merge tuple、原始 conclusion 和 artifact 引用。查询按 workflow 和 head 分页，绑定 PR 的 head/base；同 ID 的重复记录去重，冲突记录拒绝。只查询所选 attempt 的 jobs，较新 run/attempt 替代旧成功。缺失、跳过、取消、限流、过期 artifact、无法证明 checkout 或 head/base 变化都不能得到 `passed`。

当前支持现有 `actions/checkout@v4` 默认 Linux checkout 日志：只在 provider 报告的 checkout 步骤时间段内提取 `git log -1 --format=%H` 输出，再核对 Git commit 的两个父节点为 `(base, head)`。日志不可读、超出下载上限、格式不匹配或出现多个 checkout 证明时 fail closed；不会把 workflow 的 `head_sha` 当作实际测试的 merge SHA。job 日志只用于本地结构验证，不写入数据库、stdout 或评论。非默认 checkout 或不同 CI 布局需要单独验证的适配能力，不能自动推断成功。

验证分层：HTTP fixture 和 storetest 不是 live Actions 验收。真实 smoke 必须在发布确切提交后记录 request、run URL、run/attempt/job 身份及观察结果，交由独立验收会话检查；本命令输出不能替代该独立判定。当前实现不新增 workflow 触发器、不修改 `ci.yml`，也不授权真实 provider smoke、自动合并或部署。
