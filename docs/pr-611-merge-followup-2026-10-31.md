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

公开 API 确认 run `41105570205` 的总体结论为 `cancelled`，但其中 Test job 为 `failure`。日志层原先仅查看 failure/timed_out workflow，故漏掉真实失败。修复 `e8eac262` 接受已完成且取消的 workflow，同时仍只读取准确 SHA、同仓库、最新 run/attempt 的 failure/timed_out job；取消/成功 job 不成为失败证据。新增回归在旧实现失败，GitHub VCS 包完整 race 检查通过。此提交目前在源码中，服务仍为上述 f822 版本。

用修复后的 CLI 取到准确 Test job 日志，产物为 `/tmp/clawflow-pr611-ci-log-cancelled-workflow.txt`。摘录显示 `TestBlockedWorkerNeverClaimsButStaysRegistered` 在 400ms 内未完成 TLS 注册而失败，后一项 `TestHealthyGateStillClaims` 通过。已在下一次派发前补充需求讨论，要求修复 worker 在慢 CI 下的测试同步/超时，保留“仍注册但绝不 claim”断言；不能删除测试或改变生产门禁。日志为有界摘录，不保证覆盖全部失败。

## 本轮完整回归与补充界面证据

在提交 `d853c47a` 上运行 `go test -p 2 -race -json ./... -count=1 -timeout 8m`，使用独立本机 PostgreSQL，清除生产 `CLAWFLOW_STATE_DIR` 的继承。结果：3234 项通过、0 失败、0 跳过，退出 0；临时数据库已停止。完整产物及摘要在 `/tmp/clawflow-runtime-recovery-suite-i4ro6o5w/`。首次准备临时数据库时遇到 Unix socket 路径过长，尚未运行测试；改用较短的 `/tmp` fixture 路径后完成上述检查，正式数据库未受影响。

对 #611 的 `4666c027` 预览补充 `disconnected.png/.ax.txt` 与 `unavailable.png/.ax.txt`：已有数据断连后保留 17/7 项目数及各 1 项 attention/running/queued，显示最后更新时间并禁用控制；首次连接失败则显示未知，不伪造零。独立预览已退出。证据清单增至 21 份文件，已重新核对摘要并复制到本机只读验收输入。保留真实菜单栏锚定、VoiceOver 和全部中间键盘路径未验证的限制。

本轮修复与记录已正常推送至 `fix/worker-retry-journal` 分支，不表示已进入远端 main。#611 仍需真实 repair-ci、准确新提交 CI 和独立验收；#609/#617 的开发来源门禁保持。
