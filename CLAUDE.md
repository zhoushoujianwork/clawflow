# CLAUDE.md

## 语言偏好

始终用中文回复用户。用户正在学习中文。

---

## 发版规范

每次新版本发布必须按以下步骤执行，不能跳过：

### 1. 构建二进制

```bash
go build -o clawflow ./cmd/clawflow/
```

### 2. 打 Tag 并推送

```bash
git tag v{x.y.z}
git push origin v{x.y.z}
```

版本号规则：
- `v0.x.0` — 新功能（minor）
- `v0.x.y` — bug 修复（patch）
- `v1.0.0` — 首个稳定版

### 3. 创建 GitHub Release 并上传二进制

```bash
gh release create v{x.y.z} ./clawflow \
  --title "v{x.y.z} — {简短描述}" \
  --notes "{release notes}"
```

**必须上传 `clawflow` 二进制**作为 release asset，方便用户无需安装 Go 直接下载使用。

### 4. 验证

```bash
gh release view v{x.y.z}
# 确认 assets 列表里有 clawflow 二进制文件
```

---

## .gitignore 说明

| 规则 | 原因 |
|------|------|
| `/config/` | 用户配置（现存于 `~/.clawflow/config/`，不进 repo） |
| `clawflow` | 构建产物（通过 release asset 分发） |
