# Runner 看板与执行终端：实现约束

产品范围见[主文档](runner-dashboard-design.md)。本文件区分已实现的本地样式和待实现的执行链路，不作为已经完成上线的证明。

## 本地样式

- Sidebar 统一 200pt，主区域最小宽度 900pt；顶部筛选栏和详情栏均为 56pt。
- 任务和 Runner 为全宽原生 Table，数据行至少 48pt。任务展示状态、标题/原因、真实任务类型、Runner、本次耗时。空列表只出现一个紧凑空状态。
- 选中行打开可关闭详情；刷新、任务结束后仍保留该任务详情，不能因切换为历史状态自动消失。无该记录时关闭。
- 保留原来的连接新鲜度、失联停止旋转和耗时推进规则。历史缺少结束时间时显示耗时不可用，不继续累计。
- 阶段筛选仅映射已存在的 kind：implement / acceptance / repair / reply。不能根据列表顺序推断某 issue 已通过检查或审核。
- Coordinator activity 是调度事件，每 5 秒轮询；隐藏、断开或关闭详情后不继续取流。不是完整执行终端。

## 待实现的阶段与执行投影

按 repo + issue/PR 归属组织阶段，阶段内保留 task ID、attempt、fencing token 和 Offload run/session 的关联。检查、实现、PR 审核分别有独立的证据来源；CI、审核通过、合并是不同交付门禁。修复与复审可循环，不用固定线性进度条把后续阶段自动标绿。

## 待实现的 Offload 日志链路

1. 执行器显式开启单次 capture，保留配置的 ccprofile、caller_run_id、session 和恢复链；旧执行未捕获时如实显示，不自动重跑。
2. Worker 只通过 Offload transcript read/list/capabilities 协议读取自身持有的执行，不扫描个人会话目录。运行中和完成后都支持按 cursor 有界读取。
3. 通过已有 worker HTTPS 鉴权链路同步/代理，不假定 controller 与 LAN worker 共享文件系统。校验 task/attempt/worker/fence/run 身份；迟到日志不能进入新 attempt。
4. Controller 为管理员提供按 execution ID 的有界分页读取；全局看板继续只返回元数据。日志访问不能隐式创建执行、占用模型槽位、重试或切 profile。
5. App 终端展示工具调用及结果、正常输出、错误、缺口和终止；保留事件顺序与工具关联。断线保留已读内容，重连从 cursor 续读；只在接近底部且跟随开启时自动滚动。
6. 保留 Offload 的内容清理、大小限制和保留期限；不把原始输出自动发布到 issue/PR。不给只读日志终端发送任意命令的能力。

## 上线验收

真实受控执行中能看到新增消息/工具输出；进程结束后仍可回放。同一任务重试日志隔离；重连无重复且 gap 明确；过期、未捕获、不支持、不可读分别提示。远端 worker 可读、无越权；关闭 App 不终止执行。与改动相关的协议/鉴权测试、原生状态测试及实际终端画面均通过后再发布完整功能。
