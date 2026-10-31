#!/bin/sh
# Builds the native app bundle. Never launches or installs it.
#
#   macos/build-app.sh                       stable bundle  com.clawflow.dashboard   → macos/dist/ClawFlow.app
#   CLAWFLOW_CANDIDATE_ID=<12 hex> \
#   CLAWFLOW_CANDIDATE_OUT=<dir> \
#   macos/build-app.sh                       dev bundle     com.clawflow.dashboard.dev.<id> → <dir>/ClawFlow Dev.app
#
# A candidate build needs its own bundle identifier (so its preferences domain
# differs) and an artifact directory that is not the stable macos/dist.
set -eu
cd "$(dirname "$0")/.."
bundle_id="com.clawflow.dashboard"
name="ClawFlow"
out="macos/dist"
build_path="macos/.build"
if [ -n "${CLAWFLOW_CANDIDATE_ID:-}" ]; then
  case "$CLAWFLOW_CANDIDATE_ID" in
    *[!0-9a-f]*|"") echo "CLAWFLOW_CANDIDATE_ID must be 12 lowercase hex characters" >&2; exit 2 ;;
  esac
  [ "${#CLAWFLOW_CANDIDATE_ID}" -eq 12 ] || { echo "CLAWFLOW_CANDIDATE_ID must be 12 lowercase hex characters" >&2; exit 2; }
  [ -n "${CLAWFLOW_CANDIDATE_OUT:-}" ] || { echo "CLAWFLOW_CANDIDATE_OUT is required for a candidate build" >&2; exit 2; }
  case "$CLAWFLOW_CANDIDATE_OUT" in /*) ;; *) echo "CLAWFLOW_CANDIDATE_OUT must be absolute" >&2; exit 2 ;; esac
  stable_dist="$(pwd -P)/macos/dist"
  # Refuse before creating anything. Compare the lexical path and the path with
  # its deepest existing ancestor resolved, so symlinks cannot redirect it.
  probe="$CLAWFLOW_CANDIDATE_OUT"
  tail_part=""
  while [ ! -d "$probe" ] && [ "$probe" != "/" ]; do tail_part="/$(basename "$probe")$tail_part"; probe="$(dirname "$probe")"; done
  resolved="$(cd "$probe" && pwd -P)$tail_part"
  for candidate_path in "$CLAWFLOW_CANDIDATE_OUT" "$resolved"; do
    case "$candidate_path/" in
      "$stable_dist"/*|"$HOME/Applications/"*|/Applications/*) echo "refusing candidate artifact path $candidate_path: it overlaps an installed or stable location" >&2; exit 2 ;;
    esac
  done
  mkdir -p "$resolved"
  bundle_id="com.clawflow.dashboard.dev.$CLAWFLOW_CANDIDATE_ID"
  name="ClawFlow Dev"
  out="$resolved"
  build_path="$resolved/swift-build"
fi
[ "$bundle_id" != "com.clawflow.dashboard" ] || [ -z "${CLAWFLOW_CANDIDATE_ID:-}" ] || { echo "candidate build resolved to the stable bundle id" >&2; exit 2; }
swift build --package-path macos --build-path "$build_path" -c release
app="$out/$name.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Helpers"
cp "$build_path/release/ClawFlow" "$app/Contents/MacOS/ClawFlow"
cli="${CLAWFLOW_CANDIDATE_CLI:-clawflow}"
cp "$cli" "$app/Contents/Helpers/clawflow"
cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>$bundle_id</string>
<key>CFBundleName</key><string>$name</string>
<key>CFBundleExecutable</key><string>ClawFlow</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>0.1.0</string>
<key>CFBundleVersion</key><string>1</string>
<key>LSMinimumSystemVersion</key><string>14.0</string>
<key>LSUIElement</key><true/>
<key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
python3 - "$app/Contents/Info.plist" "${CLAWFLOW_APP_VERSION:-$(git describe --tags --always)}" "${CLAWFLOW_APP_CHANNEL:-source}" <<'PYMETA'
import plistlib, sys, time
path, version, channel = sys.argv[1:]
with open(path, 'rb') as f: info = plistlib.load(f)
info.update(CFBundleShortVersionString=version.removeprefix('v'), CFBundleVersion=str(int(time.time())), ClawFlowBuildChannel=channel)
with open(path, 'wb') as f: plistlib.dump(info, f)
PYMETA
codesign --force --sign - "$app/Contents/Helpers/clawflow"
codesign --force --sign - "$app"
plutil -lint "$app/Contents/Info.plist"
echo "Built $app ($bundle_id; not launched or installed)."
