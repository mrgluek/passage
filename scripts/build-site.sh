#!/usr/bin/env bash
# Builds the website with the F-Droid repository for one release.
# Usage: build-site.sh <version> <apk-dir> <out-dir>
# <apk-dir> holds passage-<version>-{arm64,armv7}.apk, signed.
# Needs fdroid (fdroidserver), python3 with qrcode, keytool, ANDROID_HOME,
# and the repository key: FDROID_KEYSTORE (PKCS12), FDROID_KEYSTORE_PASS,
# FDROID_KEY_ALIAS. FDROID_MIRRORS may list mirrors, one URL per line, each
# ending in /fdroid like the main one.
set -euo pipefail

version=$1
apks=$(cd "$2" && pwd)
out=$3
site_url=https://passage.gluek.info
repo_url=$site_url/fdroid/repo
app=info.gluek.passage

root="$(cd "$(dirname "$0")/.." && pwd)"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Only this release: the universal APK is left out, since F-Droid clients
# pick the APK for the phone's ABI.
mkdir -p "$work/repo" "$work/metadata/$app"
cp "$apks/passage-$version-arm64.apk" "$apks/passage-$version-armv7.apk" "$work/repo/"
cp "$root/fdroid/metadata/$app.yml" "$work/metadata/"
cp -R "$root/fastlane/metadata/android/." "$work/metadata/$app/"
cp "$root/fastlane/metadata/android/en-US/images/icon.png" "$work/icon.png"

{
  echo "repo_url: $repo_url"
  echo "repo_name: Passage"
  echo "repo_description: Releases of Passage, a local SOCKS5 proxy for Android."
  echo "repo_icon: icon.png"
  echo "archive_older: 0"
  echo "sdk_path: $ANDROID_HOME"
  echo "keystore: $FDROID_KEYSTORE"
  echo "repo_keyalias: $FDROID_KEY_ALIAS"
  echo "keystorepass: {env: FDROID_KEYSTORE_PASS}"
  echo "keypass: {env: FDROID_KEYSTORE_PASS}"
  if [ -n "${FDROID_MIRRORS:-}" ]; then
    echo "mirrors:"
    printf '%s\n' "$FDROID_MIRRORS" | sed '/^$/d; s/^/  - /'
  fi
} > "$work/config.yml"
chmod 600 "$work/config.yml"

(cd "$work" && fdroid update --pretty)

fingerprint=$(keytool -exportcert -keystore "$FDROID_KEYSTORE" -storetype pkcs12 \
  -storepass:env FDROID_KEYSTORE_PASS -alias "$FDROID_KEY_ALIAS" \
  | shasum -a 256 | cut -d' ' -f1 | tr a-f A-F)
test ${#fingerprint} -eq 64 || { echo "cannot read the repository key" >&2; exit 1; }

rm -rf "$out"
mkdir -p "$out/fdroid"
cp -R "$work/repo" "$out/fdroid/repo"
cp "$root/site/style.css" "$root/site/icon.svg" "$out/"
# App Links: lets Android open https://passage.gluek.info/#… in the app.
cp -R "$root/site/.well-known" "$out/"
python3 - "$repo_url?fingerprint=$fingerprint" "$out/fdroid/qr.svg" <<'EOF'
import sys, qrcode, qrcode.image.svg
qrcode.make(sys.argv[1], image_factory=qrcode.image.svg.SvgPathFillImage, border=1).save(sys.argv[2])
EOF
sed -e "s#{{VERSION}}#$version#g" \
  -e "s#{{RELEASE}}#https://github.com/mrgluek/passage/releases/download/v$version#g" \
  -e "s#{{REPO_URL}}#$repo_url#g" \
  -e "s#{{REPO_LINK}}#fdroidrepos://${repo_url#https://}?fingerprint=$fingerprint#g" \
  -e "s#{{REPO_FINGERPRINT}}#$fingerprint#g" \
  "$root/site/index.html" > "$out/index.html"
# Connection links are https://passage.gluek.info/link#…: the same page,
# whose script offers to open the link in the app.
cp "$out/index.html" "$out/link.html"
echo "site in $out, repository fingerprint $fingerprint"
