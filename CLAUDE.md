# CLAUDE.md

## 语言偏好

始终用中文回复用户。用户正在学习中文。

---

## 发版规范

每次新版本发布必须按以下步骤执行，不能跳过：

### 1. 构建二进制（含 OS/arch 后缀）

```bash
# macOS arm64（Apple Silicon）
GOOS=darwin  GOARCH=arm64  go build -o clawflow_darwin_arm64  ./cmd/clawflow/
# macOS amd64（Intel）
GOOS=darwin  GOARCH=amd64  go build -o clawflow_darwin_amd64  ./cmd/clawflow/
# Linux amd64
GOOS=linux   GOARCH=amd64  go build -o clawflow_linux_amd64   ./cmd/clawflow/
```

asset 命名格式：`clawflow_{os}_{arch}`（Windows 加 `.exe`）。
`clawflow update` 命令会自动按当前平台匹配对应 asset 名称。

### 2. 打 Tag 并推送

```bash
git tag v{x.y.z}
git push origin v{x.y.z}
```

版本号规则：
- `v0.x.0` — 新功能（minor）
- `v0.x.y` — bug 修复（patch）
- `v1.0.0` — 首个稳定版

### 3. 创建 GitHub Release 并上传所有平台二进制

```bash
gh release create v{x.y.z} \
  clawflow_darwin_arm64 \
  clawflow_darwin_amd64 \
  clawflow_linux_amd64 \
  --title "v{x.y.z} — {简短描述}" \
  --notes "{release notes}"
```

**必须上传各平台二进制**，`clawflow update` 依赖这些 asset 实现自动更新。

### 4. 验证用户可以自动更新

```bash
gh release view v{x.y.z}
# 确认 assets 列表有各平台文件

# 模拟用户更新
clawflow update
# 应输出：binary updated + SKILL.md updated
```

---

## 用户更新流程

用户安装后，后续版本只需运行：

```bash
clawflow update           # 从 GitHub 下载最新 binary + 更新 SKILL.md
clawflow update --from-source  # 从本地 repo 重新构建（开发用）
```

`clawflow update` 自动读取 `~/.clawflow/config/install.yaml`（由 install.sh 写入），
知道 SKILL.md 装在哪个 agent 目录里，并自动更新。

---

## .gitignore 说明

| 规则 | 原因 |
|------|------|
| `/config/` | 用户配置（现存于 `~/.clawflow/config/`，不进 repo） |
| `clawflow` | 构建产物根目录二进制（通过 release asset 分发） |
| `clawflow_*` | 各平台构建产物 |
