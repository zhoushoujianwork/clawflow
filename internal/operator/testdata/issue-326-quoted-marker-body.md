## 🔍 ClawFlow Bug Evaluation

**Reproducibility:** 9/10 — 现场可直接复现且已被我逐条核实。`~/.clawflow/data/runs/daboluocc__daboluo-office/issue-24/` 下三次运行全部 `status=no-marker`、算子均为 `evaluate-bug`，实付 $0.7212 / $0.7477 / $1.3787（合计 $2.85），三份正文都含完整的三维度打分行与 `**Confidence:** 8.7|8.0|8.3/10`。`~/.clawflow/logs/run.log` 中 `issue=24` 只有 `run/lock` → `run/claude_start` → `run/end status=no-marker`，无任何排除记录。扣 1 分：`agent-failed` 打标那一刻的日志/stderr 未留存，熔断触发的那一跳只能由代码路径推断，无法在日志里直接看到。

**Root cause:** 9/10 — 两处代码都能精确定位，且与既有注释形成直接矛盾。

**Fix difficulty:** 7/10 — 建议 1 是单行改动但会连带改动既有测试断言；建议 2 需要给 matcher 增加结构化原因并控制日志噪声，不是纯加一行。

**Confidence:** 8.3/10 ✅ 高于阈值

### Repro steps

1. 让某个 issue 的 `evaluate-bug` 连续三次以 `no-marker` 收尾（正文完整、缺 `<!-- clawflow:outcome=... -->` 行）。
2. `checkCircuitBreaker`（`cmd/clawflow/commands/run.go:1435`）读到 `ConsecutiveFailures == 3 >= maxFails`，执行 `j.client.AddLabel(..., "agent-failed")`（`run.go:1451`），且按设计不留评论（`run.go:1453-1457` 注释「No accompanying comment by design」）。
3. 人工加 `ready-for-agent`。`implement` 的 `labels_excluded` 含 `agent-failed`（`skills/implement/SKILL.md:10`），`MatchesWithReason` 在 `internal/operator/matcher.go:71-75` 返回 `excluded label "agent-failed" present`。
4. 该 reason 只经 `debugf("  ✗ %s: %s", ...)`（`run.go:690`）输出，而 `debugf` 在 `cmd/clawflow/commands/debug.go:16-21` 被 `Debug` 开关拦住且只写 stderr，不进 `run.log`。结果：标签加了、算子不动、零日志。

复现验证（本机实测）：

```
$ grep -iE "excluded|skip_excluded" ~/.clawflow/logs/run.log | wc -l   # 46650 行日志中 0 命中
$ grep "issue=24 " ~/.clawflow/logs/run.log | grep -c "agent-failed"   # 0
```

### Root cause analysis

**缺陷 1：`no-marker` 被当作「issue 永久坏了」计入熔断**（`internal/snapshot/snapshot.go:1465`）

```go
if r.status != "failed" && r.status != "no-marker" && r.status != "skipped-empty" && r.status != "output-limit" {
    break
}
```

函数自身的 doc comment（`snapshot.go:1432-1440`）把不计数的判据写成「描述基础设施拒绝、而非 issue 本身有问题」。`no-marker` 恰好符合这条判据：算子跑完、正文合格、坏的是写回环节。`run.go:1261-1266` 的注释给出的理由是「否则 issue 保持未打标、每轮重新触发（#143）」——这个理由在 #307 的 salvage 落地后已经过期：完整模板正文现在会走 `marker-recovered`，`run.go:1278-1286` 明确写了该状态「deliberately NOT counted by the circuit breaker」。也就是说同一份正文，装上 salvage 是「不计熔断」，salvage 没生效就是「计熔断」——分类依据落在了写回实现的版本上，而非 issue 状态上。

**为什么本轮 salvage 没救回来**：不是判据不满足。`internal/operator/salvage.go:54-91` 要求「≥2 个维度分 + Confidence 行」，office#24 三份正文全部满足（我实测三份都有 3/3 维度行 + Confidence 行）。真实原因是二进制陈旧：

```
$ clawflow --version                                        # v0.72.0-18-gabca3a2-dirty
$ strings ~/.clawflow/bin/clawflow | grep -c marker-recovered   # 0
```

`run.log` 里 issue=24 的 `run/end` 还是 `INFO` 级别，而 #314（`a5070ac`）已把 `no-marker` 提到 `WARN`（`run.go:1327-1331`），进一步印证跑的是旧码。这与 **#312** 是同一根因，本 issue 是它的下游放大。

**缺陷 2：label 排除完全无痕迹**。`MatchesWithReason` 已经把原因算出来了，但两个调用点都没落盘到 `run.log`：`run.go:688` 走 `debugf`（默认关闭、只到 stderr），`run.go:945` 只覆盖「poll 之后标签变了」的窄场景。对比同一函数附近的 `run/skip_became_parent`（`run.go:964`）和 `run/skipped_rate_limit`（`run.go:886`）都有 `runLog.Info` —— 排除路径是这批 skip 里唯一无日志的。

**历史脉络**：#307/#314 修 salvage，#308（`bfe958d`）修 402 误计熔断——同一类「状态分类错」。`internal/snapshot/consecutive_failures_test.go:44-98` 已经把「基础设施拒绝不得触发熔断」钉成契约，本 issue 主张 `no-marker` 属于同一类别。这块代码近期 churn 较高（`snapshot.go` 与 `salvage.go` 五次提交里有两次是熔断/写回相关），是 Fix difficulty 扣分的一部分。

### Suggested fix

1. **`snapshot.go:1465` 摘掉 `no-marker`**，并同步更新 doc comment 说明理由（正文已付费且有 salvage 路径，坏的是写回而非 issue）。

   ⚠️ 注意副作用：现循环用 `break`，摘掉后 `no-marker` 会**打断**streak，把它后面的真失败一并清零。这会直接打破既有断言 `internal/snapshot/consecutive_failures_test.go:88-97`（序列 `failed, no-marker, failed, success` 现期望 3，改后得 1）。需要明确取舍并改测试：
   - 若认可「打断」语义（与 `cost-limit` / `rate-limited` 一致），改断言为 1 并注明；
   - 若只想「不计数但不打断」，需要把循环从 `break` 改成 `continue` 分流，区分「中性状态」与「终止状态」两个集合。推荐后者，语义更准，改动仍限于本函数。

2. **给排除路径加日志**。在 `run.go:688` 的 `MatchesWithReason` 之后补一条 `runLog.Info("run/skip_excluded", "repo", ..., "issue", ..., "op", op.Name, "by", <命中的标签>)`。两个约束：
   - 只在「required 全部满足、仅因 excluded 被拒」时打，否则每轮 issue × 算子的笛卡尔积会淹掉 `run.log`；
   - 需要拿到命中的标签名而非整句 reason，建议给 `MatchesWithReason` 增加结构化返回（如 `MatchReason{Kind, Label}`），或新增 `ExcludedBy(sub, op) (string, bool)` 辅助函数，保持现有字符串 reason 不变以免影响 `--debug` 输出。

3. **补测试**：`consecutive_failures_test.go` 加一例「三次纯 `no-marker` 不得触发默认阈值」，形状照 #308 那个 `t.Run("three cost-limit passes stay below the default threshold")` 抄；matcher 侧加一例断言排除命中时能拿到标签名。

4. **现场解封（人工，不在代码范围）**：`daboluocc/daboluo-office#24` 已有的 `agent-failed` 不会被本修复自动摘掉，需要手工移除后 `ready-for-agent` 才生效。同样状态的还有 `#313`、`#312`（均带 `agent-failed`）。

5. **止血依赖**：本修复合入后同样不会生效，直到 #312 解决（`~/.clawflow/bin/clawflow` 重建）。建议在 PR 描述里显式标注这一前置。

建议 3 里的可选项（熔断打标时留评论）**不做**，建议 2 的日志已足够 Pilot 与人自查，加评论只是噪声。

---

👉 If this plan looks right, add the `ready-for-agent` label to kick off automatic implementation.
