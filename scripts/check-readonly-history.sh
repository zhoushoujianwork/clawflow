#!/usr/bin/env bash
# Isolated end-to-end check for issue #803: a disabled repository keeps its issues
# and failed attempts visible to a read-only LAN browser, and nothing else opens.
#
# Everything lives in a temp directory (HOME, CLAWFLOW_STATE_DIR, ports). The
# "provider" is a local mock that records every request; the script fails if it
# sees one, or if the repository configuration changes while pages are read.
#
# Needs: a LAN interface address (the server treats loopback as the owner), python3,
# node, Chrome/Chromium, and a playwright module (PLAYWRIGHT_MODULE, default "playwright").
#   CLAWFLOW_BIN  legacyweb build to serve (default: build ./cmd/clawflow with -tags legacyweb;
#                 run `pnpm --dir web build` first)
#   CHROME        browser executable (default: macOS Google Chrome)
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
PIDS=()
cleanup() { for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done; rm -rf "$WORK"; }
trap cleanup EXIT

CHROME="${CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}"
LAN="$(ipconfig getifaddr en0 2>/dev/null || python3 -c "import socket;s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.connect(('192.0.2.1',9));print(s.getsockname()[0])" 2>/dev/null || true)"
case "$LAN" in ''|127.*) echo "no non-loopback address available; cannot exercise a read-only client" >&2; exit 2;; esac
BIN="${CLAWFLOW_BIN:-}"
if [ -z "$BIN" ]; then BIN="$WORK/clawflow"; (cd "$ROOT" && go build -tags legacyweb -o "$BIN" ./cmd/clawflow); fi
read -r MOCK_PORT WEB_PORT < <(python3 -c "
import socket
s=[socket.socket() for _ in range(2)]
[x.bind(('',0)) for x in s]
print(*[x.getsockname()[1] for x in s])")

export HOME="$WORK/home" CLAWFLOW_STATE_DIR="$WORK/state"
unset CLAWFLOW_EFFECT_SPOOL CLAWFLOW_OFFLOAD_ENGINE
RUN="$CLAWFLOW_STATE_DIR/data/runs/grp__smoke/issue-1/2026-10-27T11-00-00Z"
mkdir -p "$HOME/.clawflow/config" "$RUN"

cat > "$WORK/mock.py" <<PY
import http.server
log = open("$WORK/provider.requests", "a")
class H(http.server.BaseHTTPRequestHandler):
    def _r(self):
        log.write(self.command + " " + self.path + "\n"); log.flush()
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers(); self.wfile.write(b"[]")
    do_GET = do_POST = do_PUT = do_DELETE = _r
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", $MOCK_PORT), H).serve_forever()
PY
: > "$WORK/provider.requests"
python3 "$WORK/mock.py" & PIDS+=($!)

cat > "$HOME/.clawflow/config/config.yaml" <<YAML
repos:
  grp/smoke:
    enabled: false
    platform: gitlab
    base_url: http://127.0.0.1:$MOCK_PORT
    base_branch: main
    ci_required: true
settings:
  coordinator:
    enabled: false
YAML
echo "gitlab_token: glpat-FAKE-ISOLATED-TOKEN-000000" > "$HOME/.clawflow/config/credentials.yaml"
cp "$HOME/.clawflow/config/config.yaml" "$WORK/config.before"
cat > "$CLAWFLOW_STATE_DIR/data/issues.json" <<'JSON'
[{"repo":"grp/smoke","issue_number":1,"issue_title":"Greeting: add a name argument","state":"open","captured_at":"2026-11-10T08:00:00Z","updated_at":"2026-11-10T08:00:00Z","created_at":"2026-11-10T07:00:00Z"}]
JSON
cat > "$RUN/meta.json" <<'JSON'
{"operator":"implement","repo":"grp/smoke","issue_number":1,"issue_title":"Greeting: add a name argument","issue_body":"PRIVATE ISSUE BODY","started_at":"2026-11-10T08:30:00Z","ended_at":"2026-11-10T08:30:18Z","status":"failed","error":"Invalid prompt: your prompt was flagged as potentially violating our usage policy\nworktree /Users/alice/Library/Application Support/ClawFlow/wt/issue-1 token=glpat-LEAKME1234567890","summary":"PRIVATE OPERATOR SUMMARY","offload_run_id":"off-run-1","machine":"exec-host","execution_status":"error","exit_code":1,"error_kind":"execution","elapsed_ms":18022,"profile":"p","backend":"b","model":"m","development_session":{"role":"developer","workdir":"/Users/alice/wt"}}
JSON
echo '{"type":"assistant","message":"PRIVATE TRANSCRIPT"}' > "$RUN/events.jsonl"

"$BIN" db migrate >/dev/null
"$BIN" db import-runs >/dev/null
"$BIN" web --host 0.0.0.0 --port "$WEB_PORT" --remote readonly >"$WORK/web.log" 2>&1 & PIDS+=($!)
for _ in $(seq 40); do curl -sf "http://$LAN:$WEB_PORT/api/session" >/dev/null 2>&1 && break; sleep 0.25; done
[ "$(curl -s "http://127.0.0.1:$WEB_PORT/api/session")" = '{"readonly":false}' ] || { echo "owner session is not full access"; exit 1; }
[ "$(curl -s "http://$LAN:$WEB_PORT/api/session")" = '{"readonly":true}' ] || { echo "LAN session is not read-only"; exit 1; }

# /api/pipeline never waits for a data source: the first request only starts the
# background build and returns an empty board. A deployed server has already
# published a board, so warm this one the same way before opening a browser.
warm=0
for _ in $(seq 60); do
  if curl -sf "http://$LAN:$WEB_PORT/api/pipeline" | python3 -c "import json,sys; sys.exit(0 if json.load(sys.stdin)['repos'] else 1)" 2>/dev/null; then warm=1; break; fi
  sleep 0.5
done
[ "$warm" = 1 ] || { echo "FAIL the board never produced a repository entry"; exit 1; }

OUT="${OUT:-$WORK/shots}"; mkdir -p "$OUT"
fail=0
BASE_URL="http://$LAN:$WEB_PORT" CHROME="$CHROME" OUT="$OUT" node "$ROOT/web/scripts/check-readonly-history.mjs" > "$WORK/result.txt" || fail=1
cat "$WORK/result.txt"
if grep -q '^FAIL' "$WORK/result.txt"; then fail=1; fi
n="$(wc -l < "$WORK/provider.requests" | tr -d ' ')"
if [ "$n" != 0 ]; then echo "FAIL provider received $n request(s) while pages were read"; fail=1; else echo "PASS provider received 0 requests while pages were read"; fi
if cmp -s "$WORK/config.before" "$HOME/.clawflow/config/config.yaml"; then echo "PASS repository configuration unchanged"; else echo "FAIL repository configuration changed"; fail=1; fi
[ -n "${OUT_KEEP:-}" ] && echo "screenshots: $OUT"
exit $fail
