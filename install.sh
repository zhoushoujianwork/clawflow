#!/usr/bin/env bash
# ClawFlow Skill Installer
# Usage: ./install.sh [--agent claude|openclaw|custom] [--dir <path>] [--create-labels <owner/repo>]

set -e

SKILL_NAME="clawflow"
REPO_ROOT="$(cd "$(dirname "$0")" && pwd)"
CLAWFLOW_HOME="$HOME/.clawflow"

# ---------- argument parsing ----------
AGENT=""
CUSTOM_DIR=""
CREATE_LABELS_REPO=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --agent)         AGENT="$2"; shift 2 ;;
    --dir)           CUSTOM_DIR="$2"; shift 2 ;;
    --create-labels) CREATE_LABELS_REPO="$2"; shift 2 ;;
    -h|--help)
      echo "Usage: ./install.sh [OPTIONS]"
      echo ""
      echo "Options:"
      echo "  --agent claude              Install into Claude Code (~/.claude/skills/)"
      echo "  --agent openclaw            Install into OpenClaw (~/.openclaw/skills/)"
      echo "  --agent custom --dir <path> Install into a custom skills directory"
      echo "  --create-labels <owner/repo> Create required GitHub labels in a repo"
      echo ""
      echo "Examples:"
      echo "  ./install.sh"
      echo "  ./install.sh --agent claude --create-labels my-org/my-repo"
      exit 0 ;;
    *) echo "Unknown option: $1"; exit 1 ;;
  esac
done

# ---------- auto-detect agent ----------
if [[ -z "$AGENT" ]]; then
  if [[ -d "$HOME/.claude/skills" ]]; then
    AGENT="claude"
  elif [[ -d "$HOME/.openclaw" ]]; then
    AGENT="openclaw"
  else
    echo "Could not auto-detect agent type."
    echo "Please specify: ./install.sh --agent claude|openclaw|custom [--dir <path>]"
    exit 1
  fi
fi

# ---------- resolve skill target directory ----------
case "$AGENT" in
  claude)   SKILLS_DIR="$HOME/.claude/skills" ;;
  openclaw) SKILLS_DIR="$HOME/.openclaw/skills" ;;
  custom)
    if [[ -z "$CUSTOM_DIR" ]]; then
      echo "Error: --agent custom requires --dir <path>"
      exit 1
    fi
    SKILLS_DIR="$CUSTOM_DIR"
    ;;
  *)
    echo "Unknown agent: $AGENT. Use claude, openclaw, or custom."
    exit 1 ;;
esac

SKILL_DEST="$SKILLS_DIR/$SKILL_NAME"

echo "Installing ClawFlow..."
echo "  Agent skills dir : $SKILL_DEST"
echo "  User data dir    : $CLAWFLOW_HOME"
echo ""

# ---------- 1. install skill definition ----------
mkdir -p "$SKILL_DEST"
cp "$REPO_ROOT/skills/clawflow/SKILL.md" "$SKILL_DEST/SKILL.md"
echo "  [ok] SKILL.md installed"

# ---------- 2. init user data directory ----------
mkdir -p "$CLAWFLOW_HOME/config"
mkdir -p "$CLAWFLOW_HOME/memory/repos"

for f in repos.yaml labels.yaml; do
  DST="$CLAWFLOW_HOME/config/$f"
  if [[ -f "$DST" ]]; then
    echo "  [skip] ~/.clawflow/config/$f already exists — keeping your version"
  else
    cp "$REPO_ROOT/config/$f" "$DST"
    echo "  [ok] ~/.clawflow/config/$f created from template"
  fi
done

# ---------- 3. build and install clawflow CLI ----------
if command -v go &>/dev/null; then
  echo ""
  echo "Building clawflow CLI..."
  mkdir -p "$CLAWFLOW_HOME/bin"
  if go build -o "$CLAWFLOW_HOME/bin/clawflow" "$REPO_ROOT/cmd/clawflow/" 2>/dev/null; then
    echo "  [ok] clawflow binary installed to ~/.clawflow/bin/clawflow"
    echo "  [tip] Add to PATH: export PATH=\"\$HOME/.clawflow/bin:\$PATH\""
  else
    echo "  [warn] go build failed — CLI not installed (SKILL.md will use gh/git directly)"
  fi
else
  echo "  [skip] Go not found — clawflow CLI not built (install Go to enable)"
fi

# ---------- 4. create GitHub labels (optional) ----------
if [[ -n "$CREATE_LABELS_REPO" ]]; then
  echo ""
  echo "Creating GitHub labels in $CREATE_LABELS_REPO ..."

  if ! command -v gh &>/dev/null; then
    echo "  [error] GitHub CLI (gh) not found. Install from https://cli.github.com/"
    exit 1
  fi

  create_label() {
    local name="$1" color="$2" desc="$3"
    if gh label list -R "$CREATE_LABELS_REPO" --json name -q '.[].name' | grep -qx "$name"; then
      echo "  [skip] label '$name' already exists"
    else
      gh label create "$name" --color "$color" --description "$desc" -R "$CREATE_LABELS_REPO"
      echo "  [ok] label '$name' created"
    fi
  }

  create_label "ready-for-agent" "00FF00" "Owner approved — triggers ClawFlow fix pipeline"
  create_label "agent-evaluated"  "0075CA" "ClawFlow has assessed this issue and posted a proposal"
  create_label "in-progress"      "FFA500" "Agent is actively working on this issue"
  create_label "agent-skipped"    "BDBDBD" "Low confidence — needs more information"
  create_label "agent-failed"     "FF0000" "Agent attempted but failed"
fi

# ---------- done ----------
echo ""
echo "Done! ClawFlow is ready."
echo ""
echo "Next steps:"
echo "  1. Edit ~/.clawflow/config/repos.yaml — add repos to monitor"
echo "  2. Authenticate GitHub CLI: gh auth login"
if [[ -z "$CREATE_LABELS_REPO" ]]; then
  echo "  3. Create GitHub labels: ./install.sh --create-labels <owner/repo>"
fi
echo "  4. Add CLI to PATH: export PATH=\"\$HOME/.clawflow/bin:\$PATH\""
echo "  5. Tell your agent: ClawFlow run"
