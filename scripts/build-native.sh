#!/usr/bin/env bash
# Builds libpassage.so for arm64-v8a and armeabi-v7a into the Android project.
# Needs ANDROID_HOME with NDK 27.2.12479018 and Go from core/go.mod.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
case "$(uname -s)" in
  Linux) host=linux-x86_64 ;;
  Darwin) host=darwin-x86_64 ;; # the NDK ships universal binaries under this name
  *) echo "unsupported host $(uname -s)" >&2; exit 1 ;;
esac
ndk="$ANDROID_HOME/ndk/27.2.12479018/toolchains/llvm/prebuilt/$host"
libs="$root/android/app/src/main/nativeLibs"

build() { # abi goarch clang-triple
  local abi=$1 arch=$2 cc=$3
  mkdir -p "$libs/$abi"
  # 16 KB page alignment: required for 64-bit native libraries on Android 15+.
  CGO_ENABLED=1 GOOS=android GOARCH="$arch" GOARM=7 CC="$ndk/bin/${cc}24-clang" \
    go build -trimpath -buildvcs=false -buildmode=c-shared \
    -ldflags="-s -w -buildid= -extldflags=-Wl,-z,max-page-size=16384" \
    -o "$libs/$abi/libpassage.so" ./cmd/libpassage
  rm -f "$libs/$abi/libpassage.h"
}

cd "$root/core"
build arm64-v8a arm64 aarch64-linux-android
build armeabi-v7a arm armv7a-linux-androideabi
ls -l "$libs"/*
