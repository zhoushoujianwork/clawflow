# 特殊真实测试环境：声明与就绪契约

状态：设计契约及 SOP 模板。#559 共享机器本地主项目 `testing.md`；#603 增加 coordinator SOP 摘要和能力声明的 readiness 检查（见末节）；#601 实现验收报告的运行时身份证据门禁与五层报告校验；#602 增加机器本地共享资源租约。宿主探测由获授权的环境适配器提供，尚无通用桌面探测器；不实现 coordinator 私有文档传输或跨机器权威租约服务。其余协议中的后续设计不表示已有运行时能力。未完成的执行验收项继续保留，不能因 SOP 注入完成关闭整个需求。

## 当前边界与共同输入

开发通过 `project.HeaderForRepo`、独立验收通过 `project.TestingHeaderForRepo` 读取执行机器上的同一主项目 `testing.md`，使用相同的 SOP 段落格式。主项目遵循仓库 `primary_project`，未配置时按项目名排序选择。验收只读取测试程序，不注入 `context.md` 或 `deployment.md` 中的开发结论。requirements-only planner 不读取这些机器文档，也不读取开发答案。

文档不存在、为空或仓库无项目时，保持旧提示输入行为。这是向后兼容，不是证明真实环境可用。若批准的需求要求真实测试而文档或能力缺失，审核者必须报告 `blocked`、缺失证据、就绪步骤和 `next_owner`，不得用离线通过替代。声明了需求的 coordinator 任务执行末节所述结构化前置检查；结构化报告中声明通过的 live 证据还须校验运行时身份和五层结果。实时探测仍依赖独立证据。

本机单 worker 需要有正确项目映射和私有文件。跨机器 coordinator 不分发私有文档或资源锁；#603 随任务携带公开能力需求和 SOP 摘要，worker 仍须具备自己的本地文件。配置同步也不证明候选 worker 已具备能力。不得假定 coordinator 上的文件存在于 worker。后续调度协议须携带公开环境 ID、SOP 版本摘要、所需能力和租约键，worker 解析私有绑定后验证摘要，一致才可执行。缺少映射、文件、版本匹配或能力时返回 `blocked`；项目维护者负责 SOP，机器维护者负责绑定/登录/connector，调度者负责重新分派。不能自动把私有文档复制进公共任务或评论。

共享 SOP 应仅含程序和前置条件，不含开发结果；使用不可变版本及 SHA-256 摘要约定开发和验收读取同一版。coordinator 任务在执行前校验摘要并固定会话文本；验收继承开发候选的摘要，版本变化会阻塞并要求恢复版本或重新建立基线。非 coordinator 调用仍逐次读取文件，不能声称其跨会话快照一致性已实现。验收会话仍独立，固定候选 head，重新探测并生成新证据；已有 source immutability、exact-head 和 native identity 检查保持有效。SOP 不得改变批准需求或成为 planner 的新需求来源。

## 通用声明模板

公共声明只用逻辑别名。实际文件位置、账户、应用工程和目标标识由机器私有绑定解析；只报告已脱敏的匹配结果。摘要不是脱敏机制，不公开可反推的低熵身份摘要。

```yaml
contract_version: 1
logical_environment: editor-sandbox
sop_revision: sop-v1
sop_digest: <approved-content-sha256>
required_capabilities: [host-session, authenticated-editor, connector, candidate-build]
resource_keys: [shared-editor-host] # 多仓库必须映射到同一个全局键
owners:
  sop: project-maintainer
  machine: environment-maintainer
  candidate: developer
  scheduling: coordinator
permissions:
  readonly_probe: approved-probe
  candidate_prepare: explicit-owner-authorization-required
  live_write: disposable-fixture-only-with-explicit-authorization
  recovery: approved-restore-only
```

机器绑定单独保存并限制读取：环境别名对应机器能力、宿主选择的 CLI、daemon/connector、可丢弃目标与恢复入口。私有绑定不属于源码、PR 正文、任务评论或默认日志；凭据通过既有认证渠道使用，不读取或导出 secret。机器能力声明不能代替实时探测。

## 七个独立阶段

| 阶段 | 输入与输出 | 失败责任方 |
| --- | --- | --- |
| 环境定义 | 需求要求的真实交互、能力、证据层、公开环境别名及 SOP 版本 | project-maintainer |
| 机器绑定 | 私有别名解析、能力匹配、文档版本匹配 | environment-maintainer |
| 只读探测 | 检查实际执行链、登录、connector、唯一新鲜目标；不写工程、不重启服务 | environment-maintainer |
| 人工前置 | 所有者完成登录、选择可丢弃目标、确认允许的操作窗口 | environment-maintainer |
| 候选准备 | 获授权后构建并加载固定 commit 的 CLI/daemon/connector，重新探测 | developer |
| 真实验收 | 独立会话、有效租约下，真实写入及保存重载，记录新证据 | independent-reviewer |
| 恢复 | 清理自己的 fixture，按批准方案恢复并验证基线，释放租约 | lease-holder |

任一阶段失败记录 `failed` 或 `not-run`，不能删除。阻塞之后的依赖步骤保持 `not-run`。恢复失败须隔离资源并移交机器维护者，不能释放为可用状态后让下个任务继续写。

## 运行时身份与新鲜度

只读探测必须比较 PATH 解析的 CLI 与宿主实际选择的 CLI（私有解析路径、可执行内容摘要和 build provenance），以及活跃 daemon、connector 的版本/构建身份和连接 generation。目标必须在当前 generation 内唯一解析，且绑定到经授权的可丢弃工程。不得仅凭进程存在、版本字符串相同或 PATH CLI 成功就断言链路一致。

候选构建 provenance 必须指向提交 head，覆盖本次变化影响的 CLI/daemon/connector；已发布运行时只能记录为连通基线。未加载候选时 `candidate-loaded` 不能通过。断线/重连、窗口或文档切换、进程重启、租约变化均使旧 window/document 绑定及其后续写入许可失效，必须重新探测、唯一解析并获取新绑定。探测后写入前再次验证 generation 和租约 fence，避免检查与执行之间的状态变化。

## 共享资源租约与并发

当前实现支持同一机器、同一 ClawFlow HOME 命名空间内的协作任务。在每个相关主项目的机器本地 `project.yaml` 中声明相同的逻辑键（配置变更须在任务启动前完成）：

```yaml
shared_resource_keys: [shared-editor-host]
```

`implement`、`repair-ci` 在调用执行器前获取全部资源；独立验收在 requirements-only planning 之后、准备候选 checkout 之前获取。开发调用完成（含其恢复步骤）才释放，验收使用新的 attempt、固定 head 和新的执行证据重新获取。忙碌时不执行任务，返回包含仓库、任务、阶段、候选（验收）和 attempt 的 `blocked` 诊断，`next_owner: scheduling`；调度者可在下一轮重试。开发运行记录为 `blocked`，不作为实现失败计入熔断；验收保存 blocked report。未配置资源时兼容原行为。

锁位于共同 HOME 下的 `.clawflow/locks/resources/`，键经过 SHA-256 编码，使用持久 inode 的内核 `flock`，不删除锁文件。多个资源按键排序获取，失败释放此前取得的资源。持有进程正常退出或崩溃后，内核关闭描述符并使租约立即失效；下一任务可原子接管，不用清理数据库、编辑 PID 或等待任意 TTL。过时的元数据不会续命旧租约；释放只作用于已持有的文件描述符，不按路径删除后继任务的锁。活着的持有者不会因墙钟超时被抢占。

此实现不提供跨机器、跨 HOME、非协作程序或外部用户的互斥，也不提供宿主执行端 fencing。配置了同一键不等于得到操作其他用户工程的授权。无法确保旧任务及其宿主操作已停止（例如遗留子进程、断线的远端操作）、或恢复失败时，必须由机器维护者隔离宿主并确认恢复，不能把内核锁空闲当作环境 ready。候选准备前的通用仓库 setup 不得访问共享宿主；宿主准备、真实操作及恢复必须在受租约保护的执行阶段完成。跨机器共享宿主仍保持 blocked，直至接入下述权威 fencing 服务；每仓库 `max_in_flight`、issue 锁、`agent-running` 均不能代替此锁。

租约以宿主/设备/daemon 的共享逻辑资源键为范围，跨仓库、跨开发/验收会话生效。本机可使用所有参与者共同遵循的原子资源锁；跨机器需要唯一权威的原子获取、续租、释放及单调递增 fencing token 服务。多资源按稳定顺序申请，未全部取得则不写。租约记录 task、阶段、候选 head、holder、到期时间及 fence，公开输出只保留脱敏关联 ID。

开发释放并完成恢复后，验收才可取得新租约并重新探测。busy 返回排队/blocked（`next_owner: scheduling`），不抢占、不重选其他用户工程。超时不能直接证明旧写入者已停止；旧 fence 必须被执行端拒绝，无法 fencing 的宿主需人工确认停止与恢复后才可接管。崩溃、取消和断线均进入恢复流程，释放操作必须校验 holder 与 fence，防止释放别人的租约。

## 证据分层与脱敏示例

`live-verified` 是环境证据的计算结果，不是 acceptance 顶层状态。报告仍使用 `passed|failed|blocked`、checks、next_action、next_owner。结构化 checks 中 `type: live, result: passed` 必须携带 `environment`，由 `ValidateChecks` 再次计算门禁，不能仅提交通过字符串。真实需求只有取得对应真实证据才可通过，环境 ready 本身不能通过真实写入或保存重载需求。

每项证据保存：SOP 版本/摘要、固定 head、独立 attempt/session 引用、采集时间、层级、操作、预期/实际结果、运行时匹配结果、generation、租约关联及 fence。私有完整证据与公开脱敏摘要分开存储。失败与未运行项同样保留。

```yaml
candidate_commit: <pinned-head>
sop_revision: sop-v1
attempt: independent-review-attempt
layers:
  offline: passed
  environment-ready: passed
  candidate-loaded: blocked
  live-write: not-run
  save-reload: not-run
runtime:
  path_host_cli_match: true
  daemon_connector_match: true
  target_unique_and_fresh: true
  candidate_provenance_match: false
status: blocked
next_owner: developer
next_action: 在已批准独占窗口加载候选构建后重新探测；不能复用连通基线作为候选证据
```

## 运行时证据接口与已实现的负例

`internal/acceptance/environment.go` 的 `RuntimeProbe` 保存机器私有观测：固定候选 head、已加载构建的提交来源、实际执行/PATH/宿主选择的 CLI 路径、daemon/connector 版本、登录状态、目标数量、目标随机别名和每次连接更新的 nonce。调用方在操作前重新探测；同一目标重连也必须更新 nonce，不能使用可重复的窗口名称或版本号作为连接身份。探测器负责将构建来源解析到提交 head。

`NewEnvironmentEvidence` 将私有观测投影成公开 `EnvironmentEvidence`，只输出 CLI 路径一致性，不输出路径。实际路径由环境适配器保存在机器私有证据中；公开证据引用使用无敏感信息的逻辑别名。五层固定字段始终序列化，每层保存 status 与脱敏 evidence。真实写入和保存重载还须记录各自操作时的 binding；重新绑定不能把旧连接的操作结果变成新连接的证据。`Missing(head)` 返回具体缺项，`Result(head)` 仅在身份匹配且五层均通过时返回 `live-verified`。

这些接口消费探测观测，不会自动登录、操作桌面、加载候选或获取租约，也不能证明调用方提供的观测真实。独立审核者仍须检查原始观测引用、候选来源和授权窗口。`environment_test.go` 的合成 fixture 覆盖 CLI 不匹配、缺登录/connector/daemon、零或多个目标、重连/文档切换、候选未加载、跨 head、失败/未运行层保留及旧操作证据失效；并验证现有验收入口拒绝缺失或无效 live 环境证据。

## 负例与待实现的并发验收矩阵

运行时身份及分层负例已有上述合成测试；本机资源互斥、busy 持有者诊断、开发/验收交接和崩溃进程接管已有 `internal/resourcelease` 与 `internal/acceptance/resource_lease_test.go` fixture。以下矩阵中的跨机器 SOP/能力和权威资源租约执行端仍是待实现规范，不表示已通过。fixture 需要模拟执行链身份、连接 generation、私有绑定解析和权威租约执行端；不应依赖实际桌面或账户。

| fixture 注入 | 必须观察到的结果 | 就绪步骤 / next_owner |
| --- | --- | --- |
| PATH CLI 与宿主 CLI 不一致 | environment-ready blocked，无 live-verified | 核对宿主执行链 / environment-maintainer |
| 未登录或 connector 缺失 | blocked，不尝试访问凭据或自动替换服务 | 完成人工登录/连接 / environment-maintainer |
| 目标零个或多个 | blocked，无写操作 | 指定唯一可丢弃目标 / environment-maintainer |
| 探测后窗口重连 | 旧绑定失效，旧 generation 写入被拒绝 | 重新探测绑定 / lease-holder |
| 已发布版本连通但候选未加载 | 仅基线 ready，candidate-loaded blocked | 准备候选 / developer |
| worker 缺 SOP、摘要不符或能力缺失 | blocked，保留离线结果但不替代真实结果 | 同步批准 SOP/配置能力或重新分派 / project-maintainer 或 environment-maintainer |
| 仓库 A 开发与仓库 B 验收同时申请共享键 | 仅一方取得写租约；另一方 busy/queued，最大同时写入数为 1 | 等待释放 / scheduling |
| 过期 holder 恢复写入、错误 holder 释放 | 旧 fence 写入与错误释放被拒绝 | 隔离并恢复 / environment-maintainer |
| 真实写入成功但保存重载失败 | live-write passed、save-reload failed，整体不通过相关需求 | 保留失败证据 / developer |

并发测试需用同步屏障同时申请，并在两个不同仓库任务中统计写入临界区重叠；随后验证开发恢复、释放、独立验收重新获取及新鲜探测。另测多机器争用、续租失败、任务取消、恢复失败与接管 fencing。不得仅测试每仓库并发计数。

## 权限和恢复边界

SOP 不是授权来源。只读探测获准不等于 GUI 操作、真实写入、替换 daemon、部署候选或发布获准。禁止占用其他任务/用户工程、操作生产工程、读取凭据或扩大任务范围。需要的操作超出当前授权时，保留证据并 blocked，给出操作、责任方和可审核的恢复方案。公共模板和测试 fixture 只使用逻辑别名与合成身份。

## #603：已实现的 coordinator readiness 检查

仓库配置可声明公开前置条件（不包含私有路径、身份或 SOP 正文）：

```yaml
testing_sop_required: true
testing_sop_digest: <approved-content-sha256>
required_capabilities: [fixture-runtime]
```

`testing_sop_digest` 是批准的 `testing.md` 原始字节的 SHA-256。若 coordinator
本机有非空 SOP，入队时自动要求 SOP 并固定其摘要；显式配置摘要优先。
coordinator 没有 SOP 时，须通过以上配置声明需求，不能从未声明的自然语言推断能力。
要求 SOP 却没有批准摘要时返回 blocked，由项目维护者配置摘要后重新入队。
任务只传需求和摘要，不传机器本地文档。

远程 worker 使用 `--capabilities fixture-runtime` 声明机器已配置的逻辑能力，
注册时作为 `capability:fixture-runtime` 标签公开；本机 worker 可在
`settings.coordinator.local_worker.tags` 配置相同的能力标签。
原有 `--tags`、主机标签和仓库 `worker_tags` 路由保持不变。
能力声明不代表实时探测通过，更不代表真实写入获准；#601 的运行时身份证据门禁与五层报告校验、
#602 的共享资源租约是独立检查。

worker 在 checkout 或启动算子/验收前读取自己的主项目 SOP。
缺文件/映射、摘要不匹配、未固定必需版本或缺能力时，上传结构化
`status: blocked`、`next_action` 和 `next_owner`，不执行离线测试来替代。
coordinator 持久化阻塞结果并发布说明；任务调度终态使用现有 `failed`，
result 保持 `blocked`，独立验收记录使用 `blocked`。准备就绪后需重新入队。
SOP/版本由 `project-maintainer` 处理，能力由 `environment-maintainer` 配置或请求改派。

开发执行记录实际使用的 SOP 摘要。验收任务继承该候选开发任务的需求与摘要，
即使 coordinator 配置或文档随后变化也不重新选版；worker 遇到版本变化会明确报告
“Testing SOP revision changed”，要求恢复批准版本或重新建立开发和验收基线。
本次会话通过检查的本地 SOP 字节固定在执行上下文中，后续文件编辑不能改变提示输入。
历史任务没有版本证据时只能使用其原有任务声明，不能追认其版本一致性。

SOP 始终是程序说明，不能授权 GUI 操作、替换 daemon、修改生产工程、读取凭据
或扩大发布范围。readiness 检查只读取 SOP 和已声明能力，不执行文档中的命令。
#603 的实现不包含实时探测、候选部署或资源租约，也不扩大操作授权；#602 的机器本地共享资源租约见前节。
