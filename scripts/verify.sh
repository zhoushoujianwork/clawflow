#!/usr/bin/env bash
# One verification entry shared by CI and local runs (implement, repair-ci,
# humans). There is no full suite: tests run only for the Go packages a change
# touches, compared with the base ref. Every change still gets gofmt, vet,
# a build and a CLI smoke check.
#
# Usage: scripts/verify.sh [all|go|web]...   (all = go + web)
#   VERIFY_BASE  base ref to diff against (default: origin/main)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BASE="${VERIFY_BASE:-origin/main}"

step() { printf '\n==> %s\n' "$*"; }

# Packages (as ./dir) holding Go files changed since the merge base, plus
# uncommitted and untracked changes. Deleted directories are skipped.
changed_go_packages() {
  base="$(git merge-base HEAD "$BASE" 2>/dev/null || echo "$BASE")"
  { git diff --name-only "$base" -- '*.go'; git diff --name-only HEAD -- '*.go'; git ls-files --others --exclude-standard -- '*.go'; } |
    while read -r f; do dirname "$f"; done | sort -u |
    while read -r dir; do
      if [ -d "$dir" ] && ls "$dir"/*.go >/dev/null 2>&1; then echo "./$dir"; fi
    done
}

verify_go() {
  step "gofmt"
  unformatted="$(gofmt -l cmd internal skills ./*.go 2>/dev/null || true)"
  if [ -n "$unformatted" ]; then
    echo "gofmt needed:"; echo "$unformatted"; return 1
  fi
  step "go vet"
  go vet ./...
  step "tests for changed packages (base $BASE)"
  pkgs="$(changed_go_packages)"
  if [ -z "$pkgs" ]; then
    echo "no changed Go packages"
  else
    # shellcheck disable=SC2086
    go test -race $pkgs
  fi
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

verify_web() {
  step "web build"
  # Non-interactive: a pnpm version change must not wait for a TTY prompt.
  pnpm --dir web install --frozen-lockfile --config.confirmModulesPurge=false
  pnpm --dir web build
}

[ $# -eq 0 ] && set -- all
for target in "$@"; do
  case "$target" in
    all) verify_go; verify_web ;;
    go) verify_go ;;
    web) verify_web ;;
    *) echo "unknown target: $target" >&2; exit 2 ;;
  esac
done
step "verify passed: $*"
