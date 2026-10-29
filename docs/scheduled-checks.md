# 平台定时检查

定时检查是用户显式定义的 Pilot 调用。Web 服务按分钟检查当前应运行的任务，
不补跑停机期间的任务；首次启动所在分钟不会触发。与仓库自动运行间隔独立。
每个任务同时最多一个调用，重叠的时间点记为 skipped。完成的调用保留结果、
动作记录与执行后端实际报告的 usage；没有报告的 usage 为 null。

先执行版本迁移：

```sh
clawflow db migrate
clawflow pilot schedule add --cron '*/30 * * * *' --tz Asia/Shanghai \
  --prompt '检查是否有任务卡住，并依据现有策略处理' --ops 5 --timeout 3600
clawflow pilot schedule list
clawflow pilot schedule show <id>
clawflow pilot schedule disable <id>
clawflow pilot schedule enable <id>
clawflow pilot schedule remove <id>
```

cron 支持五个数字字段（分钟、小时、日期、月份、星期），星号、列表、范围和步长。
星期日可用 0 或 7。日期和星期同时受限时，满足任意一个即触发。
时区必须明确指定为 IANA 名称。夏令时跳过的本地时刻不会运行；重复的本地时刻
按两个不同的实际分钟处理。超时单位为秒，最大一天。

也可以在 Pilot 会话中要求列出、创建、修改或删除任务。Pilot 通过
schedule_add / schedule_update / schedule_remove 结构化动作提案管理任务，
共享会话的动作预算、每日上限与审计；任务执行也通过同一 Pilot Ask 路径，
保留现有仓库和验收策略。

连续三次调用失败会自动停用任务并在 Pipeline 的 Scheduled checks 面板标记。
重叠跳过不增加也不重置失败计数。重新 enable 清零失败计数。进程丢失的调用在
下次到期检查、超过其调用截止时间后记为失败。删除任务保留历史运行记录；
show 展示最近 50 次运行。面板展示最近 50 次跨任务运行。
