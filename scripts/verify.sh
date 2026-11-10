#!/usr/bin/env bash
# One verification entry shared by CI and local runs (implement, repair-ci,
# humans). Run it from any worktree; heavy tools are cached per user, not per
# worktree, so repeated runs on one host stay warm:
#   - Go build/module caches: Go's own per-user defaults
#   - pnpm store: pnpm's per-user content-addressed store
#   - Playwright + Chromium: $CLAWFLOW_VERIFY_CACHE (default ~/.cache/clawflow-verify)
#
# Usage: scripts/verify.sh [all|go|web|web-build|render|native]...
#   all (default) = go + web, plus native on macOS.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PLAYWRIGHT_VERSION=1.56.1
CACHE_DIR="${CLAWFLOW_VERIFY_CACHE:-$HOME/.cache/clawflow-verify}"

step() { printf '\n==> %s\n' "$*"; }

verify_go() {
  step "gofmt"
  unformatted="$(gofmt -l cmd internal skills ./*.go 2>/dev/null || true)"
  if [ -n "$unformatted" ]; then
    echo "gofmt needed:"; echo "$unformatted"; return 1
  fi
  step "go vet"
  go vet ./...
  step "go test -race"
  go_test_sharded
  step "CLI smoke"
  bin="$(mktemp -d)/clawflow"
  go build -o "$bin" ./cmd/clawflow/
  "$bin" --help >/dev/null
  "$bin" version
  "$bin" operators list >/dev/null
  # Hard fail if any embedded SKILL.md has bad frontmatter.
  "$bin" operators validate
  for cmd in run db repo issue label pr config status worktree pr-check update web controller dashboard "worker join"; do
    # shellcheck disable=SC2086
    "$bin" $cmd --help >/dev/null
  done
}

# The commands package holds most of the suite and runs its tests serially
# (they swap package globals), so it is split by test name across separate
# processes; each process keeps its own globals and temp HOME. Every other
# package runs in one normal `go test` alongside the shards.
SLOW_PKG=./cmd/clawflow/commands
go_test_sharded() {
  shards="${VERIFY_SHARDS:-4}"
  logs="$(mktemp -d)"
  pids=()
  go test -race $(go list ./... | grep -v '/cmd/clawflow/commands$') >"$logs/rest.log" 2>&1 &
  pids+=($!)
  # One compile, reused by every shard through the build cache.
  go test -race -run '^$' "$SLOW_PKG" >/dev/null
  tests="$(go test -list '^Test' "$SLOW_PKG" | grep '^Test')"
  for i in $(seq 0 $((shards - 1))); do
    names="$(printf '%s\n' "$tests" | awk -v n="$shards" -v i="$i" 'NR % n == i' | paste -sd'|' -)"
    [ -z "$names" ] && continue
    go test -race -count=1 -run "^($names)\$" "$SLOW_PKG" >"$logs/shard$i.log" 2>&1 &
    pids+=($!)
  done
  failed=0
  for pid in "${pids[@]}"; do wait "$pid" || failed=1; done
  for f in "$logs"/*.log; do
    if [ "$failed" = 1 ] || [ -n "${VERIFY_VERBOSE:-}" ]; then cat "$f"; else grep -E '^(ok|FAIL|---)' "$f" || true; fi
  done
  rm -rf "$logs"
  return "$failed"
}

verify_web_build() {
  step "web build"
  # Non-interactive: a pnpm version change must not wait for a TTY prompt.
  pnpm --dir web install --frozen-lockfile --config.confirmModulesPurge=false
  pnpm --dir web build
}

# Locally the Playwright module lives in the shared cache; CI may preset
# PLAYWRIGHT_MODULE after installing browsers with system dependencies.
ensure_playwright() {
  if [ -n "${PLAYWRIGHT_MODULE:-}" ]; then
    return 0
  fi
  tools="$CACHE_DIR/render-tools-$PLAYWRIGHT_VERSION"
  if [ ! -f "$tools/node_modules/playwright/index.mjs" ]; then
    step "install playwright@$PLAYWRIGHT_VERSION into $tools"
    npm install --prefix "$tools" --no-package-lock "playwright@$PLAYWRIGHT_VERSION"
  fi
  "$tools/node_modules/.bin/playwright" install chromium
  export PLAYWRIGHT_MODULE="$tools/node_modules/playwright/index.mjs"
}

verify_render() {
  ensure_playwright
  step "render fixtures"
  (
    cd web
    for fixture in scripts/check-*-render.mjs; do
      echo "Running $fixture"
      node "$fixture"
    done
  )
}

verify_native() {
  if [ "$(uname -s)" != "Darwin" ]; then
    echo "native: skipped (requires macOS)"; return 0
  fi
  step "swift test"
  swift test --package-path macos
}

[ $# -eq 0 ] && set -- all
for target in "$@"; do
  case "$target" in
    all) verify_go; verify_web_build; verify_render; verify_native ;;
    go) verify_go ;;
    web) verify_web_build; verify_render ;;
    web-build) verify_web_build ;;
    render) verify_render ;;
    native) verify_native ;;
    *) echo "unknown target: $target" >&2; exit 2 ;;
  esac
done
step "verify passed: $*"
