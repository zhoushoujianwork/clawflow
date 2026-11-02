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
