#!/usr/bin/env bash
# Builds passage-cli for Linux and macOS (amd64, arm64) as
# passage-cli-<version>-<os>-<arch>.tar.gz in <out-dir>.
# Usage: build-cli.sh <version> <out-dir>
set -euo pipefail

version=$1
root="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$2"
out="$(cd "$2" && pwd)"
link_flags=""
if [[ -n "${PASSAGE_LINK_AUTHOR_KEY:-}" ]]; then
  [[ "$PASSAGE_LINK_AUTHOR_KEY" =~ ^[A-Za-z0-9_-]{43}$ ]] || { echo "invalid author public key" >&2; exit 1; }
  link_flags="-X github.com/mrgluek/passage/core/tunnel.AuthorKey=$PASSAGE_LINK_AUTHOR_KEY"
fi
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

cd "$root/core"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  goos=${target%/*} goarch=${target#*/}
  name="passage-cli-$version-$goos-$goarch"
  mkdir "$work/$name"
  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
    go build -trimpath -buildvcs=false -ldflags="$link_flags -s -w -buildid=" \
    -o "$work/$name/passage-cli" ./cmd/passage-cli
  cp "$root/LICENSE" "$work/$name/"
  tar -C "$work" -czf "$out/$name.tar.gz" "$name"
done
ls -l "$out"/passage-cli-*.tar.gz
