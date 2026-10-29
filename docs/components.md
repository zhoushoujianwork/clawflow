# 组件清单与显式绑定

`clawflow components` 管理独立的本机清单，保存在
`~/.clawflow/data/components.json`（schema 1，文件权限 0600）。它不修改项目定义、
Offload 主项目、仓库 enablement、执行 profile 或自动化设置，也不迁移旧配置。

```sh
clawflow components discover
clawflow components list
clawflow components register offload
clawflow components verify offload
clawflow components bind offload --project my-project --role implement \
  --resource settings.execution --capability execution-declarations \
  --credential-ref projects/my-project/credentials/implement
clawflow components activate offload --project my-project --role implement
clawflow components disable offload --project my-project --role implement
```

项目必须已经存在。`discover` 和 `list` 只读，不创建清单或运行探测。
`register` 只登记已知适配器，状态为 `discovered`，不授权执行。
`verify` 才调用只读探测，并保存身份、组件版本指纹、协议版本、能力和检查时间。
适配器版本是独立的 `version` 字段；组件版本保存在 `pin.version`。
`bind` 固定项目、角色、资源引用和批准的能力，仍不启用。
`activate` 显式激活指定项目/角色，必须通过新一次探测，并与版本指纹一致。

版本或身份变化、能力不兼容、未知协议、探测错误、取消或不可用健康状态均拒绝操作；
探测失败将现有绑定禁用。`verify` 会清除旧绑定，要求重新绑定和激活，不能静默升级。
`disable` 停止该绑定的授权；再次启用需重新验证、绑定、激活。
同一组件可绑定多个项目，但每个项目/角色单独授权。
清单更新使用现有 filelock 和原子替换，失败探测的禁用状态也会持久化。

凭证字段只接受 `projects/<project>/credentials/<role>` 形式的槽位引用，
不接受 token、带密码的 URL 或其他项目的槽位。这里不读取或存储凭证值，
也不替用户创建凭证；资源字段同样只接收公开的符号引用。不得把密钥放入任何名称字段。
探测错误使用固定错误文本，不记录底层输出或错误体。

## 已知组件及验证边界

| 组件 | 复用接口与验证范围 |
| --- | --- |
| ClawFlow project / VCS / operator | 对应现有模块，版本固定到当前可执行文件 SHA-256；健康仅表示本地模块可用 |
| Offload | 复用 `execution.ProbeCapabilities` 的受限 `-h` 检查；版本固定到可执行文件及有效执行配置的组合 SHA-256，不保存 profile 或路径原文 |
| GitHub Actions | 已知 `internal/vcs/github` 精确修订 CI 模块的清单项；缺少项目级远端探测证据时健康不可用，拒绝验证和激活 |

Offload 的 `execution-declarations` 仅表示已有调用参数得到声明，不表示真实 provider
执行、权限强制或凭证可用。没有通过帮助输出推测版本号；版本指纹用于阻止替换后继续使用旧绑定。
不安装、启动或接管未知组件；历史 cloud-worker issue 不产生清单项。

## 适配器契约与当前边界

`internal/component.Adapter` 提供只读 `Discover` 和 `Probe`。
替换适配器必须保留适配器标识、版本、引用及声明，并返回兼容的协议、身份、
版本指纹、健康和能力证据；适配器代码负责核实真实接口，不能仅按产品名称报告可用。
`ReadAdapter` 可注入其他经过核实的实现，不复制现有执行或 CI 逻辑。
`GitHubActions` 适配器复用现有 `GetRepoID` / `GetCIEvidence`，验证仓库身份和
指定 head 的检查标识；测试失败与服务不可用分开处理。它接收调用方已经限定项目和
请求上下文的 provider，不自行取得全局 token。CLI 默认没有项目凭证解析器，所以保留
不可用状态，不会用现有全局凭证冒充项目授权。

这是组件管理入口，不是新调度器。现有 runner 不读取该清单，`active` 不启动工作，
不改变现有任务、执行或合并授权。未来消费者在执行前必须调用 `Registry.Authorize`，
并使用对应项目的凭证解析器和既有执行/证据接口；失效状态应通过 `Update` 持久化。
没有提供通用命令执行、任意 URL 注册、远端凭证解析或自动服务接管。
真实 provider smoke、独立适配器验收和 GitHub Actions 运行仍需分别验证。
