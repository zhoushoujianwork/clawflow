# Worker pool

一个 coordinator 负责调度、租约和最终合并；worker 拉取任务并使用本机执行器与凭据执行，不能合并。
本设计来自 #383；存储决定由 #438/#439 更新为共享 PostgreSQL，不再使用 bbolt。

本次 #384 实现任务、worker、token、审计存储及本机 token CLI。HTTPS API (#389)、
worker 执行循环、runner 入队、独立验收和看板仍由后续切片实现。

配置预留如下，默认 disabled，不启动额外 listener，也不切换现有执行模式：

```yaml
settings:
  coordinator:
    enabled: false
    listen: ":8093"
```

使用 [PostgreSQL 设置](storage-design.md) 配置数据库，并先执行
`clawflow db migrate`。在 coordinator 主机管理 token：

```sh
clawflow worker token create --name laptop
clawflow worker token list
clawflow worker token revoke --name laptop
```

create 只输出一次 secret；请自行安全保存，list 不显示 secret 或 hash。
同名 token 不覆盖；撤销后使用新名称创建替代 token。

完整的数据模型、事务与租约约束见 [实现细节](worker-pool-design-detail.md)。
