#!/usr/bin/env bash
# ClawFlow one-line installer — macOS & Linux
# Usage: curl -fsSL https://raw.githubusercontent.com/zhoushoujianwork/clawflow/main/get.sh | bash

set -e

REPO="zhoushoujianwork/clawflow"
CLAWFLOW_HOME="$HOME/.clawflow"
CONFIG_DIR="$CLAWFLOW_HOME/config"

# ---------- platform detection ----------
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$OS" in
  darwin|linux) ;;
  *) echo "error: unsupported OS '$OS' — ClawFlow supports macOS and Linux only."; exit 1 ;;
esac

case "$ARCH" in
  x86_64)        ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "error: unsupported architecture '$ARCH'"; exit 1 ;;
esac

ASSET="clawflow_${OS}_${ARCH}"
DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"

echo "Installing ClawFlow..."
echo "  Platform : ${OS}/${ARCH}"
echo ""

# ---------- helper ----------
fetch() {
  local url="$1" dest="$2"
  if command -v curl &>/dev/null; then
    curl -fsSL "$url" -o "$dest"
  elif command -v wget &>/dev/null; then
    wget -qO "$dest" "$url"
  else
    echo "error: curl or wget is required"; exit 1
  fi
}

# ---------- resolve install dir ----------
# Prefer /usr/local/bin (system-wide, no PATH setup needed).
# Fall back to ~/.local/bin if we don't have write access.
if [[ -w /usr/local/bin ]]; then
  BIN_DIR="/usr/local/bin"
elif sudo -n true 2>/dev/null; then
  BIN_DIR="/usr/local/bin"
  USE_SUDO=1
else
  BIN_DIR="$HOME/.local/bin"
fi

# ---------- create directories ----------
mkdir -p "$CONFIG_DIR" "$CLAWFLOW_HOME/memory/repos"
if [[ "$BIN_DIR" == "$HOME/.local/bin" ]]; then
  mkdir -p "$BIN_DIR"
fi

# ---------- download binary ----------
echo "  [dl] downloading ${ASSET}..."
TMP_BIN="$(mktemp)"
fetch "$DOWNLOAD_URL" "$TMP_BIN"
chmod +x "$TMP_BIN"
if [[ -n "${USE_SUDO:-}" ]]; then
  sudo mv "$TMP_BIN" "$BIN_DIR/clawflow"
else
  mv "$TMP_BIN" "$BIN_DIR/clawflow"
fi
echo "  [ok] binary → $BIN_DIR/clawflow"

# ---------- detect agent & install SKILL.md ----------
AGENT=""
SKILL_DEST=""

if [[ -d "$HOME/.claude/skills" ]]; then
  AGENT="claude"
  SKILL_DEST="$HOME/.claude/skills/clawflow"
elif [[ -d "$HOME/.openclaw/skills" ]]; then
  AGENT="openclaw"
  SKILL_DEST="$HOME/.openclaw/skills/clawflow"
fi

if [[ -n "$SKILL_DEST" ]]; then
  mkdir -p "$SKILL_DEST"
  echo "  [dl] fetching skill file list..."
  API_URL="https://api.github.com/repos/${REPO}/contents/skills/clawflow"
  if command -v curl &>/dev/null; then
    LISTING=$(curl -fsSL "$API_URL")
  else
    LISTING=$(wget -qO- "$API_URL")
  fi
  # 从 Contents API 响应中提取 download_url（每行一个）
  DOWNLOAD_URLS=$(echo "$LISTING" | grep '"download_url"' | grep -o 'https://[^"]*')
  while IFS= read -r url; do
    [[ -z "$url" ]] && continue
    filename="${url##*/}"
    fetch "$url" "$SKILL_DEST/$filename"
    echo "  [ok] $filename → $SKILL_DEST/$filename"
  done <<< "$DOWNLOAD_URLS"
else
  echo "  [skip] no agent detected — install Claude Code or OpenClaw first, then re-run"
fi

# ---------- init config (skip if already exists) ----------
if [[ ! -f "$CONFIG_DIR/repos.yaml" ]]; then
  cat > "$CONFIG_DIR/repos.yaml" << 'YAML'
# ClawFlow monitored repositories
# Add repos you want ClawFlow to watch for issues.
#
# Example:
# repos:
#   - repo: owner/repo-name
#     enabled: true
repos: []
YAML
  echo "  [ok] config → $CONFIG_DIR/repos.yaml (default template)"
else
  echo "  [skip] config already exists — keeping your version"
fi

# ---------- write install record ----------
cat > "$CONFIG_DIR/install.yaml" <<YAML
agent: ${AGENT:-unknown}
skill_dir: ${SKILL_DEST:-}
repo_dir: ""
installed_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)
YAML
echo "  [ok] install record saved"

# ---------- PATH setup (only needed for ~/.local/bin) ----------
NEED_SOURCE=""
if [[ "$BIN_DIR" == "$HOME/.local/bin" ]]; then
  PATH_LINE='export PATH="$HOME/.local/bin:$PATH"'
  SHELL_RC=""
  case "${SHELL:-}" in
    */zsh)  SHELL_RC="$HOME/.zshrc" ;;
    */bash) SHELL_RC="$HOME/.bashrc" ;;
  esac

  if [[ -n "$SHELL_RC" ]] && ! grep -q '.local/bin' "$SHELL_RC" 2>/dev/null; then
    printf '\n# ClawFlow\n%s\n' "$PATH_LINE" >> "$SHELL_RC"
    echo "  [ok] PATH added to $SHELL_RC"
    NEED_SOURCE="$SHELL_RC"
  fi
fi

# ---------- done ----------
echo ""
echo "ClawFlow installed successfully."
echo ""
echo "Next steps:"
if [[ -n "$NEED_SOURCE" ]]; then
  echo "  source $NEED_SOURCE"
fi
echo "  clawflow config set-token          # GitHub token (scope: repo, read:org)"
echo "  clawflow config set-gitlab-token   # GitLab token (scope: api) — if needed"
echo "  clawflow repo add owner/repo       # add a repo to monitor"
echo "  # then tell your agent: ClawFlow run"
