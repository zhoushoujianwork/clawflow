# 多个 GitLab 站点并存

同一台 ClawFlow 可以同时操作多套自建 GitLab（不同域名、不同版本、不同端口或部署前缀）。每个站点有自己的地址、凭据、版本和能力声明；仓库明确归属某个站点。添加、修改或删除一个站点不会改动另一个站点。

返回[GitLab CLI 参考](gitlab-cli.md)或[项目入口](../readme.md)。

## 添加第二个站点

假设现在只有一个旧站点（单站点配置，仓库只有 `base_url`，凭据是全局 `gitlab_token`）。这些配置不需要改动，继续有效。

1. **注册已有站点**，把它命名并显式沿用旧的全局 token（只能有一个站点这样做）：

   ```bash
   clawflow gitlab site add old --url http://gitlab-a.example.com --version 11.11 --use-legacy-token
   ```

   URL 与该站点仓库的 `base_url` 一致时，这些仓库自动绑定到 `old`，原有 token 继续使用。

2. **添加新站点**，地址写到部署前缀为止，不带 `/api/v4`：

   ```bash
   clawflow gitlab site add new --url https://gitlab-b.example.com:8443/gitlab --version 17.0 \
     --ssh-host ssh.gitlab-b.example.com:2222
   ```

   新站点**不会**得到旧 token。它没有凭据，直到你单独设置：

   ```bash
   printf %s "$TOKEN_B" | clawflow gitlab site set-token new --stdin
   ```

   也可以用环境变量：`clawflow gitlab site set new --token-env MY_B_TOKEN`，或直接设置 `GITLAB_TOKEN_NEW`。查找顺序：站点的 `token_env`、`GITLAB_TOKEN_<NAME>`、`credentials.yaml` 的 `gitlab_tokens.<name>`、仅限显式选择了 `--use-legacy-token` 的那一个站点使用旧全局 token。站点之间不会借用凭据。

3. **添加仓库**，用完整项目 URL，仓库会绑定到所属站点，键为 `new:team/tools/service`：

   ```bash
   clawflow repo add https://gitlab-b.example.com:8443/gitlab/team/tools/service
   ```

4. **检查结果**（不访问网络）：

   ```bash
   clawflow gitlab site list
   clawflow repo list
   clawflow gitlab site resolve https://gitlab-b.example.com:8443/gitlab/team/tools/service
   ```

配置保存后，下一次 CLI 调用立即使用新配置。长时间运行的进程（controller、worker）在下次重新读取配置时生效。已经开始执行的任务保持启动时的仓库归属，不受后来的站点修改影响。

## 仓库如何选定

所有命令（`issue`、`pr`、`ci`、`queue`、`repo`、`run --repo` 等）以及 App 的仓库选择使用同一套规则：

| 输入 | 选中 |
|---|---|
| `https://host[:port][/前缀]/group/sub/project`（可带 `/-/merge_requests/3` 等） | URL 前缀匹配的站点。scheme、端口、部署前缀和完整 namespace 都要对得上 |
| `site:group/sub/project` | 该别名的站点 |
| `git@host:group/project.git`、`ssh://git@host:2222/group/project.git` | 只在 SSH 地址已通过 `--ssh-host` 绑定，或与站点 API 主机相同（且不重复）时选中。不做猜测映射 |
| `group/sub/project`（简写） | 配置中唯一匹配的仓库 |
| `owner/repo`（两段，未配置） | GitHub |

三段以上的路径永远不会被当成 GitHub。

### 消除歧义

多个站点（或 GitHub 与 GitLab）有同名路径时，简写会报错并列出所有候选，而不是默认第一个：

```text
repository "team/tools/service" is ambiguous; it matches 2 configured repositories:
gitlab site=new https://gitlab-b.example.com:8443/gitlab team/tools/service; gitlab site=old http://gitlab-a.example.com team/tools/service.
Pass the full project URL or one of the keys new:team/tools/service, team/tools/service (see: clawflow repo list)
```

改用完整 URL 或 `new:team/tools/service`。队列条目同理：`clawflow queue add new:team/tools/service#7`。

站点返回 401/403/404 时，ClawFlow 不会换另一个站点或另一份凭据重试。401 的报错会写明站点名、地址和请求路径，并说明这是认证或权限问题，不代表版本不兼容。

### SSH 地址与 API 地址不一致

Git SSH 主机、端口常与 Web/API 不同。给站点绑定 SSH 地址：

```bash
clawflow gitlab site set new --ssh-host ssh.gitlab-b.example.com:2222
```

一个 SSH 地址只能绑定一个站点。克隆使用站点的第一个 `ssh_hosts`；没有绑定时才使用 API 主机。

## 版本差异与能力声明

旧版 GitLab 可能缺少某些 API。用 `--unsupported` 声明站点缺少的能力，用 `--supported` 取消声明：

```bash
clawflow gitlab site set old --unsupported issue_events --unsupported mr_pipeline
```

| 能力 | 用途 |
|---|---|
| `issue_events` | issue 历史里的状态/标签事件 |
| `ci_variable_scope` | 带环境范围的 CI/CD 变量 |
| `mr_pipeline` | MR 的 head pipeline（CI 状态判断） |
| `runners` | 项目 runner 列表 |
| `job_play` | 启动手动 job |

对声明缺失的能力，请求**不会发出**，错误会写明站点、仓库和能力，并说明这不是认证错误。只影响该站点，其他站点不变。

CI 门禁不会因此放宽：缺少 `mr_pipeline` 的站点不能报告 CI 成功，也不会被当成“没有 CI”而放行；需要 CI 的仓库会因此保持阻塞，直到你处理为止。

## 数据隔离

- 仓库身份：同一站点内的项目 ID 才有意义。运行历史记录同时记录站点；两个站点即使项目 ID、路径、issue 编号相同，历史、队列状态和锁也在不同目录。同一站点内删除重建同名项目仍会归档旧历史（#272）。
- 旧的无站点记录在首次绑定站点时被接纳一次，之后按该站点比对。
- 目录名使用 `site+group__project`；`+` 不会出现在站点名或项目路径中，因此可以还原。

## 输出与密钥

`repo list/show`、`gitlab site list/show`、App 的仓库行、报错和审计都显示平台、站点、地址和完整路径。token 不出现在任何输出里，`set-token` 只回显最后四位。`credentials.yaml` 权限为 0600，`config.yaml` 不含 token。

## 限制

- 能力声明由你维护；ClawFlow 不自动探测服务器版本。
- 站点在仍有仓库绑定时不能删除，先 `clawflow repo remove` 这些仓库。
- 重命名站点别名需要移除并重新添加其仓库，因为别名是仓库键的一部分。
