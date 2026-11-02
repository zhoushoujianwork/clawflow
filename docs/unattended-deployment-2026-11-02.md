# 本机闭环首轮部署

已将 #634 验收提交 `5d0822aaa55ca1772fc5926dc2a1b059db4fed06` 部署到本机 controller 和 worker。修复标准目录迁移后的同步检查及 Python 测试缓存污染。#635 的无效报告修复路由改动尚未部署。

## 证据

- #634 于 06:34:17 UTC 自动合并；独立验收 attempt `353c84eb-0c92-42f6-8ea6-a58d2efddb6d` completed/passed，准确 head 的必需 CI success。
- 候选证据：`/tmp/clawflow-readiness-candidates/8798e9b53ebe/evidence.json`。二次隔离启停通过，逐文件比较无正式数据变化；首次因 stable data changed 失败的记录保留，不作为通过证据。
- 目标二进制 SHA-256：`a008980a0f554c0f45d4d98389f3a742279b80ad0a5feeb1f239b6ec4d79ebbc`。版本标签为 `candidate-8798e9b53ebe`，部署的是已核验产物，未重新构建改变摘要。
- controller 升级记录 `20261102T064112Z-94ed2846`：succeeded，schema 11，无新增迁移，切换时 0 活动 durable leases；API 中断 1234 ms，采样分辨率 1000 ms。API/viewer 健康、worker 重连和调度恢复通过。
- 独立 worker 升级记录 `20261102T064151Z-36c66374`：succeeded，drain/roll/ready/resume 完成。内置 local manager 随 controller 启动进入目标 generation。
- 06:42 UTC 两条 worker 的 Offload readiness 均 ready，仅证明配置与可执行能力，不冒充实时模型认证或任务成功。

正式数据库和 App 未重启。原始回执及 rejected journal 保留。#622 仍因最新开发来源不合格而阻塞，未清空预算或伪造成功。

## 后续

#634 合并后自动删除源分支，导致以该分支为基线的 #635 被关闭；需将其五文件变更重新提交到 main 基线，重新完成 CI 与独立验收后部署。连续一周、至少 20 个真实任务的闭环验收仍未完成。

## 最新修复已完成部署（07:19 UTC）

无效报告修复最终通过 #638（替代 #635/#636，关联 #637）交付，准确 head `a107ff93a1ccccfe52354a72333487cfe67758b8`。已完成合并，代码不再从无效报告生成自动修复证据，失败记录及合并门禁保持。

- CI 通过；开发核验任务 `a147de85940ed78e6e18396e28df50120628b8d71b975a6bbde9447df1667d76` acknowledged，success/no-change，原生会话 `e144ac61-79ae-4784-957b-edf960a99f0b`。
- 独立验收最终 attempt `a1450b40-3935-4684-bbdf-3313bd4302e6` passed。此前两轮 blocked 分别缺少环境说明及开发证据定位；补齐原始文件路径后重新审核，历史记录未篡改。另一次计划引用不匹配在审核前失败，也保留记录。
- 候选 `/tmp/clawflow-readiness-candidates/f783c43a7431/evidence.json` 验证通过；候选内部正式数据摘要无变化。外围诊断覆盖更长时间段，观察到 github-http/budget.json 变化，未把它隐去或宣称整个外围时间窗完全静止。
- 目标二进制摘要 `af0c43ce7b471e86bd94d68be912357476751363de4eaf4a7f6762daf3b7e065`；controller 和两条 worker generation 均已匹配。
- Controller 升级 `20261102T071856Z-49c59d96` succeeded，PID 20854，API 中断 1441 ms（分辨率 1000 ms），viewer endpoint 1449 ms；schema 11，无新增迁移，0 活动 durable leases。
- Worker 升级 `20261102T071900Z-a068d8d7` succeeded，排空、滚动、重新注册与恢复派发均通过；Offload readiness ready。

数据库和 App 未重启。以上是准确版本的交付及启动后检查，不代表连续一周、20 个任务的无人值守验收完成。

07:21:59 UTC 新 controller 已实际触发 ClawFlow 与 Offload 两个仓库的定时扫描；07:22:11 Offload 扫描正常结束，run 日志版本为 `candidate-f783c43a7431`。这证明部署后定时派发恢复，不将它等同于已有新开发任务完成。
