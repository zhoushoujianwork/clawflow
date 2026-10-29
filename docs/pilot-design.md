# Pilot 平台 steward

Pilot 是一个全平台 steward，覆盖所有启用的仓库，与项目成员关系无关。它只在用户对话时运行；用户定义的定期检查由 #511 实现，本切片没有内置调度器。`clawflow run` 收尾和 Web tick 都不会自动调用 Pilot。

```bash
clawflow pilot ask "汇总平台进展并指出阻塞项" --ops 5 --timeout 60
clawflow pilot chat --ops 5 --timeout 60
```

`ask` 打印回答；`chat` 接受逐行消息，保留本次对话历史，使用 `/exit` 或 EOF 退出。每条消息都是一次有动作预算和超时限制的调用。`--timeout` 单位为分钟；不传美元预算上限。`pilot wake [project]` 是兼容入口，忽略项目参数，以 `review platform state` 提示运行一次全局调用。网页顶部及项目页的 Steward 按钮通过现有 `/api/chat/spawn` 打开同一个 `pilot chat`。

上下文包括所有启用仓库、缓存看板（含验收摘要）、最近运行结果及其实际 usage、最近 steward 结果。快照缺失或过期不能当作成功证据；未知 usage 不估算。全局章程位于 `~/.clawflow/pilot/charter.md`，缺失时从已有项目 context 初始化一次。之后已有章程保持不变；项目 `context.md` 继续作为实现上下文，不再是 Pilot 的可写记忆。

回答可以附带 fenced `pilot-actions` JSON，交给原有动作执行器。支持 `edit_spec`、`comment`、`label_add/remove`、`create_issue`、`rerun_ci`、`override_merge`、`note` 等已有动作；schema、允许列表、幂等键、创建/每日限额、审计及每仓库 `pilot_override_merge` 策略继续生效。禁止修改 `clawflow-skip`。动作提案不代表执行成功；拒绝原因写入审计并返回错误。

项目 YAML 中旧 `automation` 被忽略并在保存时删除。`clawflow project automation ...` 是输出一行迁移提示、退出 0 的兼容空操作，不能启用、禁用或绑定 steward。旧项目历史仍可读取。

执行角色保持 `pilot`：

```yaml
settings:
  execution:
    profile: default
    role_profiles:
      pilot: supervisor
```

Offload 管理模型及凭据，未配置专属角色时回退到默认 profile。`clawflow execution check --role pilot` 只检查本地执行准备情况，不调用模型。已有更早设计见 [历史设计记录](pilot-design-detail.md)，自动唤醒部分已被 #510 取代。
