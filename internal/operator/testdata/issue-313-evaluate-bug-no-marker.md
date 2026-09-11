## 🔍 ClawFlow Bug Evaluation

**Reproducibility:** 9/10 — issue 给出了 `branch list` / `branch delete --yes` 的完整输出和仓库路径，我在本机 `/Users/mikas/github/easyeda-agent` 直接复现了判据：`git for-each-ref --merged=origin/main refs/heads/` 返回 21 行，其中 **21/21 的 `%(worktreepath)` 均非空**（含 `main` 自己），扣掉 `main` 后正是 20 个「eligible 但必然删不掉」的分支。`git worktree list --porcelain | grep -c '^branch '` = 22，与 issue 一致。扣 1 分只因复现依赖这台机器上这个特定 clone 的历史堆积，纯净仓库需要人为造 worktree。

**Root cause:** 9/10 — 根因定位准确且可在代码中直接指认。`internal/branch/branch.go:254` `ListMerged` 对本地分支只做了一层过滤（`branch.go:268` 的 `IsProtected(b.Name, base)`，而 `IsProtected` 仅覆盖 base/origin/`protectedNames`），完全没有 worktree 维度；`branch.go:305` `DeleteLocal` 委托 `git branch -d`，git 自身拒绝被 worktree 占用的分支。issue 说「注释里已写明」也对得上：`branch.go:301-304` 的注释确实写了 "or one held by a worktree fails with git's own error"。扣 1 分是因为 issue 建议解析 `git worktree list --porcelain`，实际上 `for-each-ref` 自带 `%(worktreepath)` 字段（本机 git 2.50.1 已验证有效），比外挂一次 porcelain 解析更省事——根因判断没错，只是给的手段不是最优解。

**Fix difficulty:** 8/10 — 止血方案（方案 1）是单文件局部改动：`refFormat`（`branch.go:61`）加一个 `%(worktreepath)` 字段、`parseRefLines`（`branch.go:64` 起）多解析一列、`Branch` 结构体（`branch.go:~40`）加一个 `WorktreePath string`，`ListMerged` 里加一条 `continue`。`ListMerged` 只有两个调用方（`cmd/clawflow/commands/branch.go:93` 和 `:149`），`DeleteLocal` 一个（`:209`），扇出很小。扣分项有两个：一是 `ParseRefLinesExported`（`branch.go:291`）被 `internal/api` 复用，改字段格式要同步核对（`internal/api/repo_branches.go:157` 用的是自己的 format，风险不大但要过一遍）；二是这块代码近期 churn 明显——#302、#311 都在改同一组函数的判据，**#311 目前 open 且带 `agent-failed`，它提议把 `DeleteLocal` 签名改成 `DeleteLocal(localPath, name, mergeRef string, force bool)`**，两个 fix 会在同一个函数上碰头。方案 2（自动 `git worktree remove`）风险高得多，不该混进同一个 PR。

**Confidence:** 8.7/10 ✅ above threshold

### Repro steps

1. 取一个跑过多轮 `implement` 算子的仓库（现成现场：`zhoushoujianwork/easyeda-agent`，本地 clone `/Users/mikas/github/easyeda-agent`），其 `.claude/worktrees/` 下堆着未回收的 Claude Code 子 worktree。
2. 确认 merge 判据两侧一致（排除 #311 干扰）：`git -C <clone> rev-list --count main..origin/main` → `0`。
3. `clawflow branch list --repo zhoushoujianwork/easyeda-agent` → 打印 20 行 `worktree-*` 并声称 `20 merged branch(es) eligible for cleanup`。
4. `clawflow branch delete --repo zhoushoujianwork/easyeda-agent --yes` → `0 deleted, 20 failed`，每条都是 `cannot delete branch '...' used by worktree at ...`。
5. 最小化验证（不依赖 clawflow）：`git -C <clone> for-each-ref --merged=origin/main --format='%(refname:short)|%(worktreepath)' refs/heads/`，会看到候选分支的 `worktreepath` 全部非空——这一列就是 `ListMerged` 漏掉的那一维。

### Root cause analysis

**历史脉络（已检索本仓库 issue 归档）**：这是 Play 1 分支清理的第四次判据修正。#229 引入功能 → #302 把 merge 判据从本地 base 升到 `origin/base`（已 closed，`agent-implemented`）→ #311 指出 `DeleteLocal` 没跟上（**open，带 `agent-failed`**）→ 本 issue 指出 list 侧还缺 worktree 维度。#297 更早就报过完全相同的错误文本（同一个 easyeda-agent 仓库，当时 11 个 worktree，现在 21 个），当时的结论是「`branch delete` 的守卫是对的，问题在 operator 生命周期缺清理」，修复落在 `cleanClaudeWorktrees`（`cmd/clawflow/commands/run.go:2296`）+ `clawflow worktree prune` 的 Phase 2（`cmd/clawflow/commands/worktree.go:216-259`）。本 issue 的定位是对 #297 结论的合理补充：**清理侧修了，但 list 侧的「eligible」语义从没修过**。

具体代码：

- `internal/branch/branch.go:262`
  ```go
  localOut, err := gitOut(localPath, "for-each-ref", "--merged="+mergeRef, "--format="+refFormat, "refs/heads/")
  ```
  `refFormat`（`branch.go:61`）= `"%(refname:short)%00%(committerdate:unix)"`，**没有取 `%(worktreepath)`**，所以 worktree 占用信息在数据层就丢了。
- `branch.go:268` 的过滤只有 `IsProtected(b.Name, base)`，而 `IsProtected`（`branch.go` 尾部）判的是 `name == base || name == "origin" || protectedNames[name]` —— 与 worktree 无关。
- `branch.go:305` `DeleteLocal` 执行 `git branch -d/-D`，git 对 worktree 占用是硬拒绝，**`-D` 也照样拒绝**（这点很重要：`--force` 救不了这个场景，所以不存在「用户加 `--force` 就好了」的规避路径）。

于是 `N merged branch(es) eligible for cleanup`（`cmd/clawflow/commands/branch.go:105`）这个数字的语义是「已合并且未被保护」，而不是它宣称的「可清理」。在 easyeda-agent 上两者差距是 20 vs 0。

**为什么自动路径没兜住**：`cleanClaudeWorktrees` 只在 `run.go:2218` 和 `run.go:2274` 被调用，两处都在算子执行的 worktree 生命周期收尾里，清理对象是 `<clawflow worktree>/.claude/worktrees/`——即 ClawFlow 自己创建的临时 worktree 内部的子 worktree。而 easyeda-agent 堆积的 21 个 worktree 挂在 **仓库主 clone** 的 `/Users/mikas/github/easyeda-agent/.claude/worktrees/` 下（我已用 `git worktree list` 确认路径），这条路径只有手动 `clawflow worktree prune` 的 Phase 2 会扫（它遍历 `cfg.Repos` 的 `LocalPath`）。`clawflow run` 从不调用它——`run.go:271` 调的是 `pruneOrphanedAnalysisWorktrees`，只管 `~/.clawflow/worktrees/*/analysis-*`。issue 对这一点的描述准确。

顺带一个 issue 没提的观察：`git worktree list` 里还有 3 个 `/private/tmp/claude-503/.../scratchpad/wt*` 已标记 `prunable` 的 detached worktree，说明这个 clone 的 worktree 注册表本身也需要一次 `git worktree prune`。

### Suggested fix

**Step 1（止血，建议单独 PR，不碰 `DeleteLocal`）** —— 让 `eligible` 数字诚实：

1. `internal/branch/branch.go:61` 扩展 format：
   ```go
   const refFormat = "%(refname:short)%00%(committerdate:unix)%00%(worktreepath)"
   ```
   `%(worktreepath)` 对未被占用的分支返回空串，对 remote-tracking ref 恒为空，语义天然安全。
2. `Branch` 加 `WorktreePath string`（json tag 跟随现有风格），`parseRefLines` 解析第三段；**注意向后兼容**：`ParseRefLinesExported` 是导出的，字段少于 3 段时不要 panic，缺失即视为空。
3. `ListMerged` 本地分支循环里，`IsProtected` 之后加：
   ```go
   if b.WorktreePath != "" { heldByWorktree++; continue }
   ```
   把 `heldByWorktree` 通过返回值或一个轻量 struct 带出去（或者提供 `ListMergedWithStats`，避免改现有签名影响 #311 的改动面）。
4. `cmd/clawflow/commands/branch.go:105` / `:170` 的输出加一行，与 `baseLagNote` 同款风格：
   ```
   note: 3 merged branch(es) held by a worktree (not eligible; run 'clawflow worktree prune' to release)
   ```
   这样 20/20 失败变成 `no merged branches to clean up` + 一行可操作提示，Play 1 的每轮 20 条告警直接归零。

**测试**：`internal/branch/branch_test.go` 已有 `TestListMergedIntegration` / `TestListMergedLaggingLocalBase` 用 `t.TempDir()` 建真实 git 仓库的先例，照抄一个 `TestListMergedExcludesWorktreeHeld`：建仓 → 建分支 → merge 进 main → `git worktree add` 占住该分支 → 断言 `ListMerged` 不返回它、且 held 计数为 1。

**Step 2（回收，独立 issue/PR）** —— 把 Phase 2 接进自动路径。倾向于 issue 里提的「`clawflow run` 收尾跑一次」而非「delete 时顺手 `git worktree remove`」：清理路径应保持只读语义，`branch delete` 里内联一个带条件判断（无未提交改动 + 不领先 origin/base）的 `worktree remove` 会把安全边界压在最容易被 `--force` 绕过的地方。建议做法是把 `worktree.go:216-259` 的 Phase 2 抽成 `pruneOrphanClaudeWorktrees(cfg) (found, removed int)`，在 `run.go:271` 那次 `pruneOrphanedAnalysisWorktrees` 旁边调用，并顺带跑一次 `git worktree prune` 清掉已标 prunable 的注册项。默认只清 **无未提交改动且不领先 origin/base** 的，其余打印跳过原因。

**排序与冲突提示**：#311 open 且要改 `DeleteLocal` 的签名，本 issue 的 Step 1 完全不碰 `DeleteLocal`，两者可并行；但如果两个 PR 同时开工，`ListMerged` 附近会有 diff 重叠，建议先落本 issue 的 Step 1（改动更小、风险更低、直接消掉噪声），再处理 #311。

---

👉 If this plan looks right, add the `ready-for-agent` label to kick off automatic implementation.
