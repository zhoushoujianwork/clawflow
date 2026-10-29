# Worker pool

一个托管平台负责仓库注册、调度、租约、结果写回与最终合并；客户端提交仓库和查看状态，
不运行本地调度器；worker 使用本机 Offload profile 和 VCS 凭据领取任务并执行，不能自行合并。
平台主机也只通过 worker 执行任务，可配置同机 worker。存储使用共享 PostgreSQL。

## 连接和注册

管理员在平台主机创建 `admin` token，客户端用私有文件登录：

```sh
clawflow platform login --url https://platform.example.com:8093 \
  --token-file /private/path/admin.token --fingerprint <SHA-256>
clawflow platform status
clawflow repo add https://github.com/owner/repo --disabled
clawflow repo enable owner/repo
```

每次注册或启用前先检查 `platform status`，成功后报告 add/enable 响应中的平台 URL。
未连接平台就停止；连接失败也不能回退为本地注册。客户端不传 `--local-path`，不启动
`clawflow run` 或 web scheduler。平台注册跳过 label 初始化。
`admin` token 管理平台仓库；`worker` token 只允许注册 worker、领取、续租和报告任务，不能互换。

## 新增执行机器

仅在明确要求增加容量时启动 worker。机器需安装 ClawFlow、配置 Offload profile 与 VCS
凭据，并由管理员提供独立的 worker token 文件和证书指纹：

```sh
clawflow worker --coordinator https://platform.example.com:8093 \
  --token-file /private/path/worker.token --fingerprint <SHA-256> \
  --tags github --max-parallel 1
```

标签必须覆盖任务要求；无匹配 worker 的任务保持等待，不回退到其他机器。
worker 不需要 admin 登录或本地启用仓库。同机 worker 也遵循相同租约与标签规则。
现有单机安装在未启用 coordinator 时仍保持原行为；托管注册流程不使用该路径。

平台初始化、token 管理、标签示例及数据模型见 [实现细节](worker-pool-design-detail.md)。
