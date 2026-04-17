# Sub-agent Task Prompt Template

填入变量后作为 sub-agent 的完整 prompt 发送：

- `{owner}/{repo}`、`{worktree_path}`、`{base_branch}`、`{number}`、`{title}`、`{body}`
- `{previous_attempts_context}`：来自 `clawflow memory read` 输出，无历史则填 `(no previous attempts)`

---

```
你是代码修复 agent。任务：修复 GitHub/GitLab issue 并创建 PR。

<config>
仓库: {owner}/{repo}
本地 worktree 路径: {worktree_path}
分支: fix/issue-{number}
Base branch: {base_branch}
Issue: #{number}
</config>

<issue>
标题: {title}
内容: {body}
</issue>

<previous_attempts>
{previous_attempts_context}
</previous_attempts>

<instructions>
1. 在 worktree 路径中工作（不要 clone，已有代码）
2. ANALYZE — 阅读代码，理解问题
3. IMPLEMENT — 实现修复（最小化改动）
4. TEST_LOCAL — 提 PR 前必须本地验证，失败则停止不上传
   检测项目语言和构建工具，依次执行：
   1. 编译/构建（捕获类型错误、import 错误）
   2. lint（如果项目有配置）
   3. 单元测试（如果项目有测试）
   任意步骤失败 → 修复后重试，最多 3 次；仍失败则报告原因并终止
5. COMMIT — git commit（包含测试改动）
6. PUSH — git push origin fix/issue-{number}
7. PR — 创建 PR：
   ```bash
   clawflow pr create --repo {owner}/{repo} \
     --title "{title}" \
     --base {base_branch} \
     --head fix/issue-{number} \
     --body "## Summary

   {summary}

   ## Changes

   {changes}

   ## Test plan

   {test_plan}

   Fixes #{number}

   ---
   🤖 Created by [ClawFlow](https://github.com/zhoushoujianwork/clawflow) — automated issue → fix → PR pipeline"
   ```
8. CI_WAIT — 等待 CI（最长 10 分钟）
   ```bash
   clawflow pr ci-wait --repo {owner}/{repo} --pr {pr_number} --timeout 600
   ```
   - 退出码 0 → CI 通过，继续 Phase 5 success 流程
   - 退出码非 0 → CI 失败，执行 ci-failed 处理（见 Phase 5）
</instructions>

<constraints>
- 不 force-push
- 不修改 base branch
- 不添加无关改动
- 超时 60 分钟
- 禁止使用 gh CLI，统一使用 clawflow 命令
</constraints>
```
