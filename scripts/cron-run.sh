#!/usr/bin/env bash
# ClawFlow cron runner — wraps `claude -p "ClawFlow run"` with rich logging.
# Usage: add to crontab via `clawflow cron install`
# Log: ~/.clawflow/logs/cron.log

set -euo pipefail

# ── PATH ──────────────────────────────────────────────────────────────────────
export PATH="$HOME/.clawflow/bin:/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:$PATH"

# Ensure node is visible (claude-hud plugin requires it)
if ! command -v node &>/dev/null; then
  NODE_CANDIDATES=(
    /opt/homebrew/bin/node
    /usr/local/bin/node
    "$HOME/.nvm/versions/node/$(ls "$HOME/.nvm/versions/node" 2>/dev/null | tail -1)/bin/node"
  )
  for candidate in "${NODE_CANDIDATES[@]}"; do
    if [[ -x "$candidate" ]]; then
      export PATH="$(dirname "$candidate"):$PATH"
      break
    fi
  done
fi

# ── Auth / Proxy env ──────────────────────────────────────────────────────────
ENV_FILE="$HOME/.clawflow/config/env"
if [[ -f "$ENV_FILE" ]]; then
  # shellcheck source=/dev/null
  source "$ENV_FILE"
fi

# ── Config ────────────────────────────────────────────────────────────────────
REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
CLAUDE="${CLAUDE_BIN:-/Users/mikas/.claude/local/claude}"
LOG_DIR="$HOME/.clawflow/logs"
LOG_FILE="$LOG_DIR/cron.log"
MAX_LOG_LINES=5000   # rotate when log exceeds this

mkdir -p "$LOG_DIR"

# ── Log rotation ──────────────────────────────────────────────────────────────
if [[ -f "$LOG_FILE" ]]; then
  line_count=$(wc -l < "$LOG_FILE")
  if (( line_count > MAX_LOG_LINES )); then
    tail -n $((MAX_LOG_LINES / 2)) "$LOG_FILE" > "$LOG_FILE.tmp" && mv "$LOG_FILE.tmp" "$LOG_FILE"
  fi
fi

# ── Run ───────────────────────────────────────────────────────────────────────
START_TIME=$(date '+%Y-%m-%d %H:%M:%S')
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━" >> "$LOG_FILE"
echo "▶ ClawFlow run  $START_TIME" >> "$LOG_FILE"
echo "  repo:   $REPO_DIR" >> "$LOG_FILE"
echo "  claude: $CLAUDE" >> "$LOG_FILE"
echo "  node:   $(command -v node 2>/dev/null || echo 'not found')" >> "$LOG_FILE"
echo "  clawflow: $(clawflow version 2>/dev/null || echo 'not found')" >> "$LOG_FILE"
echo "  env:    ${ENV_FILE} $([ -f "$ENV_FILE" ] && echo '(loaded)' || echo '(not found)')" >> "$LOG_FILE"
echo "" >> "$LOG_FILE"

EXIT_CODE=0
TMP_JSON=$(mktemp)
TMP_ERR=$(mktemp)

cd "$REPO_DIR"
"$CLAUDE" -p "ClawFlow run" --output-format stream-json --verbose \
  > "$TMP_JSON" 2>"$TMP_ERR" || EXIT_CODE=$?

# ── Parse assistant output & token stats ──────────────────────────────────────
python3 - "$TMP_JSON" <<'PYEOF' >> "$LOG_FILE"
import json, sys

path = sys.argv[1]
with open(path) as f:
    for line in f:
        line = line.strip()
        if not line:
            continue
        try:
            obj = json.loads(line)
            t = obj.get('type', '')
            if t == 'assistant':
                for block in obj.get('message', {}).get('content', []):
                    if block.get('type') == 'text':
                        text = block['text'].strip()
                        if text:
                            print(text)
            elif t == 'result':
                u = obj.get('usage', {})
                cost = obj.get('total_cost_usd') or obj.get('cost_usd') or 0
                dur  = obj.get('duration_ms', 0)
                turns = obj.get('num_turns', 0)
                print("")
                print("── Token Usage ──────────────────────────────────────")
                print(f"  input:        {u.get('input_tokens', 0):,}")
                print(f"  output:       {u.get('output_tokens', 0):,}")
                print(f"  cache read:   {u.get('cache_read_input_tokens', 0):,}")
                print(f"  cache write:  {u.get('cache_creation_input_tokens', 0):,}")
                print(f"  cost:         ${cost:.4f} USD")
                print(f"  duration:     {dur/1000:.1f}s  ({turns} turns)")
        except Exception:
            pass
PYEOF

# ── Stderr (errors / warnings) ────────────────────────────────────────────────
if [[ -s "$TMP_ERR" ]]; then
  echo "" >> "$LOG_FILE"
  echo "── stderr ───────────────────────────────────────────────────────────────" >> "$LOG_FILE"
  cat "$TMP_ERR" >> "$LOG_FILE"
fi

rm -f "$TMP_JSON" "$TMP_ERR"

END_TIME=$(date '+%Y-%m-%d %H:%M:%S')
echo "" >> "$LOG_FILE"
if [[ $EXIT_CODE -eq 0 ]]; then
  echo "✓ done  $END_TIME  (exit 0)" >> "$LOG_FILE"
else
  echo "✗ failed  $END_TIME  (exit $EXIT_CODE)" >> "$LOG_FILE"
fi
echo "" >> "$LOG_FILE"

exit $EXIT_CODE
