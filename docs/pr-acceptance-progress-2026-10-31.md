# 四个 PR 的验收推进记录

日期：2026-10-31。延续[此前合并协助记录](pr-merge-assistance-2026-10-31.md)。本轮未绕过来源、独立验收或 CI 门禁，尚未合并这四个 PR。

| PR | 当前提交 | 本轮结果 | 剩余条件 |
| --- | --- | --- | --- |
| [#609](https://github.com/zhoushoujianwork/clawflow/pull/609) | `d4745f4f89456ef48bc0e77db187e3a4d90290ae` | 来源门禁仍阻塞；本地 9371 份运行元数据中未找到 issue 608 的开发记录；旧执行主机 SSH 再次超时 | 从真实执行来源取得可信开发记录，不能补造 |
| [#611](https://github.com/zhoushoujianwork/clawflow/pull/611) | `5ba2716f68bcc2ec7f513c037e3e27f2c98bbf8e` | 补充实际原生预览交互证据 | 新一轮独立结构化验收；旧验收因证据不完整失败 |
| [#617](https://github.com/zhoushoujianwork/clawflow/pull/617) | `a75c39643022f80bd526b71efb3bfa42d658bd33` | 修复并推送，52 项相关 race 检查通过，当前提交 CI 通过，隔离真实启停通过 | 当前提交可信开发来源、独立验收；原生视觉层仍待验证 |
| [#618](https://github.com/zhoushoujianwork/clawflow/pull/618) | `edf1f73b93e03045a8542802f027a4cee0ff90e9` | 保留前轮 PostgreSQL 四包 1410 项、相关 race 31 项、Swift 严格并发 26 项通过记录 | 独立验收证据尚缺 |

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

全局 AGENTS.md 明确禁用 Offload；正式独立验收会调用 Offload。已请求仅限这四个 PR、串行且每个最多一轮/30 分钟的明确授权，尚未收到答复，因此本轮没有启动模型验收。没有伪造开发元数据、审核身份或通过结论，也没有调用要求继承 reservation 文件描述符的内部入口来绕过调度。

#611、#617 已通过 ClawFlow CLI 发布本轮验证进展评论；评论发布成功不代表验收通过或已经合并。
