#!/usr/bin/env bash
# ClawFlow source installer — for users cloning the repo to build/modify.
#
# For most users: use the one-liner from get.sh instead
#   curl -fsSL https://raw.githubusercontent.com/zhoushoujianwork/clawflow/main/get.sh | bash
#
# This script does four things:
#   1. builds the clawflow binary from source into ~/.clawflow/bin/
#   2. seeds ~/.clawflow/config/config.yaml from the template (only if absent)
#   3. writes ~/.clawflow/config/install.yaml so `clawflow update --from-source`
#      knows where this checkout lives for future rebuilds
#   4. optionally creates the ClawFlow label set in a repo (--create-labels)
#
# Built-in operators ship inside the binary (go:embed), so there is no skill
# directory to install — unlike older releases that wrote SKILL.md files
# into ~/.claude/skills/.

set -e

REPO_ROOT="$(cd "$(dirname "$0")" && pwd)"
CLAWFLOW_HOME="$HOME/.clawflow"
CREATE_LABELS_REPO=""

# ---------- argument parsing ----------
while [[ $# -gt 0 ]]; do
  case "$1" in
    --create-labels) CREATE_LABELS_REPO="$2"; shift 2 ;;
    -h|--help)
      cat <<'USAGE'
Usage: ./install.sh [--create-labels <owner/repo>]

Options:
  --create-labels <owner/repo>   After install, run `clawflow label init`
                                 to create the standard label set on that repo.

Examples:
  ./install.sh
  ./install.sh --create-labels my-org/my-repo
USAGE
      exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

echo "Installing ClawFlow from source..."
echo "  Repo     : $REPO_ROOT"
echo "  Data dir : $CLAWFLOW_HOME"
echo ""

# ---------- 1. seed user data directory ----------
mkdir -p "$CLAWFLOW_HOME/bin" "$CLAWFLOW_HOME/config"

CFG_DST="$CLAWFLOW_HOME/config/config.yaml"
if [[ -f "$CFG_DST" ]]; then
  echo "  [skip] $CFG_DST already exists — keeping your version"
else
  cp "$REPO_ROOT/config/config.yaml" "$CFG_DST"
  echo "  [ok]   $CFG_DST created from template"
fi

# ---------- 2. build the binary ----------
if ! command -v go &>/dev/null; then
  echo "  [err]  Go toolchain not found — install Go first" >&2
  exit 1
fi

VERSION="dev"
if command -v git &>/dev/null; then
  # `git describe --tags` gives us a meaningful Version on source builds.
  TAG="$(git -C "$REPO_ROOT" describe --tags --dirty --always 2>/dev/null || true)"
  [[ -n "$TAG" ]] && VERSION="$TAG"
fi

echo ""
echo "Building clawflow (version=$VERSION)..."
LDFLAGS="-s -w -X github.com/zhoushoujianwork/clawflow/cmd/clawflow/commands.Version=$VERSION"
if go build -ldflags "$LDFLAGS" -o "$CLAWFLOW_HOME/bin/clawflow" "$REPO_ROOT/cmd/clawflow/"; then
  echo "  [ok]   binary installed → $CLAWFLOW_HOME/bin/clawflow"
else
  echo "  [err]  go build failed" >&2
  exit 1
fi

CLAWFLOW_BIN="$CLAWFLOW_HOME/bin/clawflow"

# ---------- 3. write install record (for `clawflow update --from-source`) ----------
cat > "$CLAWFLOW_HOME/config/install.yaml" <<YAML
repo_dir: $REPO_ROOT
installed_at: $(date -u +%Y-%m-%dT%H:%M:%SZ)
YAML
echo "  [ok]   install record → $CLAWFLOW_HOME/config/install.yaml"

# ---------- 4. optional: create ClawFlow labels on a repo ----------
if [[ -n "$CREATE_LABELS_REPO" ]]; then
  echo ""
  echo "Creating ClawFlow labels on $CREATE_LABELS_REPO..."
  if ! "$CLAWFLOW_BIN" label init "$CREATE_LABELS_REPO"; then
    echo "  [warn] label init failed — run \`clawflow label init $CREATE_LABELS_REPO\` manually after setting up tokens" >&2
  fi
fi

# ---------- done ----------
cat <<MSG

Done. ClawFlow is installed.

Next steps:
  1. Add CLI to PATH (bash/zsh):
       export PATH="\$HOME/.clawflow/bin:\$PATH"
  2. Store a VCS token:
       clawflow config set-token <ghp_...>         # GitHub
       clawflow config set-gitlab-token <glpat_..> # GitLab
  3. Register a repo to monitor:
       clawflow repo add <owner/repo | URL | local path>
  4. Run the operator loop:
       clawflow run

Built-in operators:
       clawflow operators list
MSG
