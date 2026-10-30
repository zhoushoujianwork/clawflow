#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
swift build --package-path macos -c release
app="macos/dist/ClawFlow.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Helpers"
cp macos/.build/release/ClawFlow "$app/Contents/MacOS/ClawFlow"
cp clawflow "$app/Contents/Helpers/clawflow"
cat > "$app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.clawflow.dashboard</string>
<key>CFBundleName</key><string>ClawFlow</string>
<key>CFBundleExecutable</key><string>ClawFlow</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>0.1.0</string>
<key>CFBundleVersion</key><string>1</string>
<key>LSMinimumSystemVersion</key><string>14.0</string>
<key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
codesign --force --sign - "$app/Contents/Helpers/clawflow"
codesign --force --sign - "$app"
plutil -lint "$app/Contents/Info.plist"
echo "Built $app (not launched or installed)."
