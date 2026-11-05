#!/usr/bin/env bash
# Builds passage-cli with the iroh helper as
# passage-cli-<version>-<os>-<arch>.tar.gz in <out-dir>. Windows gets
# passage-<version>-windows-<arch>.zip, which adds passage.exe, the app that
# runs in the notification area.
# Usage: build-cli.sh <version> <out-dir> [os/arch ...]
# Targets: linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64
# windows/arm64 (default: the Linux and macOS ones).
# The helper is built from source with Rust for each target (Linux needs
# musl-tools on a host of the same architecture, Windows builds on Windows).
# PASSAGE_IROH_DIR instead takes prebuilt helpers from
# <dir>/<os>-<arch>/passage-iroh[.exe]. PYTHON names the interpreter.
set -euo pipefail

version=$1
root="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$2"
out="$(cd "$2" && pwd)"
shift 2
targets=("$@")
[[ ${#targets[@]} -gt 0 ]] || targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64)
link_flags=""
if [[ -n "${PASSAGE_LINK_AUTHOR_KEY:-}" ]]; then
  [[ "$PASSAGE_LINK_AUTHOR_KEY" =~ ^[A-Za-z0-9_-]{43}$ ]] || { echo "invalid author public key" >&2; exit 1; }
  link_flags="-X github.com/mrgluek/passage/core/tunnel.AuthorKey=$PASSAGE_LINK_AUTHOR_KEY"
fi
if [[ -n "${PASSAGE_UPDATE_PUBLIC_KEY:-}" ]]; then
  [[ "$PASSAGE_UPDATE_PUBLIC_KEY" =~ ^[A-Za-z0-9_-]{43}$ ]] || { echo "invalid update public key" >&2; exit 1; }
  link_flags+=" -X github.com/mrgluek/passage/core/update.PublicKey=$PASSAGE_UPDATE_PUBLIC_KEY"
fi
if [[ -n "${PASSAGE_UPDATE_SOURCES:-}" ]]; then
  [[ "$PASSAGE_UPDATE_SOURCES" =~ ^[^[:space:]\'\"]+$ ]] || { echo "invalid update sources" >&2; exit 1; }
  link_flags+=" -X github.com/mrgluek/passage/core/update.ExtraSources=$PASSAGE_UPDATE_SOURCES"
fi

work=$(mktemp -d)
syso="$root/core/cmd/passage/rsrc_windows.syso"
trap 'rm -rf "$work" "$syso"' EXIT
# go-winres wants a numeric version.
file_version=$version
[[ $file_version =~ ^[0-9]+(\.[0-9]+){0,3}$ ]] || file_version=0.0.0

cd "$root/core"
for target in "${targets[@]}"; do
  goos=${target%/*} goarch=${target#*/}
  case $target in
    linux/amd64) rust=x86_64-unknown-linux-musl ;;
    linux/arm64) rust=aarch64-unknown-linux-musl ;;
    darwin/amd64) rust=x86_64-apple-darwin ;;
    darwin/arm64) rust=aarch64-apple-darwin ;;
    windows/amd64) rust=x86_64-pc-windows-msvc ;;
    windows/arm64) rust=aarch64-pc-windows-msvc ;;
    *) echo "unsupported target $target" >&2; exit 1 ;;
  esac
  exe=""
  [[ $goos == windows ]] && exe=.exe
  name="passage-cli-$version-$goos-$goarch"
  [[ $goos == windows ]] && name="passage-$version-$goos-$goarch"
  mkdir "$work/$name"
  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
    go build -trimpath -buildvcs=false -ldflags="$link_flags -X main.version=$version -s -w -buildid=" \
    -o "$work/$name/passage-cli$exe" ./cmd/passage-cli
  if [[ $goos == windows ]]; then
    # The icon (resource ID 1, which the window uses), version info and a
    # manifest for per-monitor DPI.
    (cd cmd/passage && go run github.com/tc-hib/go-winres@v0.3.3 simply --arch "$goarch" --no-suffix \
      --out rsrc_windows.syso --icon icons/passage.ico --manifest gui \
      --product-name Passage --file-description Passage --original-filename passage.exe \
      --product-version "$file_version" --file-version "$file_version")
    CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
      go build -trimpath -buildvcs=false -ldflags="$link_flags -X main.version=$version -s -w -buildid= -H windowsgui" \
      -o "$work/$name/passage.exe" ./cmd/passage
    rm -f "$syso"
  fi
  if [[ -n "${PASSAGE_IROH_DIR:-}" ]]; then
    install -m 755 "$PASSAGE_IROH_DIR/$goos-$goarch/passage-iroh$exe" "$work/$name/passage-iroh$exe"
  else
    "${PYTHON:-python3}" "$root/scripts/prepare-iroh.py" desktop "$rust" "$work/$name/passage-iroh$exe"
  fi
  cp "$root/LICENSE" "$work/$name/"
  if [[ $goos == windows ]]; then
    (cd "$work" && "${PYTHON:-python3}" -m zipfile -c "$out/$name.zip" "$name")
  else
    tar -C "$work" -czf "$out/$name.tar.gz" "$name"
  fi
done
ls -l "$out"/passage-*
