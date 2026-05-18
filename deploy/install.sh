#!/usr/bin/env bash
# install.sh — bootstrap a ClawFlow cloud server on a fresh Ubuntu/Debian VM.
#
# Idempotent: re-running upgrades the binary and reloads systemd without
# rebuilding state. Run as root (or via sudo).
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/zhoushoujianwork/clawflow/main/deploy/install.sh | sudo bash
#   # — OR —
#   sudo bash deploy/install.sh /path/to/clawflow_linux_amd64
#
# What it does:
#   1. Creates the `clawflow` system user + /var/lib/clawflow data dir.
#   2. Installs the binary to /usr/local/bin/clawflow (from arg, or
#      downloads the latest release asset if no arg given).
#   3. Copies the systemd unit, env template (if absent), and Caddyfile
#      into /etc/systemd/system, /etc/clawflow, /etc/caddy.
#   4. Installs Caddy via apt if missing.
#   5. Enables but does NOT start the service — you must populate
#      /etc/clawflow/cloud.env first.

set -euo pipefail

if [[ $EUID -ne 0 ]]; then
    echo "install.sh must run as root (use sudo)" >&2
    exit 1
fi

REPO_RAW_URL="https://raw.githubusercontent.com/zhoushoujianwork/clawflow/main"
RELEASE_API_URL="https://api.github.com/repos/zhoushoujianwork/clawflow/releases/latest"
BIN_DST="/usr/local/bin/clawflow"
DATA_DIR="/var/lib/clawflow"
ETC_DIR="/etc/clawflow"
CADDY_CFG="/etc/caddy/Caddyfile"
SYSTEMD_UNIT="/etc/systemd/system/clawflow-cloud.service"

# ---- 1. user + data dir ----

if ! id -u clawflow >/dev/null 2>&1; then
    echo "Creating clawflow system user..."
    useradd --system --no-create-home --shell /usr/sbin/nologin clawflow
fi
install -d -o clawflow -g clawflow -m 0755 "$DATA_DIR"
install -d -o root -g root -m 0755 "$ETC_DIR"

# ---- 2. binary ----

if [[ $# -ge 1 && -f "$1" ]]; then
    echo "Installing binary from $1..."
    install -m 0755 "$1" "$BIN_DST"
else
    echo "Fetching latest release tag..."
    tag=$(curl -fsSL "$RELEASE_API_URL" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')
    if [[ -z "$tag" ]]; then
        echo "Could not resolve latest release tag" >&2
        exit 1
    fi
    asset_url="https://github.com/zhoushoujianwork/clawflow/releases/download/${tag}/clawflow_linux_amd64"
    echo "Downloading $asset_url..."
    tmp=$(mktemp)
    curl -fsSL -o "$tmp" "$asset_url"
    install -m 0755 "$tmp" "$BIN_DST"
    rm -f "$tmp"
fi

echo "Installed: $($BIN_DST version 2>/dev/null | head -1 || echo 'unknown')"

# ---- 3. systemd unit + env template ----

curl -fsSL "$REPO_RAW_URL/deploy/clawflow-cloud.service" -o "$SYSTEMD_UNIT"
chmod 0644 "$SYSTEMD_UNIT"

if [[ ! -f "$ETC_DIR/cloud.env" ]]; then
    curl -fsSL "$REPO_RAW_URL/deploy/clawflow-cloud.env.example" -o "$ETC_DIR/cloud.env"
    chown root:clawflow "$ETC_DIR/cloud.env"
    chmod 0640 "$ETC_DIR/cloud.env"
    echo "Wrote $ETC_DIR/cloud.env (template). Populate it before starting the service."
else
    echo "$ETC_DIR/cloud.env already exists — leaving alone."
fi

# ---- 4. Caddy ----

if ! command -v caddy >/dev/null 2>&1; then
    echo "Installing Caddy..."
    apt-get update -qq
    apt-get install -y -qq debian-keyring debian-archive-keyring apt-transport-https curl
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
        | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
        > /etc/apt/sources.list.d/caddy-stable.list
    apt-get update -qq
    apt-get install -y -qq caddy
fi

curl -fsSL "$REPO_RAW_URL/deploy/Caddyfile" -o "$CADDY_CFG"

# ---- 5. activate ----

systemctl daemon-reload
systemctl enable clawflow-cloud
systemctl restart caddy

echo ""
echo "==============================================================="
echo "Install complete. Next steps:"
echo ""
echo "  1. Edit $ETC_DIR/cloud.env and fill in the GitHub App secrets."
echo "  2. systemctl start clawflow-cloud"
echo "  3. Visit https://clawflow.daboluo.cc/api/v1/auth/me — should 401."
echo "  4. On a dev box, run: clawflow cloud login --url https://clawflow.daboluo.cc"
echo "==============================================================="
