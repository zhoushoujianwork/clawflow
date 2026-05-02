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

# Built-in operator skills ship inside the binary (go:embed) — no remote fetch
# needed. User operators stay in ~/.clawflow/skills/ and are not touched here.

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
echo "Quick start:"
if [[ -n "$NEED_SOURCE" ]]; then
  echo "  source $NEED_SOURCE"
fi
echo "  clawflow config set-token <ghp_...>        # GitHub token (scope: repo, read:org)"
echo "  clawflow config set-gitlab-token <glpat_...> # GitLab token (optional)"
echo "  clawflow repo add owner/repo               # add a repo"
echo ""
echo "Common commands:"
echo "  clawflow issue list --repo owner/repo      # view issues"
echo "  clawflow pr list --repo owner/repo         # view PRs"
echo "  clawflow label add --repo R --issue N --label bug"
echo ""
echo "Advanced (optional):"
echo "  clawflow run                               # run operator pipeline"
