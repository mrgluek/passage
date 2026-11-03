#!/usr/bin/env bash
# Builds passage-cli with the iroh helper as
# passage-cli-<version>-<os>-<arch>.tar.gz in <out-dir>.
# Usage: build-cli.sh <version> <out-dir> [os/arch ...]
# Targets: linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 (default: all).
# The helper is built from source with Rust for each target (Linux needs
# musl-tools on a host of the same architecture). PASSAGE_IROH_DIR instead
# takes prebuilt helpers from <dir>/<os>-<arch>/passage-iroh.
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
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

cd "$root/core"
for target in "${targets[@]}"; do
  goos=${target%/*} goarch=${target#*/}
  case $target in
    linux/amd64) rust=x86_64-unknown-linux-musl ;;
    linux/arm64) rust=aarch64-unknown-linux-musl ;;
    darwin/amd64) rust=x86_64-apple-darwin ;;
    darwin/arm64) rust=aarch64-apple-darwin ;;
    *) echo "unsupported target $target" >&2; exit 1 ;;
  esac
  name="passage-cli-$version-$goos-$goarch"
  mkdir "$work/$name"
  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
    go build -trimpath -buildvcs=false -ldflags="$link_flags -s -w -buildid=" \
    -o "$work/$name/passage-cli" ./cmd/passage-cli
  if [[ -n "${PASSAGE_IROH_DIR:-}" ]]; then
    install -m 755 "$PASSAGE_IROH_DIR/$goos-$goarch/passage-iroh" "$work/$name/passage-iroh"
  else
    python3 "$root/scripts/prepare-iroh.py" desktop "$rust" "$work/$name/passage-iroh"
  fi
  cp "$root/LICENSE" "$work/$name/"
  tar -C "$work" -czf "$out/$name.tar.gz" "$name"
done
ls -l "$out"/passage-cli-*.tar.gz
