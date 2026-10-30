# GitHub App 认证

GitHub 仓库可使用 App 安装身份调用 API 和通过 HTTPS 执行 Git 操作。
安装覆盖的仓库使用 App；其他仓库保留个人 token 回退。未配置 App 时行为不变。
App 的 API 写入归属于 `<app>[bot]`；Git commit 作者仍由 Git 的 user.name/email 决定。
安装身份拥有独立于个人 token 的 API 配额，通常至少每小时 5,000 次请求；实际额度以 GitHub 响应头为准。
预算、条件请求缓存按安装身份隔离，令牌刷新不会重置安装的预算。

## 创建并安装

1. 在 GitHub 的 Settings → Developer settings → GitHub Apps 创建 App。
   只需后台调用时可关闭 webhook，无需用户授权回调。
2. 仅授予以下 Repository permissions：

   | 权限 | 级别 |
   | --- | --- |
   | Metadata | Read-only |
   | Contents | Read and write |
   | Issues | Read and write |
   | Pull requests | Read and write |
   | Checks | Read-only |
   | Actions | Read-only |
   | Commit statuses | Read-only |

   不授予 Administration、Secrets 或 Workflows write 权限。
   因此 App 不能提交修改 `.github/workflows/` 的变更，需要此能力的任务应由所有者处理。
3. 在 Install App 中选择目标用户或组织，并选择 Only select repositories，勾选需要管理的仓库。
4. 记录 App ID，生成并下载 RSA private key。将密钥安全复制到 mini 的本地文件中，
   执行 `chmod 600 /absolute/local/path/app.pem`，不要放进仓库。
5. 编辑 mini 上的 `~/.clawflow/config/config.yaml`，增加：

   ```yaml
   settings:
     github_app:
       app_id: 123456
       # 可省略：通过目标仓库的安装信息解析（包含 owner）。
       installation_id: 789012
       private_key_path: /absolute/local/path/app.pem
   ```

   `installation_id` 指定时，只使用该安装覆盖的仓库；其他安装和未安装仓库回退到个人 token。
   个人 token 继续通过 `clawflow config set-token` 或既有环境变量配置。
6. 执行 `clawflow repo show owner/repo` 查看 `github_identity`、安装授予的 `permissions`
   和 `repository_selection`（`selected` 或 `all`）。该命令不打印令牌或密钥路径。

安装令牌只缓存在进程内，提前五分钟刷新；并发调用共享刷新。JWT 使用 RS256，
`iat` 回退一分钟，`exp` 为当前时间后九分钟。网络或认证故障会返回错误，只有确认未覆盖
目标仓库时才回退，避免把临时故障误判成未安装。仓库覆盖信息最多缓存五分钟。

## Git 与 worker

App 模式下新克隆使用不含凭据的 HTTPS URL。已有 `git@github.com:owner/repo.git`
远程通过进程级 URL 重写使用 HTTPS，不更改 `.git/config`。Git helper 只为目标 HTTPS
仓库提供 `x-access-token` 用户名及安装令牌，不持久化凭据。
实现代理继承进程级 helper 配置，通过本地 `clawflow git-credential` 按需获取新令牌，
无需在代理环境里预存一小时后失效的令牌。显式 `https://user:token@...` 的旧远程应先移除内嵌凭据。

每台 coordinator 和 worker 分别配置自己的机器本地 App 凭据。worker 使用自己的
`settings.github_app`（以及自己的个人 token 回退），不依赖 coordinator 分发密钥。
配置同步既不上传也不导入 `github_app`；API/网页不返回此配置。不要将配置或密钥提交到 Git，
不要把 PEM、令牌或密钥路径粘贴到日志、issue 评论中。

配置与密钥轮换后重启长期运行进程，清空其内存中的旧安装令牌。删除安装或撤销权限的
实际生效时间由 GitHub 决定，不能将本地缓存当作持续授权证明。

参考：[GitHub JWT 文档](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app)、
[安装令牌文档](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app)。
