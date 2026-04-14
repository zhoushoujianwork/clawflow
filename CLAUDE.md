# CLAUDE.md

## 语言偏好

始终用中文回复用户。用户正在学习中文。

---

## 发版规范

每次新版本发布只需两步：

### 1. 打 Tag 并推送

```bash
git tag v{x.y.z}
git push origin v{x.y.z}
```

GitHub Actions（`.github/workflows/release.yml`）会自动完成：
- 三平台构建（darwin arm64/amd64、linux amd64）
- 创建 GitHub Release 并上传所有平台二进制

版本号规则：
- `v0.x.0` — 新功能（minor）
- `v0.x.y` — bug 修复（patch）
- `v1.0.0` — 首个稳定版

### 2. 验证用户可以自动更新

```bash
gh release view v{x.y.z}
# 确认 assets 列表有各平台文件（Actions 完成后）

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
