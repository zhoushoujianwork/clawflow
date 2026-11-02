# 本机安全滚动升级首次部署

2026-11-02 14:11–14:13（+08），按用户“有开发完的就合并跟部署”的授权，将此前已合并的 [PR #631](https://github.com/zhoushoujianwork/clawflow/pull/631) 更新到本机 controller、两条 worker admissions 和独立安装的 CLI。

## 运行版本和验收

- 精确源码：`5e0eec3f74fa0666a3f11069a2d19451978f59eb`。它已包含在 main 的合并提交 `2ef54db7` 中，两者源码树差异为空。使用已通过验收的 PR head 构建，避免将合并提交冒充已验收 head。
- 版本：`0.75.0-dev.20261102060908.5e0eec3f`；二进制 SHA-256：`a16f8d7a5dae80c8a56464f638221f0cd5c48ca8af98cd5c360c77ece115a6e0`。
- 精确 head 的 Test、Native macOS、Web render CI 均通过；独立验收 attempt `02f757fc-1bf2-445e-b740-53aafb11d6de` 通过，见 [原始报告](https://github.com/zhoushoujianwork/clawflow/pull/631#issuecomment-6548254287)。没有改写历史验收记录。
- 在独立 PostgreSQL 17 夹具上运行 upgrade、candidate、coordinator、store 四包完整 race 检查，全部通过。临时数据库结束后正常停止。
- 通过 candidate 库的既有隔离和进程所有权接口，把同一部署二进制复制到 candidate 自有 bin，实际启动隔离 controller 和两条 worker 并确认注册。未重编译为不同的 candidate 版本后冒用哈希。证据状态 passed，stable_changes 为空；未运行的 live-write、save-reload、native-visual 层保持原值。
- 证据、私有夹具日志、升级前后快照：`/tmp/clawflow-delivery-20261102/`。部署候选为其 `candidates/687a1eca6fef/evidence.json`。

## 切换和运行检查

使用 `clawflow upgrade plan` 预检，然后执行 `upgrade controller --execute`。记录 `20261102T061122Z-e84d8a12` 最终 succeeded：排空、保存原版本、迁移检查、暂停领取、停止旧 controller、获取所有权锁、更新 LaunchAgent、启动新版本、API/worker/租约验证和恢复派发均完成。schema 保持 11，没有新增迁移。

- controller PID `19445 → 11073`；服务指向升级器保留的不可变目标，而非临时构建目录。
- API 中断实测 2463 ms，采样分辨率 1000 ms；dashboard 端点恢复 2479 ms。不声称零停机。
- controller 启动时按既有 generation 流程更新 local-worker；对 local-cli-worker 执行 `upgrade workers --execute`，记录 `20261102T061155Z-5ab106f6` 最终 succeeded。
- 两个 generation descriptor 的固定 CLI 摘要均与目标一致；两个逻辑 worker 在线、未暂停，并行容量保持 4 和 1。
- 升级前没有运行中任务，因此本轮不能作为“长任务跨升级继续运行”的实测证明；该行为仍由已有隔离集成测试覆盖。前后全部 53 条历史任务保留，state、attempt、worker 字段没有变化。
- 独立安装入口 `~/Library/Application Support/ClawFlow/bin/clawflow` 已原子更新，版本和哈希已核对。

## 保留项和恢复边界

正式 PostgreSQL PID `63405` 和 `/Applications/ClawFlow.app` PID `49829` 保持不变，没有重启或安装 App，没有进行 SQLite 切换，也没有开启 self_upgrade。本轮只观察 API 和 worker 实际运行，未声称观察了原生 App 的重连。

正式数据库自定义格式备份已通过 `pg_restore --list`，旧 CLI、配置和相关 LaunchAgent 保留在私有目录 `~/Library/Application Support/ClawFlow/backups/delivery-631-20261102/`。首次备份命令失败后修正了连接参数传递方式，再次备份及检查成功；失败产物保留。升级器另有不可变旧版本和阶段记录。回退应按[滚动升级手册](controller-upgrade.md)验证兼容性，不用旧数据库覆盖新结果。

#634 的同步兼容修复和 #622 的 SQLite 迁移不包含在此次部署中，仍须分别通过当前提交的开发来源、CI、独立验收及各自运行门禁。未清空修复预算、删除历史 journal 或改写失败结果。
