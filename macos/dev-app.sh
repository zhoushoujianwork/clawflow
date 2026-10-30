#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
case "${1:-}" in ""|--install-restart) ;; *) echo "Usage: $0 [--install-restart]" >&2; exit 2 ;; esac
base=$(git describe --tags --abbrev=0 2>/dev/null || echo v0.0.0)
commit=$(git rev-parse --short HEAD)
dirty=""
if [ -n "$(git status --porcelain)" ]; then dirty=".dirty"; fi
version="${base#v}-dev.$(date -u +%Y%m%d%H%M%S).$commit$dirty"
export CLAWFLOW_APP_VERSION="$version" CLAWFLOW_APP_CHANNEL=dev
make build VERSION="$version" CHANNEL=source
./macos/build-app.sh
if [ "${1:-}" = --install-restart ]; then
    python3 macos/install-dev-app.py "${CLAWFLOW_DEV_INSTALL_DIR:-/Applications}"
fi
