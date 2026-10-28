# Pilot 自主监督设计

设计来源：[议题 #407](https://github.com/zhoushoujianwork/clawflow/issues/407)，交付队列 #375。

Pilot 的目标是监督交付流水线，承担健康检查、解阻、队列维护、提前可行性研究、系统性诊断、策略约束下的合并、合并后运维和状态报告。它不实现产品代码、不审查自己制定的实现、不改变仓库设置或门禁，也不自行降低验收标准。

## P1 已实现的边界

Pilot 唤醒使用独立的 `pilot` 执行角色。配置示例：

```yaml
settings:
  execution:
    profile: implementer
    role_profiles:
      pilot: supervisor
```

`supervisor` 是机器本地的 Offload profile 名称；模型、推理强度和凭据由 Offload 管理，可使用 ACP 后端。未设置 `role_profiles.pilot` 时回退到默认 `profile`，不会继承 `role_profiles.operator`。显式 `CLAWFLOW_OFFLOAD_PROFILE` 优先于角色映射。

`clawflow execution check --role pilot` 检查所选 profile 的本地执行能力；它不会调用模型，也不证明真实服务可用。`clawflow config show` 显示有效 Pilot profile；providers API 保留原始 `role_profiles`，并以 `pilot_profile` 返回有效值。

本次不改变唤醒频率、预算、权限或动作执行方式。每协调器一个 Pilot、事件驱动唤醒、结构化动作及审计均属后续切片；当前仍是每项目按轮次唤醒，已有预算拒绝规则继续生效。

完整职责、动作协议和分期见 [详细设计](pilot-design-detail.md)。
