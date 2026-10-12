#!/bin/bash
# Builds Music Library Organizer for macOS and installs it into /Applications, then opens it.
#
#   ./build.sh          build, install into /Applications and open the app
#   ./build.sh --zip    also write dist/MusicLibraryOrganizer-<version>-macos-universal.zip
#
# INSTALL_DIR=~/Applications ./build.sh installs somewhere else.
#
# Needs Go and the Xcode command line tools, nothing else: the interface in
# frontend/dist is plain HTML, CSS and JavaScript and is embedded as it is.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
NAME="Music Library Organizer"
BUNDLE="MusicLibraryOrganizer.app"
BUNDLE_ID="com.musiclibraryorganizer.app"
VERSION="$(sed -n 's/.*"productVersion": *"\([^"]*\)".*/\1/p' "$ROOT/wails.json")"
MACOS_MIN="13.0" # the oldest macOS the Go toolchain builds for
DEST="${INSTALL_DIR:-/Applications}"
ZIP=0
[ "${1:-}" = "--zip" ] && ZIP=1

# Build outside the project, in a copy of it: synced folders (iCloud Documents)
# attach extended attributes to everything, and codesign refuses bundles that
# carry them. The copy also takes the freshly built phone agent, so the
# project's own files are never touched.
OUT="$(mktemp -d -t music-library-organizer)"
trap 'rm -rf "$OUT"' EXIT
SRC="$OUT/src"
APP="$OUT/$BUNDLE"
ICONSET="$OUT/AppIcon.iconset"

echo "Building $NAME $VERSION..."
rsync -a --exclude /.git --exclude /build --exclude /dist --exclude "/$NAME.exe" "$ROOT/" "$SRC/"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources" "$ICONSET"

# The phone agent is embedded into the application, so it is built first: an
# application built against a stale agent reads tags on the phone with old code.
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go -C "$SRC" build -trimpath -buildvcs=false -ldflags="-s -w" \
  -o internal/device/agent/mlm-agent-arm64 ./cmd/agent

# The application, once per architecture, joined into one universal binary.
# "desktop,production" are the tags `wails build` passes for a release build.
for arch in arm64 amd64; do
  CGO_ENABLED=1 GOOS=darwin GOARCH=$arch \
  CGO_CFLAGS="-mmacosx-version-min=$MACOS_MIN" \
  CGO_LDFLAGS="-framework UniformTypeIdentifiers -mmacosx-version-min=$MACOS_MIN" \
    go -C "$SRC" build -trimpath -buildvcs=false -tags desktop,production -ldflags="-w -s" \
    -o "$OUT/app-$arch" .
done
lipo -create "$OUT/app-arm64" "$OUT/app-amd64" -output "$APP/Contents/MacOS/$NAME"

for spec in \
  "16 icon_16x16.png" "32 icon_16x16@2x.png" \
  "32 icon_32x32.png" "64 icon_32x32@2x.png" \
  "128 icon_128x128.png" "256 icon_128x128@2x.png" \
  "256 icon_256x256.png" "512 icon_256x256@2x.png" \
  "512 icon_512x512.png" "1024 icon_512x512@2x.png"; do
  sips -z "${spec%% *}" "${spec%% *}" "$ROOT/packaging/appicon.png" --out "$ICONSET/${spec#* }" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/AppIcon.icns"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleName</key><string>$NAME</string>
<key>CFBundleDisplayName</key><string>$NAME</string>
<key>CFBundleIdentifier</key><string>$BUNDLE_ID</string>
<key>CFBundleExecutable</key><string>$NAME</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleVersion</key><string>$VERSION</string>
<key>CFBundleShortVersionString</key><string>$VERSION</string>
<key>CFBundleIconFile</key><string>AppIcon.icns</string>
<key>LSMinimumSystemVersion</key><string>$MACOS_MIN</string>
<key>LSApplicationCategoryType</key><string>public.app-category.music</string>
<key>NSHumanReadableCopyright</key><string>Copyright (c) 2026 Kreal</string>
<key>NSHighResolutionCapable</key><true/>
<key>NSAppleEventsUsageDescription</key><string>Music Library Organizer reads the playlists in Music, so they can be saved for another player.</string>
</dict></plist>
PLIST

# Ad-hoc signature: required on Apple Silicon, and keeps the bundle intact in a zip.
codesign --force --deep --sign - "$APP" >/dev/null 2>&1
codesign --verify --deep --strict "$APP"

if [ "$ZIP" = 1 ]; then
  mkdir -p "$ROOT/dist"
  ZIPFILE="$ROOT/dist/MusicLibraryOrganizer-$VERSION-macos-universal.zip"
  rm -f "$ZIPFILE"
  ditto -c -k --norsrc --keepParent "$APP" "$ZIPFILE"
  echo "Release zip: $ZIPFILE"
fi

# Quit a running copy first, and open the new one afterwards.
WAS_RUNNING=0
if pgrep -f "$DEST/$BUNDLE/Contents/MacOS/" >/dev/null 2>&1; then
  WAS_RUNNING=1
  echo "Quitting the running $NAME..."
  osascript -e "tell application id \"$BUNDLE_ID\" to quit" >/dev/null 2>&1 || true
  for _ in $(seq 1 50); do
    pgrep -f "$DEST/$BUNDLE/Contents/MacOS/" >/dev/null 2>&1 || break
    sleep 0.2
  done
  if pgrep -f "$DEST/$BUNDLE/Contents/MacOS/" >/dev/null 2>&1; then
    echo "$NAME is still running. Quit it and run the build again." >&2
    exit 1
  fi
fi

mkdir -p "$DEST"
rm -rf "${DEST:?}/$BUNDLE"
ditto "$APP" "$DEST/$BUNDLE"
echo "Installed: $DEST/$BUNDLE"

# Opened right away, except on a CI runner.
[ -n "${CI:-}" ] || open "$DEST/$BUNDLE"
exit 0
