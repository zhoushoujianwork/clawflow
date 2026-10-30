# 四个 PR 的验收推进记录

日期：2026-10-31。延续[此前合并协助记录](pr-merge-assistance-2026-10-31.md)。本轮未绕过来源、独立验收或 CI 门禁。#618 已通过正式验收并通过正常入口合并，其他 PR 按下表推进。

| PR | 当前提交 | 本轮结果 | 剩余条件 |
| --- | --- | --- | --- |
| [#609](https://github.com/zhoushoujianwork/clawflow/pull/609) | `d4745f4f89456ef48bc0e77db187e3a4d90290ae` | 来源门禁仍阻塞；本地 9371 份运行元数据中未找到 issue 608 的开发记录；旧执行主机 SSH 再次超时 | 从真实执行来源取得可信开发记录，不能补造 |
| [#611](https://github.com/zhoushoujianwork/clawflow/pull/611) | `5ba2716f68bcc2ec7f513c037e3e27f2c98bbf8e` | 已补原生预览证据；本轮独立报告仍未通过结构化校验，正式结果 failed | 原生证据需传递给独立审核；修复报告证据格式 |
| [#617](https://github.com/zhoushoujianwork/clawflow/pull/617) | `a75c39643022f80bd526b71efb3bfa42d658bd33` | 修复并推送，52 项相关 race 检查通过，当前提交 CI 通过，隔离真实启停通过 | 当前提交可信开发来源、独立验收；原生视觉层仍待验证 |
| [#618](https://github.com/zhoushoujianwork/clawflow/pull/618) | `edf1f73b93e03045a8542802f027a4cee0ff90e9` | 正式独立验收通过，22 项条件满足；保留前轮 PostgreSQL、race、Swift 检查记录 | 已通过正常合并入口合并，保留源分支；原生视觉行为单独标为未验证 |

## #617：修复验收机制本身的缺陷

真实候选启停暴露了两项问题：构建使用 `-mod=mod` 会改写固定提交的 `go.mod`；随后一次失败启动的停止操作可能复用上次成功证据。首轮生成的成功记录因此无效，不用于验收。

修复后构建使用 `-mod=readonly`，并重新核对提交和工作区；每次启动先归档旧证据，失败写入本次证据，活动实例重复启动被拒绝。停止时拒绝损坏或身份不匹配的记录。稳定目录快照比较内容摘要、类型和权限，校验器重新计算各层结果，拒绝自相矛盾的成功状态。

相关 race 检查覆盖 `internal/candidate`、`internal/devmode` 和 `cmd/clawflow/commands`，52 项通过。四项回归曾在旧代码上失败，修复后通过，分别覆盖构建只读、失败重启证据隔离、同大小同时间的内容变更，以及矛盾成功状态。

在临时 PostgreSQL 上构建并启动准确提交，候选 controller 和各容量 1 的 operator/review worker 注册成功，任务数为 0。停止三个候选进程后，启停层校验通过，稳定数据没有变化；未发起模型请求。重复启动被拒绝，故意传入错误提交后，停止和校验均保持失败，历史成功未被复用。

本地完整记录目录：

`/var/folders/64/xyvthqtj4xqb6xp70kw9hgf80000gq/T/clawflow-pr617-fixed-9zbjgjaa`

- `real-lifecycle-evidence.json`：修复后成功的启停记录，runtime `1ece1213e976`。
- `result.json`：各步骤、worker 注册和故意失败用例结果。
- 当前 `evidence.json`：最后一次故意失败用例，预期为失败；不能与前述成功记录混用。

候选及临时数据库均已停止。这里的成功仅覆盖已执行的构建和启停层；记录明确保留 `native-visual: pending`，未执行 live-write/save-reload，不代表独立验收通过。

## #611：原生视图交互证据

隔离预览从准确提交复制 Swift 源码，保留原始 `MenuSummary`，仅将模型连接至合成 fixture 和 mock CLI，并在独立窗口展示视图。独立 bundle、合成项目和非本机连接避免调用稳定服务。来源摘要及测试接线说明见本地 `source-provenance.json`。

已实际验证并保存截图和辅助功能树：

- `compact.png`：紧凑窗口中的状态、任务和固定底部操作。
- `pending.png` / `pending.ax.txt`：操作中提示及按钮禁用。
- `error.png` / `error.ax.txt`：明确错误反馈，按钮恢复，状态未误报成功。
- `success.png` / `success.ax.txt`：暂停后监控项目 17→16、暂停项目 7→8，运行中和排队任务均保留。
- `scrolled.png` / `scrolled.ax.txt`：项目区域有界滚动，底部操作保留。
- `keyboard-tab.png` / `keyboard-tab.ax.txt`、`keyboard-reverse.ax.txt`：Tab 和 Shift-Tab 移动焦点。
- `disconnected.png` / `disconnected.ax.txt`：断连后保留旧数据，明确标识最后已知状态并禁用操作。
- `unavailable.png` / `unavailable.ax.txt`：首次不可用时报告未知，不误报空闲。

本地证据目录：`/tmp/clawflow-pr611-native-acceptance`。最后的 `idle.png` 仍显示混合任务状态，不能作为空闲状态通过证据。未声称真实菜单栏锚定或 VoiceOver 朗读已验证。

预览已退出。稳定 controller、worker、PostgreSQL 和原有执行进程的 PID 与本轮开始相同，未重启服务。

## 独立验收的执行边界

全局 AGENTS.md 明确禁用 Offload；正式独立验收会调用 Offload。用户随后明确授权继续开启，并指定 CC profile。本轮显式指定 `CLAWFLOW_OFFLOAD_PROFILE=claude-claude-sonnet-5-1m-inherit-inherit`（endpoint `cc`、模型 `claude-sonnet-5[1m]`），按四个 PR、串行且每个最多一轮/30 分钟的边界推进。之前未授权阶段没有启动模型验收。没有伪造开发元数据、审核身份或通过结论，也没有调用要求继承 reservation 文件描述符的内部入口来绕过调度。

#611、#617 已通过 ClawFlow CLI 发布本轮验证进展评论；评论发布成功不代表验收通过或已经合并。

## 后续 CC 独立验收

#618 的正式验收命令退出 0：attempt `82e9d771-0cae-4b4a-b9d9-e2da9b9b4dee`，planner isolation 为 `tools-disabled`。审核会话 `c807418d-4db1-40e5-87e5-4b89058266d8`，初审 run `0e274b181878451492a530acdc131686`，同会话格式修正 run `8c517c56c8f0779a2f05dc14d85ec80f`；两次均核实 profile 为上述 CC profile、模型 `claude-sonnet-5[1m]`。格式修正属于同轮审核，没有重新启动独立审核。最终正式记录 completed/passed，22 项检查。

审核引用准确提交的 Test CI `124176330125` 和 Native macOS CI `124176330374`，并结合源代码核对覆盖范围。本地 Go 测试超时没有作为通过证据，原生视觉行为保留为未验证的建议项。测试可能继承正式状态目录并等待容量锁，这是待确认的环境诊断，不作为产品缺陷结论。后续调用不显式传入 `CLAWFLOW_STATE_DIR`；已核实兼容别名仍解析到相同正式配置。

#618 随后的 `clawflow pr merge --no-delete-branch` 退出 0，报告合并成功。#611 在该步骤结束后才启动本轮验收，避免并发变更基分支。

远端只读核对确认 #618 为 merged，合并提交 `453883f68354ed850738f7339e323ce9634e7b69`。#611 首次入口在模型启动前被 `clawflow-skip` 拒绝；issue #610 的历史说明它是本地接手的防重复调度标记。本次按用户继续验收授权临时解除，保留 `agent-failed` 和 `agent-implemented`，验收若阻塞则恢复该标记。#617 首次入口在模型启动前遇到现有 controller 巡检 PID 28730 持有的锁，未删除锁或终止进程。#609 正式入口退出 1，原因仍为 `trusted development provenance unavailable`，没有启动模型审核。

#611 本轮真实审核 attempt `58343a27-344e-45c4-896e-30d5cba1a9ce`，会话 `cdd34e2c-90d0-4e21-b9bc-91cc3a77ee69`，初审 run `35888aaac8fdc361bd3cc1fa2bd19424`，同会话修正 run `8037a022daf7e24c3641c435be48fa31`。两者均使用 CC profile。模型认为 22/23 项通过、AC-21 原生交互证据缺失，未发现代码缺陷；但修正后仍未满足 compatible proof 校验，CLI 最终退出 1，保存的正式结论是 `failed` / `unstructured report cannot authorize acceptance`，不能将模型的 blocked 文本当成正式验收结果。

独立模型提示中包含默认禁止桌面交互的本地 SOP，没有收到前轮隔离预览的截图/辅助功能树及项目 AGENTS.md 中的 ClawFlow 专属授权。本地已有证据不等于已被独立模型接受；后续需要把准确提交的证据和授权范围交接到受支持的审核输入，再产生新的有效结构化报告。本轮没有再开第二次独立审核或豁免 AC-21。

#611 的 `clawflow-skip` 已成功恢复，并通过 ClawFlow CLI 发布中文澄清评论。#617 在巡检释放锁后再次进入正式入口，退出 1，原因 `latest development run is absent, incomplete or unsuccessful`；没有启动模型审核。此次只是前置锁冲突后的来源核对，不是第二轮模型验收。#609 和 #617 均未更改来源记录或合并门禁。

本轮结束时稳定 controller PID 91217、worker PID 91406、local worker PID 91221、PostgreSQL PID 61176 保持原值；未执行服务重启或部署。#618 的源码合并不表示新服务代码已经部署。
