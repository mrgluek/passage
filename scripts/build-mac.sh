#!/usr/bin/env bash
# Builds an ad hoc signed Passage.app for local development on macOS.
# Usage: build-mac.sh <version> <out-dir> [arm64|amd64]
# PASSAGE_IROH_DIR optionally supplies <dir>/darwin-<arch>/passage-iroh.
set -euo pipefail

[[ $(uname -s) == Darwin ]] || { echo "build on macOS with Xcode command line tools" >&2; exit 1; }
version=${1:?usage: build-mac.sh <version> <out-dir> [arm64|amd64]}
root="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "${2:?output directory required}"
out="$(cd "$2" && pwd)"
arch=${3:-$(go env GOARCH)}
case $arch in
  arm64) rust=aarch64-apple-darwin ;;
  amd64) rust=x86_64-apple-darwin ;;
  *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac
[[ $version =~ ^[A-Za-z0-9][A-Za-z0-9.+-]*$ ]] || { echo "invalid version" >&2; exit 1; }
bundle_version=$version
[[ $bundle_version =~ ^[0-9]+(\.[0-9]+){0,2}$ ]] || bundle_version=0.0.0
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
app="$work/Passage.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
link_flags=""
if [[ -n "${PASSAGE_LINK_AUTHOR_KEY:-}" ]]; then
  [[ "$PASSAGE_LINK_AUTHOR_KEY" =~ ^[A-Za-z0-9_-]{43}$ ]] || { echo "invalid author public key" >&2; exit 1; }
  link_flags="-X github.com/mrgluek/passage/core/tunnel.AuthorKey=$PASSAGE_LINK_AUTHOR_KEY"
fi
export MACOSX_DEPLOYMENT_TARGET=13.0
cd "$root/core"
CGO_ENABLED=1 GOOS=darwin GOARCH="$arch" go build -trimpath -buildvcs=false \
  -ldflags="$link_flags -X main.version=$version -s -w" \
  -o "$app/Contents/MacOS/passage" ./cmd/passage
if [[ -n "${PASSAGE_IROH_DIR:-}" ]]; then
  install -m 755 "$PASSAGE_IROH_DIR/darwin-$arch/passage-iroh" "$app/Contents/MacOS/passage-iroh"
else
  "${PYTHON:-python3}" "$root/scripts/prepare-iroh.py" desktop "$rust" "$app/Contents/MacOS/passage-iroh"
fi
cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleIdentifier</key><string>info.gluek.passage</string>
  <key>CFBundleName</key><string>Passage</string>
  <key>CFBundleExecutable</key><string>passage</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$bundle_version</string>
  <key>CFBundleVersion</key><string>$bundle_version</string>
  <key>CFBundleIconFile</key><string>Passage</string>
  <key>LSMinimumSystemVersion</key><string>13.0</string>
  <key>LSUIElement</key><true/>
  <key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
icons="$work/Passage.iconset"
mkdir "$icons"
for size in 16 32 128 256 512; do
  sips -z "$size" "$size" "$root/fastlane/metadata/android/en-US/images/icon.png" \
    --out "$icons/icon_${size}x${size}.png" >/dev/null
  double=$((size * 2))
  sips -z "$double" "$double" "$root/fastlane/metadata/android/en-US/images/icon.png" \
    --out "$icons/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$icons" -o "$app/Contents/Resources/Passage.icns"
cp "$root/LICENSE" "$app/Contents/Resources/"
codesign --force --sign - "$app/Contents/MacOS/passage-iroh"
codesign --force --sign - "$app"
archive="$out/passage-$version-darwin-$arch.zip"
ditto -c -k --keepParent "$app" "$archive"
echo "$archive"
