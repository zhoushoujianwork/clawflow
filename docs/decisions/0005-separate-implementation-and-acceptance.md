# ADR-0005：实现和验收分离，验收独立取证

- 状态：Accepted
- 日期：2026-10-26

## 背景

开发 agent 自己声称“做完了、测试通过了”不能当作交付证据。平台需要一个能阻止未经独立检查的 PR 被合并的门禁。#349（2026-10-26）定义了这个门禁，是 #347 平台跟踪 issue 下的第一批工作之一。

后来（#496，2026-10-29）统计显示约 45% 的 PR 第一次验收失败（24 小时样本：39 个通过，31 个未通过），每次失败的修复加复审约消耗 500 万 token。实现者看不到验收标准，所以会漏掉 reviewer 要检查的要求。

## 决定

- 开发和验收使用不同的原生会话。验收不能使用开发的会话或运行标识，也不信任模型自报的角色字符串，身份由编排层校验。
- 启用 `acceptance_required` 的仓库，在实现开始前由 planner 只根据需求（issue 正文）写验收清单，planner 没有源码访问。清单保存后才创建审查工作区。
- reviewer 在被提交 commit 的一次性 detached 检出里工作，可以构建和运行测试，但不能修改已跟踪源码、提交、切换 HEAD，也没有远程和凭据。
- 验收判定绑定任务代次、仓库、提交的 head、base、策略摘要和 reviewer 会话。未知、被阻塞、缺失、过期或策略不符的证据都阻止交付。必需 CI 只接受 success。
- 实现者和各类修复（CI、验收、冲突）收到同一份清单，必须逐项自检并在 PR 正文说明覆盖方式（#496）。自检不是验收证据，reviewer 仍是新的独立会话，独立判断。
- 需求未变时验收复用已保存的清单；正文变化后重新规划。
- 验收失败时在同一个 PR 上修复，再由新的 reviewer 重新验收（#374）。

## 理由

- 开发者给自己打分会放过自己的盲点，独立会话加独立取证才能构成门禁。
- 把清单提前给实现者，能降低第一次验收失败带来的重复开销（#496 给出的统计）。

## 放弃的方案

- 开发者自验收或自报通过：被合约校验拒绝。
- 仅依赖 provider 的 PR 批准或 CI：验收是在此之上增加的门禁，不替代它们。
- 让 planner 或 reviewer 看到实现者的结论或项目 `context.md`：两者都不会收到。reviewer 收到的 PR 标题和正文被标为不可信的参考数据，不能改变需求或判定规则。
- 隔离 planner 的完整文件读取边界：只读后备模式下做不到，文档明确这是残余风险，不是完整隔离边界。

## 相关 issue

- [#349](https://github.com/zhoushoujianwork/clawflow/issues/349) 独立验收合约和绑定到版本的交付门禁
- [#496](https://github.com/zhoushoujianwork/clawflow/issues/496) 实现前规划验收标准并交给实现者
- [#374](https://github.com/zhoushoujianwork/clawflow/issues/374) 在同一个 PR 上修复失败的验收并重新验收
- [#347](https://github.com/zhoushoujianwork/clawflow/issues/347) 平台跟踪 issue
- 细节见 `docs/independent-acceptance.md`
