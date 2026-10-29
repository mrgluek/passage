#!/usr/bin/env bash
# Builds libpassage.so for arm64-v8a into the Android project.
# Needs ANDROID_HOME with NDK 27.2.12479018 and Go from core/go.mod.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
case "$(uname -s)" in
  Linux) host=linux-x86_64 ;;
  Darwin) host=darwin-x86_64 ;; # the NDK ships universal binaries under this name
  *) echo "unsupported host $(uname -s)" >&2; exit 1 ;;
esac
ndk="$ANDROID_HOME/ndk/27.2.12479018/toolchains/llvm/prebuilt/$host"
out="$root/android/app/src/main/nativeLibs/arm64-v8a"
mkdir -p "$out"

cd "$root/core"
# 16 KB page alignment: required for native libraries on Android 15+.
CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC="$ndk/bin/aarch64-linux-android24-clang" \
  go build -trimpath -buildvcs=false -buildmode=c-shared \
  -ldflags="-s -w -buildid= -extldflags=-Wl,-z,max-page-size=16384" \
  -o "$out/libpassage.so" ./cmd/libpassage
rm -f "$out/libpassage.h"
ls -l "$out"
