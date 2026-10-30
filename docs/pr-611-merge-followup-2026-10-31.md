# 菜单栏查看任务状态：继续合并记录

用户希望点菜单栏就能看懂任务正在做什么、为什么排队，以及哪些项目暂停监控；不需要先打开主窗口。对应 PR 为 [#611](https://github.com/zhoushoujianwork/clawflow/pull/611)，需求为 [#610](https://github.com/zhoushoujianwork/clawflow/issues/610)。

## 已完成的改进

- 将这组 issue/PR 标题改为说明用户可获得的效果。#609 表述为“环境故障不再误扣代码修复次数”，#617 为“测试新版本时不影响正在运行的正式服务”，#618 为“缺少 Offload 时提前提示，避免任务启动后失败”。未改变功能要求或验收门禁。
- 仓库 AGENTS.md 增加说明顺序：先讲用户问题与使用效果，再讲实现、验证和限制，提交 `acce75f`。
- #611 通过正常合并保留 #618 的 Offload 提示，提交并推送 `4666c027f73f0c680b17dfb1142003eebe358668`。Swift 严格并发检查 39 项通过。
- 针对新提交重新构建独立原生预览，保存名称、可见键盘焦点、滚动、待处理、错误和成功状态的截图与 AX 树。暂停监控后项目数 17/7 → 16/8，运行与排队任务保留。没有声称真实菜单栏锚定、VoiceOver 朗读或所有中间键盘路径均通过。
- 本机测试 SOP 新增证据定位与校验程序，要求独立审核先核对准确提交、源码和产物摘要，再自行判断是否充分。索引位于 `~/Library/Application Support/ClawFlow/acceptance-inputs/clawflow-pr611/index.json`；原 SOP 已备份。没有把外部实测资料直接写成独立验收通过。

## CI 阻塞

提交 `4666c027` 的 GitHub Actions run 为 `41105570205`。Native macOS 检查 `124234340413` 在启动时取消；其公开 annotation 明确指出 macOS 14 runner 的计划暂停。GitHub [官方公告](https://github.com/actions/runner-images/issues/13518)说明 2026-10-30 14:00 UTC 至 10-31 00:00 UTC 为暂停窗口，并于 11 月 2 日停用该镜像。当前 [runner 列表](https://github.com/actions/runner-images)包含受支持的 `macos-15`。

Test 检查 `124234340438` 也失败，Web render 通过。ClawFlow 的有界日志入口未取得 Test 的具体失败输出；没有猜测具体代码缺陷。已将明确的 runner 兼容修复范围写入需求讨论：迁移 Native macOS 镜像，保留 job 名称、测试、构建和全部门禁。由现有 controller 的 repair-ci 流程执行，不启动第二 scheduler，也不并发改写 PR 分支。旧的临时 `clawflow-skip` 和历史 `agent-failed` 已在本轮授权下解除，让正常流程可继续；共享修复账本没有清空。

## 修复调度器误报当前 issue 未知

现场日志显示修复 #611 时反复被 `unknown issue #610` 阻塞。问题是协调快照同时含有当前 issue 和相关 issue，校验器却只在相关列表中校验模型引用，错误拒绝了已知的当前 issue。

修复提交 `f7d5a20557f196dda53e5e6a25589fc227100dca` 允许引用快照中的正数当前编号。新增回归先在旧实现上复现两项失败；修复后的协调层和相邻调度 race 检查通过，vet、diff 检查通过。未知编号、零编号、自我替代、活跃任务和自动关闭限制仍保留。

随后发现现有 [PR #619](https://github.com/zhoushoujianwork/clawflow/pull/619) 已处理相同问题，并额外禁止“仅等待自己”。本地正式合并其原始提交 `12a7297f`，保留作者和安全校验，同时保留正数编号检查及补充回归。旧实现通过 Go overlay 真实复现仅等待自己被错误放行，合并后的协调与相邻调度 race 检查通过。最终部署源为 `0df19376fda67558441c9ec973d4e11097531f34`；这是本地整合，不表示 GitHub PR #619 已合并。

已将最终版本滚动部署到本机独立服务，版本 `dev-coordination-0df19376`，二进制摘要 `50852116d7e6130c5bb3f19adbfd47dc3bc1503e28a3281b41ed8c19d4c9efa8`。两个 worker generation 均匹配新摘要。先 drain 再替换入口并优雅重启 admissions，随后恢复原暂停状态。#612 的 executor PID `59667`、Offload #68 的 executor PID `2876` 均保持 epoch 1；原固定执行器未替换。最终备份位于 `~/Library/Application Support/ClawFlow/backups/coordination-primary-20261031-q6eta3it/`。本次未修改 App 源码或重启 PostgreSQL，数据库 PID 保持 `61176`。

#611 的准确提交还通过独立临时 PostgreSQL 上的 `TestNativeBridgeLANLifecycle` race 检查，实际执行 1 项、跳过 0 项；产物位于 `/var/folders/64/xyvthqtj4xqb6xp70kw9hgf80000gq/T/clawflow-pr611-contract-plare12h/`。

## 修复重试现场

现有调度器于 2026-10-30 18:34 UTC 派发任务 `204276758f4759feb2268826020319b68931fde33c6cc03210f3ef1203e3f730`。启动前检测到协调快照已变化，因此在调用开发模型前中止，账本将其记录为已释放的 launch 故障，并保留到 18:44:52 UTC 的基础设施退避。只读对比确认 PR 说明刚被本轮改写：派发时为旧版本，worker 读取时为新版本。该拒绝符合保护要求，不放宽快照校验、不重置共享账本。

已冻结相关 PR/issue 描述等待现有调度器重试。本机项目 context 追加中文、先讲问题与使用效果的说明要求，原文件备份在同一 acceptance-inputs 目录。运行日志和被中止的 journal 均保留。后续需追加真实 repair、CI、独立验收和合并结果；当前尚未宣称 #611 合并。

## worker 本地队列损坏导致的再次启动失败

第二次派发 `8f3263b4cf4ab85de22546055a6dbce599c40280eb10a2ec9e3f45850d71b66e` 于 18:48 UTC 在调用开发模型前失败。实际原因是 worker 的 `local-worker/data/queue.json` 内容为 `not json`，mtime 为 18:45:25 UTC；这与 `internal/queue` 测试的损坏夹具相同。测试只改 HOME、未清除继承的 `CLAWFLOW_STATE_DIR`，因此可写入 worker 状态。该路径不是 controller 的人工排序来源，controller 自身队列文件不存在。

修复 `f82243844cb131097a934df980d6dd1e264696a3` 让 worker 刷新远端协调事实时直接沿用已批准的 controller 排序，不再预先读取无权决定排序的本地文件；controller 路径仍拒绝损坏的人工排序。队列测试辅助函数清除继承的状态目录。新增损坏文件回归在旧实现真实失败；修复后协调相关 race 检查、队列 race 检查通过，继承目录中的哨兵内容保持不变。远端需求变化仍被拒绝。

该版本已滚动部署为 `dev-coordination-f8224384`，摘要 `246e2f84f1a946dfe19d545f28752a5d45a6825b7805625bc0924cdac639d036`，两个 worker generation 匹配。活动 executor PID 59667、2876，epoch 1 与固定执行器均保持；PostgreSQL PID 61176 保持。备份为 `~/Library/Application Support/ClawFlow/backups/coordination-primary-20261031-t13nihq5/`，含原样保留的损坏队列文件。本轮没有删除或用空状态覆盖损坏文件。账本记录第二次 launch 释放并退避到 19:08:06 UTC，没有手工清除。

## 读取被取消 workflow 中的实际测试失败

公开 API 确认 run `41105570205` 的总体结论为 `cancelled`，但其中 Test job 为 `failure`。日志层原先仅查看 failure/timed_out workflow，故漏掉真实失败。修复 `e8eac262` 接受已完成且取消的 workflow，同时仍只读取准确 SHA、同仓库、最新 run/attempt 的 failure/timed_out job；取消/成功 job 不成为失败证据。新增回归在旧实现失败，GitHub VCS 包完整 race 检查通过。该修复先完成源码验证，随后随下述最终服务版本部署。

用修复后的 CLI 取到准确 Test job 日志，产物为 `/tmp/clawflow-pr611-ci-log-cancelled-workflow.txt`。摘录显示 `TestBlockedWorkerNeverClaimsButStaysRegistered` 在 400ms 内未完成 TLS 注册而失败，后一项 `TestHealthyGateStillClaims` 通过。已在下一次派发前补充需求讨论，要求修复 worker 在慢 CI 下的测试同步/超时，保留“仍注册但绝不 claim”断言；不能删除测试或改变生产门禁。日志为有界摘录，不保证覆盖全部失败。

## 本轮完整回归与补充界面证据

在提交 `d853c47a` 上运行 `go test -p 2 -race -json ./... -count=1 -timeout 8m`，使用独立本机 PostgreSQL，清除生产 `CLAWFLOW_STATE_DIR` 的继承。结果：3234 项通过、0 失败、0 跳过，退出 0；临时数据库已停止。完整产物及摘要在 `/tmp/clawflow-runtime-recovery-suite-i4ro6o5w/`。首次准备临时数据库时遇到 Unix socket 路径过长，尚未运行测试；改用较短的 `/tmp` fixture 路径后完成上述检查，正式数据库未受影响。

对 #611 的 `4666c027` 预览补充 `disconnected.png/.ax.txt` 与 `unavailable.png/.ax.txt`：已有数据断连后保留 17/7 项目数及各 1 项 attention/running/queued，显示最后更新时间并禁用控制；首次连接失败则显示未知，不伪造零。独立预览已退出。证据清单增至 21 份文件，已重新核对摘要并复制到本机只读验收输入。保留真实菜单栏锚定、VoiceOver 和全部中间键盘路径未验证的限制。

本轮修复与记录已正常推送至 `fix/worker-retry-journal` 分支，不表示已进入远端 main。#611 仍需真实 repair-ci、准确新提交 CI 和独立验收；#609/#617 的开发来源门禁保持。

## 最终服务版本与记忆提交

完整回归后，将源提交 `2106391aec4e9c4b4bf333bf311b57109039b927` 滚动部署为 `dev-merge-assist-2106391a`，摘要 `6f91518012a86fbbb4d21d59d45396d8161a9fd39fc7610b870e6559d8ee6482`。两个 worker generation 均匹配；#612 executor PID 59667、epoch 1、原固定执行器及 PostgreSQL PID 61176 保持。Offload #68 此时已自然完成，journal 为 acknowledged，不将其进程结束误报为部署中断。备份为 `~/Library/Application Support/ClawFlow/backups/coordination-primary-20261031-lxpdopu6/`。安装后的 CLI 已实际读回准确失败 job 日志，证明日志修复进入部署版本，结果在 `/tmp/clawflow-pr611-installed-ci-log-proof.txt`。

按用户此前明确要求，将“现在已经改运行在本地了 更新一下你的记忆”原文及本机 ClawFlow/B0435.local 的只读证据提交到已配置的 Agent-Host 记忆连接 default，回执 `b9e6f395-4b3d-4b4b-a8f8-17f1f79dc9cf`、ingestion_status=received。这只证明已接收，未声称已完成选择或发布，不轮询处理状态。

## 恢复固定 SOP，隔离补充资料

19:07:59 UTC，#612 原 CC 会话达到一小时执行期限，结果为 `context deadline exceeded`、error_kind=canceled、infra_kind=execution-interrupted；任务原 epoch 1 的 journal 和 execution.json 已由正常机制保留在 `history/.../epoch-1/`。它在本轮各次部署期间保持连续，最终因原定执行期限结束，不能描述为部署取消。

随后自动重试进入 epoch 2/3，但在模型启动前被 testing SOP 摘要不一致拦截：任务固定 `800afdfb709f5445c55ed84774ce89a726836f3862530828c377a0a8fb956bfa`，而本轮为 #611 追加的共享 SOP 已改变。这是本轮操作引入的失误，不能通过改任务摘要或清空重试历史解决。

已从原始备份恢复共享 `testing.md`，SHA-256 精确匹配上述固定版本。补充原生资料说明移至 `acceptance-inputs/clawflow-pr611/testing.native-evidence-procedure.md`，只在项目 context 中提供只读定位；它不替代原 SOP、原有验收要求或独立判断。临时证据写入脚本也已改为只更新独立说明文件，避免新提交验收时再次改共享 SOP。调度器已重新选取 #612；是否进入新会话、修复与合并仍需按后续真实记录判断。

## 自动退避后的真实修复与独立复核

#611 在自动退避后进入任务 `9ba86bf06ed206f49807ce2381981d0322c865f05723c058836ccece55463c63`、epoch 1。原生 CC session 为 `87728782-1d1c-4816-83c2-7c565b31fe0b`，profile `claude-claude-sonnet-5-1m-inherit-inherit`。19:14:41 UTC 的只读证据确认 worker 本地队列仍为 `not json`，共享 SOP 已恢复为 800afdfb…，模型已真实启动。这证明部署修复让自动重试越过原阻塞，未靠清空损坏文件或账本。产物 `/tmp/clawflow-pr611-auto-recovery-proof.json`。

协助诊断新回归夹具时，仅对其 `commands.test` 子进程 PID 80944（19:18:09 UTC）、84359（19:20:54 UTC）发送 SIGQUIT。外层 Offload/native CC/worker executor 保持运行；原 Bash 链完成正常 stash pop。两次人工诊断终止不能作为测试通过或旧实现回归失败证明，已在 issue 讨论中明确。CC 后续修正夹具读取请求 body，并重新执行正常检查。

该真实 repair-ci 会话正常结束、journal acknowledged，SOP 摘要精确为 800afdfb…，普通推送产生提交 `17b92f515234737ae1f9fdf8e17187a306267a4d`。它修正重新注册在上下文关闭时误报故障，保留 fatal 信任错误行为。没有修改 CI 配置。

主代理随后在准确提交 17b92f5 的独立 checkout 进行 Go overlay 复核：保留修复后的回归夹具，仅替换为父提交的 worker_run.go，3 次均以 context canceled 触发目标断言，退出 1；无超时、无 SIGQUIT、无 no-tests 情况。修复版相邻 worker gate/claim/durable race 检查通过，退出 0。产物 `/tmp/clawflow-pr611-cancel-regression-5dhbfk82/`。该复核与修复器自报分开，不将缺少本地 PostgreSQL或旧的 4666c027 原生截图说成 17b92f5 的完整验收。

## 公共 CI 维护改走正常实现

17b92f5 的真实 CI run `41115045367` 中 Native macOS job `124266946772` 仍被取消。仓库 `skills/repair-ci/SKILL.md` 明确禁止修改 CI configuration，故不能继续让受限修复器承担镜像迁移。未修改这项权限边界。

创建独立需求 [#620](https://github.com/zhoushoujianwork/clawflow/issues/620)“恢复 PR 必需 CI：更新停用的 macOS 镜像，修复 worker 退出误报”，由现有平台走正常实现、CI、独立验收。范围是 macos-15 和仅复用 17b92f5 的两个 Go 文件，保留作者来源；不携带 #611 菜单栏或 #612 存储功能。#610 临时设置 clawflow-skip，避免重复受限修复；原预算账本保留。公共维护合入 main 后，正常同步 main 回 #611、解除临时 hold，再做准确新提交的原生及独立验收。没有将额外需求伪装成 #611 已合并。

## 独立验收计划修正与公共维护合并

旧 #621 验收计划把 CI/API 观察标成需要完整运行时身份的 live 证明，还把未来合并／关闭流程列为合并前的行为前提，导致报告无法满足原有严格校验。保留旧 blocked 记录，修正计划生成说明：CI/API 使用 execution；只有明确运行目标才要求 live 的五层身份；交付动作不替代产品行为验收；passed 的 missing 必须为空。PlanningPolicyVersion 正常升为 3，旧缓存留存，已固定的运行计划不改写。提交 be88412b；报告校验器、CI 和合并门禁未放宽。

同时修正并发计划写入测试夹具：不同内容冲突与相同内容幂等重放分开验证，生产 Store.write 未改动。冻结旧测试时钟可确定复现原夹具错误；修正后两种场景 race 各运行 100 次通过，完整 acceptance 包和相邻规划检查通过。首次新全量回归为 3233 通过、1 失败、0 跳过，失败即该夹具，不能描述为全量通过；修正后重新进行集成回归。

be88412b 已滚动部署为 dev-acceptance-plan-be88412b，CLI SHA-256 为 f133ee138b42a54da848048b8e7b2c6c9a3240c1cb8ba2f0b17d581658104979；两个 generation 相符，部署时 #612 executor PID 77416、原生 CC 会话 eec02683-c7c0-4ec8-81ec-d5e0040c9d98、固定执行器和计划摘要均保留，正式 PostgreSQL PID 61176 未重启。

#621 在新计划下由独立 CC 会话 ea5a5b46-4bbb-405e-8157-90a6b46a284c 完成 14 项验收，attempt dc0f13a9-69f9-4f0b-917e-d8bde16b3843；开发会话为不同的 7c8b7404-74f7-4921-b9b3-e4c8d78862a7。准确 head bb76d071442140bc8aa4ccd37ec505f8c7fa4bcb 的三个必需 CI 全部成功（run 41116955757）。现有 controller 于 20:08:13 UTC 自动合并，main 合并提交为 951508b01cb911b64b664fe70e93aee838aeb20a。已正常合并回恢复分支，提交 405a90a2，保留本地恢复修复；不代表恢复分支的其他改动已进入远端 main。

## #611 当前提交与实际阻塞

普通 merge main 产生 e1837c8a5016437654d88279bddd14b4e2b2a5e8，已普通推送原 fix/issue-610 分支，无强制推送。该提交 Swift 严格并发 39 项通过；独立 PostgreSQL NativeBridge race 契约 1 项通过、0 跳过，产物 /var/folders/64/xyvthqtj4xqb6xp70kw9hgf80000gq/T/clawflow-pr611-contract-oxdcpd9a，临时数据库已停止。

当前提交的隔离预览已构建并签名，但 CUA 返回 cgWindowNotFound；只读系统会话确认 IOConsoleLocked=Yes、CGSSessionScreenIsLocked=Yes。另构建明确标注 regular/foreground activation 的预览入口仍无法取窗；未更改产品 Swift 源码。尚无 e1837c8a 新截图，旧 4666c027 资料保留原来源，不冒充新提交实测。#610 临时 hold 保留，等待真实视觉和独立验收。PR 正文已纠正为当前源码和检查状态，不再声称没有 Go/CI 变更或失败日志不可得。

20:16 UTC 快照中两个 worker 均在线、未暂停、无运行任务。#612 的 d022... 会话达到原一小时执行期限后结束，后续 epoch 因协调上下文变化在启动前阻塞；没有重置重试账本、改写固定计划或终止原生会话。#609 仍缺可信开发来源；#617 的当前 head 也仍需满足来源门禁，未伪造开发记录。

准确 head e1837c8a 的 acceptance status 已返回 `no current completed acceptance evidence`，没有开发来源错误；正常 merge 的可信来源继承路径有效。三项新 CI 已创建，检查 ID 为 124290165305、124290165242、124290164827，观察时均排队中，未重跑或绕过。#617 的实际返回为 `latest development run is absent, incomplete or unsuccessful`，#609 为 `trusted development provenance unavailable`。


## 集成回归与本地服务交付

提交 405a90a2 的完整独立 PostgreSQL race 回归退出 0：3236 通过、0 失败、0 跳过；产物 `/tmp/clawflow-runtime-recovery-suite-r08c4s8w/summary.json`，临时数据库已停止。它包括并发计划夹具修正后的最终全量结果，替代此前存在 1 个夹具失败的回归结论。

第一次部署预检发现 #612 已自动启动新任务 40587e540602d93e7bb37c96ddc7a6e34be38b07be028e0e9cb18a6df6811355，故预检在替换 CLI 前退出并恢复原来的 admission 状态；只新增备份 4iwy7mf2，未停止该任务。随后核对其 durable 协议与完整执行身份，再使用受支持的滚动流程部署，未靠忽略活动任务继续。

最终服务为 `dev-merge-assist-405a90a2`，源提交 `405a90a2b28567992fb5e9a9649153bee795ab36`，CLI SHA-256 `b1e030b2d91b3121b6529c92c14a32027d221d212ffae0caf8a4d82a9a2971df`，两个 generation 均匹配。备份和前后证据位于 `~/Library/Application Support/ClawFlow/backups/coordination-primary-20261031-rrojm7y3/`。先 drain 新 admission，正常 SIGTERM controller 和服务 admission manager，核对后恢复原未暂停状态；未发送 SIGKILL，未重启正式 PostgreSQL，未执行 SQLite 迁移。

活动 #612 的 executor PID 62130、Offload PID 62369、native CC PID 62371、会话 750996d5-cbce-49d6-a05a-a4e84f6617a2、工作树、原 f133ee... 固定执行器、fencing_token=1、lease_owner=local-cli-worker 及固定计划 bfc25647... 在滚动前后逐项相同。两个 worker 在线、未暂停，容量仍为 1/4，PostgreSQL PID 61176 保持。App 仍为原安装版本；#611 界面未宣称已部署。桌面最后检查仍锁定。

交付记录时，#611 的新 Native macOS 和 Web render 均已成功，Test 仍在运行；没有把 pending 当成通过。当前提交的界面实测及之后的独立验收仍待完成，#610 hold 未解除。
