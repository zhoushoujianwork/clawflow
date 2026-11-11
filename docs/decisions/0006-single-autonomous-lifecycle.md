# ADR-0006：只有一个自主生命周期，没有评估和审批阶段

- 状态：Accepted
- 日期：2026-10-26

## 背景

早期流程把一个 issue 拆成多个由算子驱动的阶段：`classify` 分类，`evaluate-bug` 和 `evaluate-feat` 评分，`decompose` 拆分，再靠 `ready-for-agent` 之类的审批标签放行实现，另有 `workflow`、`auto_approve`、`auto_evaluate_all_issues` 和 `confidence_threshold` 配置选择流程。评估阶段本身带来过故障：评估输出缺少 marker 被计入熔断器，导致 issue 被多个算子冻结（#323）；还有评估算子在标签没有变化时重复执行（#143）。

## 决定

2026-10-26 的重构（提交 `906b60c1`，"retire the evaluation and approval pipeline"）只保留一个自主生命周期。

- 人负责项目方向和功能描述。符合条件的 issue 直接进入 `implement`，分析、规划、测试和修复都在这一次实现里完成。每个 issue 不需要单独的人工确认。
- 确切名字 `classify`、`evaluate-bug`、`evaluate-feat` 和 `decompose` 已退役，同名的用户覆盖也会被拒绝。其他自定义算子和历史记录保留。eval 模型角色只服务自定义的 `evaluate-*` 扩展。
- 旧配置仍可读取。显式选择旧流程会打印迁移提示并继续按自主流程运行。重写配置时不再输出 `workflow`、`auto_approve`、`auto_evaluate_all_issues` 和 `confidence_threshold`，其他受支持的设置（启用状态、worker 标签、合并和 CI 要求）原样保留。
- 无人值守不等于绕过验收：CI 必须成功，独立验收和条件合并保留（见 ADR-0005）。失败的测试留在实现和修复循环里，而不是等待审批。

## 理由

`docs/managed-development-design.md` 的目标是让用户描述任务、配置权限和预算后，ClawFlow 持续完成开发、检查、有限修复和交付。把评估和审批做成强制阶段，与这个目标矛盾。更具体的取舍原因，来源只写了结果，这里不补写。

## 放弃的方案

- 保留评估和审批作为默认路径：退役。
- 保留一个 `workflow` 选择器，让用户选自主或旧流程：配置键保留读取兼容，但不再有选择行为。
- 以“无人值守”为由放宽验收、CI 或重试上限：不放宽，见上。

## 相关 issue

- 提交 `906b60c1`，验证记录 `docs/autonomous-cleanup-validation-2026-10-26.md`
- [#323](https://github.com/zhoushoujianwork/clawflow/issues/323) 评估阶段 no-marker 冻结问题（该阶段被移除前的实际故障）
- [#143](https://github.com/zhoushoujianwork/clawflow/issues/143) 评估算子重复执行
- [#337](https://github.com/zhoushoujianwork/clawflow/issues/337) `Depends on:` 执行前门禁，防止自动审批越过未完成依赖
- 细节见 `docs/managed-development-design.md`、`CLAUDE.md`
